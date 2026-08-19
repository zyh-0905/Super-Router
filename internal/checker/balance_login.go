package checker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"
)

// ==================== Sub2API 余额自动登录 ====================
// 站点配置了余额登录邮箱/密码后，checker 自动登录换取会话 JWT 查询余额，
// 免去用户抓包手动维护令牌。令牌缓存于 Redis（TTL 跟随 expires_in），
// 余额接口返回 401 时清除缓存并重新登录重试一次。

// sub2apiSession Sub2API 登录响应（顶层字段与 data 包装均兼容）
type sub2apiSession struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"` // 秒
}

// tokenTTLBuffer 令牌实际过期前多久视为过期（秒），留出请求耗时缓冲
const tokenTTLBuffer = 300

func sub2apiTokenKey(channelID int) string {
	return fmt.Sprintf("balance:sub2api:token:%d", channelID)
}

// loginSub2API 用邮箱密码登录 Sub2API 站点换取会话令牌
func (b *BalanceChecker) loginSub2API(ctx context.Context, baseURL, email, password string) (*sub2apiSession, error) {
	payload, _ := json.Marshal(map[string]string{"email": email, "password": password})
	url := strings.TrimRight(strings.TrimSpace(baseURL), "/") + "/api/v1/auth/login"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("构建登录请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("登录请求失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	var raw struct {
		Code    string          `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	_ = json.Unmarshal(body, &raw)

	var sess sub2apiSession
	switch {
	case len(raw.Data) > 0:
		if err := json.Unmarshal(raw.Data, &sess); err == nil && sess.AccessToken != "" {
			return &sess, nil
		}
	case raw.Code == "" || raw.Code == "0":
		// 无 data 包装：令牌可能在顶层
		if err := json.Unmarshal(body, &sess); err == nil && sess.AccessToken != "" {
			return &sess, nil
		}
	}

	msg := raw.Message
	if msg == "" {
		msg = raw.Code
	}
	if msg == "" {
		msg = "登录响应缺少 access_token"
	}
	return nil, fmt.Errorf("自动登录失败: %s", msg)
}

// getSub2APIToken 获取站点会话令牌：优先 Redis 缓存，未命中则登录并缓存
func (b *BalanceChecker) getSub2APIToken(ctx context.Context, u Upstream) (string, error) {
	key := sub2apiTokenKey(u.ID)
	if b.redisUsable() {
		if tok, err := b.redis.Client.Get(ctx, key).Result(); err == nil && tok != "" {
			return tok, nil
		}
	}

	sess, err := b.loginSub2API(ctx, u.BaseURL, u.BalanceLoginEmail, u.BalanceLoginPassword)
	if err != nil {
		b.logger.Warn("Sub2API auto login failed",
			zap.Int("channel_id", u.ID), zap.String("email", u.BalanceLoginEmail), zap.Error(err))
		return "", err
	}
	if sess.AccessToken == "" {
		return "", fmt.Errorf("自动登录失败: 登录响应缺少 access_token")
	}

	b.logger.Info("Sub2API auto login succeeded",
		zap.Int("channel_id", u.ID), zap.String("email", u.BalanceLoginEmail))

	if b.redisUsable() {
		ttl := time.Duration(sess.ExpiresIn)*time.Second - tokenTTLBuffer*time.Second
		if ttl <= 0 {
			ttl = 5 * time.Minute
		}
		if err := b.redis.Client.Set(ctx, key, sess.AccessToken, ttl).Err(); err != nil {
			b.logger.Warn("Cache sub2api token failed", zap.Int("channel_id", u.ID), zap.Error(err))
		}
	}
	return sess.AccessToken, nil
}

// clearSub2APIToken 清除站点会话令牌缓存（401 后强制重新登录）
func (b *BalanceChecker) clearSub2APIToken(ctx context.Context, channelID int) {
	if !b.redisUsable() {
		return
	}
	if err := b.redis.Client.Del(ctx, sub2apiTokenKey(channelID)).Err(); err != nil {
		b.logger.Warn("Clear sub2api token failed", zap.Int("channel_id", channelID), zap.Error(err))
	}
}

func (b *BalanceChecker) redisUsable() bool {
	return b.redis != nil && b.redis.Client != nil
}

// isUnauthorizedErr 判断余额接口错误是否为认证失败（HTTP 401）
func isUnauthorizedErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "HTTP 401")
}

// balanceCredential 解析站点余额凭据：自动登录会话（配置了邮箱密码时优先，免手动维护令牌）
// > 独立令牌 > API Key > Access Token；自动登录失败时回退静态令牌链并记录原因。
func (b *BalanceChecker) balanceCredential(ctx context.Context, upstream Upstream) (cred string, auto bool, loginErr error) {
	if upstream.BalanceLoginEmail != "" && upstream.BalanceLoginPassword != "" {
		if tok, err := b.getSub2APIToken(ctx, upstream); err == nil {
			return tok, true, nil
		} else {
			loginErr = err
		}
	}
	cred = upstream.BalanceAPIToken
	if cred == "" {
		cred = upstream.APIKey
	}
	if cred == "" {
		cred = upstream.AccessToken
	}
	return cred, false, loginErr
}

// fetchWithAutoRetry 调用余额接口；凭据来自自动登录且返回 401 时，
// 清除缓存重新登录并重试一次（登录站点可能轮换了会话）。
func (b *BalanceChecker) fetchWithAutoRetry(ctx context.Context, url, cred string, auto bool, upstream Upstream) (float64, string, error) {
	bal, src, err := b.fetchGeneric(ctx, url, cred)
	if err != nil && auto && isUnauthorizedErr(err) {
		b.clearSub2APIToken(ctx, upstream.ID)
		if tok, lerr := b.getSub2APIToken(ctx, upstream); lerr == nil && tok != cred {
			return b.fetchGeneric(ctx, url, tok)
		}
	}
	return bal, src, err
}

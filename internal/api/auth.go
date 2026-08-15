package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"smart-router/internal/store"

	"github.com/gin-gonic/gin"
)

// AuthMiddleware API Key 认证中间件
func AuthMiddleware(db *store.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 从请求头读取 Authorization
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(401, gin.H{
				"error": "missing authorization header",
			})
			c.Abort()
			return
		}

		// 解析 Bearer token
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || parts[0] != "Bearer" {
			c.JSON(401, gin.H{
				"error": "invalid authorization header format, expected: Bearer <token>",
			})
			c.Abort()
			return
		}

		apiKey := parts[1]

		// 计算 key_hash
		keyHash := hashAPIKey(apiKey)

		// 从数据库验证
		ctx := context.Background()
		var role string
		var enabled bool
		err := db.Pool.QueryRow(ctx, `
			SELECT role, enabled
			FROM api_keys
			WHERE key_hash = $1
		`, keyHash).Scan(&role, &enabled)

		if err != nil {
			c.JSON(401, gin.H{
				"error": "invalid api key",
			})
			c.Abort()
			return
		}

		if !enabled {
			c.JSON(403, gin.H{
				"error": "api key disabled",
			})
			c.Abort()
			return
		}

		// 将认证信息存入上下文
		c.Set("api_key", apiKey)
		c.Set("key_hash", keyHash)
		c.Set("role", role)

		// 加载 Key 绑定分组（空 = 不限制）
		groupIDs, err := loadKeyGroupIDs(db, keyHash)
		if err != nil {
			c.JSON(500, gin.H{"error": "failed to load key groups"})
			c.Abort()
			return
		}
		c.Set("key_groups", groupIDs)

		c.Next()
	}
}

// loadKeyGroupIDs 读取 API Key 绑定的分组 ID 列表（空切片 = 不限制）
func loadKeyGroupIDs(db *store.DB, keyHash string) ([]int, error) {
	rows, err := db.Pool.Query(context.Background(), `
		SELECT ag.group_id
		FROM api_key_groups ag
		JOIN api_keys k ON k.id = ag.api_key_id
		WHERE k.key_hash = $1
		ORDER BY ag.group_id
	`, keyHash)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			continue
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// RequireRole 要求特定角色
func RequireRole(role string) gin.HandlerFunc {
	return func(c *gin.Context) {
		userRole, exists := c.Get("role")
		if !exists {
			c.JSON(401, gin.H{
				"error": "authentication required",
			})
			c.Abort()
			return
		}

		if userRole != role && userRole != "admin" {
			c.JSON(403, gin.H{
				"error": fmt.Sprintf("requires %s role", role),
			})
			c.Abort()
			return
		}

		c.Next()
	}
}

func hashAPIKey(apiKey string) string {
	hash := sha256.Sum256([]byte(apiKey))
	return fmt.Sprintf("%x", hash)
}

// EnsureDefaultKeys 初始化管理员 Key：
// - bootstrap=true（本地开发）：表为空时写入 test-admin-key / test-caller-key；
// - bootstrap=false（生产）：表为空时生成随机管理员 Key，仅通过日志打印一次，
//   返回该 Key 供启动日志展示，绝不写死公开凭据。
func EnsureDefaultKeys(db *store.DB, bootstrap bool) (string, error) {
	ctx := context.Background()

	var count int
	if err := db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM api_keys`).Scan(&count); err != nil {
		return "", err
	}
	if count > 0 {
		return "", nil
	}

	if bootstrap {
		defaults := []struct {
			key  string
			role string
		}{
			{"test-admin-key", "admin"},
			{"test-caller-key", "caller"},
		}

		for _, d := range defaults {
			_, err := db.Pool.Exec(ctx, `
				INSERT INTO api_keys (key_hash, key_prefix, role, enabled)
				VALUES ($1, $2, $3, true)
				ON CONFLICT (key_hash) DO NOTHING
			`, hashAPIKey(d.key), d.key[:8], d.role)
			if err != nil {
				return "", err
			}
		}
		return "", nil
	}

	// 生产模式：随机生成一次性管理员 Key
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	generated := "sr-" + hex.EncodeToString(buf)
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO api_keys (key_hash, key_prefix, role, enabled)
		VALUES ($1, $2, 'admin', true)
		ON CONFLICT (key_hash) DO NOTHING
	`, hashAPIKey(generated), generated[:8]); err != nil {
		return "", err
	}
	return generated, nil
}

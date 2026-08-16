// Package safenet 校验管理员输入的上游 URL，阻断 SSRF：
// 拒绝环回、RFC1918 私网、链路本地、IPv6 ULA、未指定地址与云元数据 IP。
//
// 说明：本校验在解析时对主机名做 DNS 解析，因此能拦住指向私网的域名，
// 但不能防住校验后发生变化的 DNS 重绑定；生产环境仍建议配合出口网络策略。
package safenet

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// Options 校验选项（由配置注入：开发环境可显式放宽）
type Options struct {
	AllowHTTP    bool // 允许 http://（生产默认仅 https）
	AllowPrivate bool // 允许私网/环回地址（仅限本地开发）
}

// ValidateUpstreamURL 校验上游/余额/探测接口 URL。
func ValidateUpstreamURL(raw string, o Options) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("URL 为空")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("URL 解析失败: %v", err)
	}
	if u.Scheme != "https" {
		if u.Scheme != "http" || !o.AllowHTTP {
			return fmt.Errorf("仅允许 https:// 地址（当前: %s）", u.Scheme)
		}
	}
	if u.Host == "" {
		return fmt.Errorf("URL 缺少主机名")
	}
	if u.User != nil {
		return fmt.Errorf("URL 不允许携带用户信息")
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("URL 缺少主机名")
	}
	// 端口必须是合法数字（url.Parse 已基本保证，这里兜底）
	if port := u.Port(); port != "" {
		if _, err := net.LookupPort("tcp", port); err != nil {
			return fmt.Errorf("非法端口 %q", port)
		}
	}
	return validateHost(host, o)
}

// ValidateRedirect 校验重定向目标（在 http.Client.CheckRedirect 中调用）。
func ValidateRedirect(raw string, o Options) error {
	return ValidateUpstreamURL(raw, o)
}

// validateHost 解析主机名并拒绝所有指向内网的地址。
func validateHost(host string, o Options) error {
	// 常见内网/元数据主机名直判（不依赖 DNS）
	lower := strings.ToLower(strings.TrimSuffix(host, "."))
	if !o.AllowPrivate && (lower == "localhost" || strings.HasSuffix(lower, ".localhost")) {
		return fmt.Errorf("禁止访问 localhost")
	}
	if lower == "metadata.google.internal" || lower == "metadata" ||
		strings.HasSuffix(lower, ".metadata.google.internal") {
		return fmt.Errorf("禁止访问云元数据地址")
	}
	if ip := net.ParseIP(host); ip != nil {
		return validateIP(ip, o)
	}
	if !o.AllowPrivate {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return fmt.Errorf("域名解析失败: %v", err)
		}
		if len(addrs) == 0 {
			return fmt.Errorf("域名 %q 无解析结果", host)
		}
		for _, a := range addrs {
			if err := validateIP(a.IP, o); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateIP(ip net.IP, o Options) error {
	if o.AllowPrivate {
		return nil
	}
	// 云元数据链路本地地址（169.254.0.0/16，含 169.254.169.254）
	if ip.IsLinkLocalUnicast() {
		return fmt.Errorf("禁止访问链路本地地址 %s", ip)
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() || ip.IsMulticast() {
		return fmt.Errorf("禁止访问内网/环回地址 %s", ip)
	}
	return nil
}

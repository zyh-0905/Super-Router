// Package station 中转站归并的共享口径：
// base_url 规范化（归并键）与自动命名。
// Web（internal/api/relay_stations.go）与 Telegram（internal/telegram/query.go）
// 必须共用同一实现，保证两个入口看到的归并结果一致。
package station

import (
	"net/url"
	"strings"
)

// NormalizeBaseURL 规范化站点 base_url（归并键）：
// scheme 与 host 小写、去除尾部斜杠；路径与查询保留原样。
// 解析失败时退化为朴素去尾斜杠（仍可归并字面相同的 URL）。
func NormalizeBaseURL(raw string) string {
	s := strings.TrimSpace(raw)
	u, err := url.Parse(s)
	if err != nil || (u.Scheme == "" && u.Host == "") {
		return strings.TrimRight(s, "/")
	}
	out := strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host)
	if p := strings.TrimRight(u.Path, "/"); p != "" {
		out += p
	}
	if u.RawQuery != "" {
		out += "?" + u.RawQuery
	}
	return out
}

// AutoName 自动命名：去 scheme 的 host + 路径（如 api.247kan.com/v1）。
func AutoName(baseURL string) string {
	s := strings.TrimPrefix(NormalizeBaseURL(baseURL), "://")
	u, err := url.Parse(NormalizeBaseURL(baseURL))
	if err != nil || u.Host == "" {
		return strings.TrimPrefix(strings.TrimPrefix(s, "http://"), "https://")
	}
	out := u.Host
	if p := strings.Trim(u.Path, "/"); p != "" {
		out += "/" + p
	}
	return out
}

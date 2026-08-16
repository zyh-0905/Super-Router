package safenet

import "testing"

func TestValidateUpstreamURL(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		opts Options
		ok   bool
	}{
		// 严格模式：公网 IP 字面量（不依赖测试环境的 DNS）
		{"https public ip", "https://8.8.8.8", Options{}, true},
		{"https public ipv6", "https://[2606:4700:4700::1111]", Options{}, true},
		{"https public ip with path", "https://8.8.8.8/v1", Options{}, true},
		// 开发模式（AllowPrivate）跳过 DNS/内网校验：域名应通过
		{"https hostname allowed in dev", "https://api.example.com", Options{AllowPrivate: true}, true},
		{"http allowed in dev", "http://api.example.com", Options{AllowHTTP: true, AllowPrivate: true}, true},
		// 严格模式拒绝
		{"http blocked by default", "http://8.8.8.8", Options{}, false},
		{"empty", "", Options{}, false},
		{"no host", "https://", Options{}, false},
		{"userinfo", "https://user:pass@8.8.8.8", Options{}, false},
		{"localhost", "https://localhost:8080", Options{}, false},
		{"localhost with allow", "https://localhost:8080", Options{AllowPrivate: true}, true},
		{"loopback", "https://127.0.0.1:3000", Options{}, false},
		{"rfc1918", "http://192.168.1.10", Options{AllowHTTP: true}, false},
		{"rfc1918 with allow", "http://192.168.1.10", Options{AllowHTTP: true, AllowPrivate: true}, true},
		{"link local metadata", "http://169.254.169.254/latest/meta-data", Options{AllowHTTP: true}, false},
		{"ipv6 loopback", "https://[::1]:8080", Options{}, false},
		{"ipv6 ula", "https://[fd00::1]:8080", Options{}, false},
		{"metadata hostname", "https://metadata.google.internal", Options{}, false},
		{"metadata hostname blocked even in dev", "https://metadata.google.internal", Options{AllowPrivate: true}, false},
		{"bad scheme", "ftp://8.8.8.8", Options{}, false},
		{"bad port", "https://8.8.8.8:99999", Options{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateUpstreamURL(tc.raw, tc.opts)
			if tc.ok && err != nil {
				t.Fatalf("expected ok, got error: %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

func TestValidateUpstreamURLRejectsUnresolvableHostname(t *testing.T) {
	// 严格模式下不存在的域名：解析失败 → 拒绝（不依赖外网可达性，NXDOMAIN 是确定性错误）
	if err := ValidateUpstreamURL("https://this-host-does-not-exist-4f9a2b.invalid", Options{}); err == nil {
		t.Fatal("unresolvable hostname must be rejected")
	}
}

func TestValidateRedirect(t *testing.T) {
	if err := ValidateRedirect("http://169.254.169.254/x", Options{AllowHTTP: true}); err == nil {
		t.Fatal("metadata redirect must be blocked")
	}
	if err := ValidateRedirect("https://8.8.8.8/v2", Options{}); err != nil {
		t.Fatalf("public redirect should pass: %v", err)
	}
}

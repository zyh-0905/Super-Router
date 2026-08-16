package config

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

const validYAML = `server:
  port: 8080
  read_timeout: 30s
  write_timeout: 0
  bootstrap_default_keys: false
database:
  postgres:
    host: postgres
    port: 5432
    user: gateway
    password: gateway_pass
    dbname: smart_router
  redis:
    host: redis
    port: 6379
    password: ""
    db: 0
checker:
  alive_interval: 30s
  pricing_interval: 10m
  probe_interval: 1h
  balance_interval: 10m
  daily_probe_budget: 5.00
  probe_failed_backoff: 6h
  probe_model: "gpt-4o"
  retention_days: 30
routing:
  default_strategy: custom_priority
  max_attempts: 3
  total_budget_ms: 15000
  filter:
    max_price_cap: 100.0
    max_ttft_ms: 5000
  circuit_breaker:
    min_samples: 20
    open_failure_rate: 0.50
    open_min_failures: 5
    initial_cooling_seconds: 30
    max_cooling_seconds: 600
    cooling_backoff: [30, 60, 120, 300, 600]
    half_open_probe_count: 1
    recovery_success_threshold: 3
  balanced_weights:
    cost: 0.35
    reliability: 0.30
    latency: 0.25
    load: 0.10
logging:
  level: info
  format: json
security:
  encryption_key: ""
`

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return p
}

// replaceLine 替换 yaml 中第一个匹配行的内容（用于构造非法配置）。
func mutate(t *testing.T, content, old, new string) string {
	t.Helper()
	return writeConfig(t, stringReplace(content, old, new))
}

func stringReplace(s, old, new string) string {
	idx := indexOf(s, old)
	if idx < 0 {
		return s
	}
	return s[:idx] + new + s[idx+len(old):]
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestLoadValidAndDefaults(t *testing.T) {
	cfg, err := Load(writeConfig(t, validYAML))
	if err != nil {
		t.Fatalf("valid config must load: %v", err)
	}
	if cfg.Server.ReadHeaderTimeout.Seconds() != 10 {
		t.Fatalf("read_header_timeout default = %v", cfg.Server.ReadHeaderTimeout)
	}
	if cfg.Server.MaxHeaderBytes != 1<<20 {
		t.Fatalf("max_header_bytes default = %d", cfg.Server.MaxHeaderBytes)
	}
	if cfg.Checker.RetentionDays != 30 {
		t.Fatalf("retention_days default = %d", cfg.Checker.RetentionDays)
	}
}

func TestValidateRejectsEmptyCoolingBackoff(t *testing.T) {
	_, err := Load(mutate(t, validYAML, "cooling_backoff: [30, 60, 120, 300, 600]", "cooling_backoff: []"))
	if err == nil {
		t.Fatal("empty cooling_backoff must be rejected (runtime panic risk)")
	}
}

func TestValidateRejectsBadStrategy(t *testing.T) {
	_, err := Load(mutate(t, validYAML, "default_strategy: custom_priority", "default_strategy: random_walk"))
	if err == nil {
		t.Fatal("unknown strategy must be rejected")
	}
}

func TestValidateRejectsBadPort(t *testing.T) {
	_, err := Load(mutate(t, validYAML, "port: 8080", "port: 70000"))
	if err == nil {
		t.Fatal("port > 65535 must be rejected")
	}
}

func TestValidateRejectsBadFailureRate(t *testing.T) {
	_, err := Load(mutate(t, validYAML, "open_failure_rate: 0.50", "open_failure_rate: 1.5"))
	if err == nil {
		t.Fatal("failure rate > 1 must be rejected")
	}
}

func TestValidateRejectsBadEncryptionKey(t *testing.T) {
	_, err := Load(mutate(t, validYAML, "encryption_key: \"\"", "encryption_key: \"bm90LWJhc2U2NA==\""))
	if err == nil {
		t.Fatal("invalid base64 encryption key must be rejected")
	}
}

func TestValidateAcceptsEncryptionKey(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	_, err := Load(mutate(t, validYAML, "encryption_key: \"\"", "encryption_key: \""+key+"\""))
	if err != nil {
		t.Fatalf("valid encryption key must load: %v", err)
	}
}

func TestUsesInsecureDefaults(t *testing.T) {
	cfg, err := Load(writeConfig(t, validYAML))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.UsesInsecureDefaults() {
		t.Fatal("gateway_pass + empty redis password must flag insecure defaults")
	}
	// 覆盖密码后不再告警
	cfg.Database.Postgres.Password = "strong-pass"
	cfg.Database.Redis.Password = "redis-pass"
	if cfg.UsesInsecureDefaults() {
		t.Fatal("strong credentials must not flag insecure defaults")
	}
}

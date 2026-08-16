package config

import (
	"fmt"
	"time"

	"smart-router/internal/crypto"

	"github.com/spf13/viper"
)

type Config struct {
	Server   ServerConfig   `mapstructure:"server"`
	Database DatabaseConfig `mapstructure:"database"`
	Checker  CheckerConfig  `mapstructure:"checker"`
	Routing  RoutingConfig  `mapstructure:"routing"`
	Logging  LoggingConfig  `mapstructure:"logging"`
	Security SecurityConfig `mapstructure:"security"`
}

type ServerConfig struct {
	Port                 int           `mapstructure:"port"`
	ReadTimeout          time.Duration `mapstructure:"read_timeout"`
	WriteTimeout         time.Duration `mapstructure:"write_timeout"` // 0 = 不限（SSE 长流式）
	ReadHeaderTimeout    time.Duration `mapstructure:"read_header_timeout"`
	IdleTimeout          time.Duration `mapstructure:"idle_timeout"`
	MaxHeaderBytes       int           `mapstructure:"max_header_bytes"`
	BootstrapDefaultKeys bool          `mapstructure:"bootstrap_default_keys"` // 空库时是否写入开发用默认 Key（仅限本地开发）
	AllowedOrigins       []string      `mapstructure:"allowed_origins"`        // CORS 白名单；空 = 允许任意来源（本地开发）
	MetricsToken         string        `mapstructure:"metrics_token"`          // 非空时 /metrics 需要 Bearer 认证
	AllowPrivateUpstream bool          `mapstructure:"allow_private_upstream"` // SSRF 防护豁免：允许私网/环回上游（仅本地开发）
	AllowHTTPUpstream    bool          `mapstructure:"allow_http_upstream"`    // SSRF 防护豁免：允许 http://（仅本地开发）
}

type DatabaseConfig struct {
	Postgres PostgresConfig `mapstructure:"postgres"`
	Redis    RedisConfig    `mapstructure:"redis"`
}

type PostgresConfig struct {
	Host     string `mapstructure:"host"`
	Port     int    `mapstructure:"port"`
	User     string `mapstructure:"user"`
	Password string `mapstructure:"password"`
	DBName   string `mapstructure:"dbname"`
}

type RedisConfig struct {
	Host     string `mapstructure:"host"`
	Port     int    `mapstructure:"port"`
	Password string `mapstructure:"password"`
	DB       int    `mapstructure:"db"`
}

type CheckerConfig struct {
	AliveInterval      time.Duration `mapstructure:"alive_interval"`
	PricingInterval    time.Duration `mapstructure:"pricing_interval"`
	ProbeInterval      time.Duration `mapstructure:"probe_interval"`
	BalanceInterval    time.Duration `mapstructure:"balance_interval"`
	DailyProbeBudget   float64       `mapstructure:"daily_probe_budget"`
	ProbeFailedBackoff time.Duration `mapstructure:"probe_failed_backoff"`
	ProbeModel         string        `mapstructure:"probe_model"`
	RetentionDays      int           `mapstructure:"retention_days"` // 历史数据保留天数（0 = 默认 30）
}

type RoutingConfig struct {
	DefaultStrategy          string                `mapstructure:"default_strategy"`
	MaxAttempts              int                   `mapstructure:"max_attempts"`
	TotalBudgetMS            int                   `mapstructure:"total_budget_ms"`
	RetryBeforeFirstByteOnly bool                  `mapstructure:"retry_before_first_byte_only"`
	AllowEmergencyPool       bool                  `mapstructure:"allow_emergency_pool"`
	TieBreaker               string                `mapstructure:"tie_breaker"`
	Filter                   FilterConfig          `mapstructure:"filter"`
	CircuitBreaker           CircuitBreakerConfig  `mapstructure:"circuit_breaker"`
	BalancedWeights          BalancedWeightsConfig `mapstructure:"balanced_weights"`
}

type FilterConfig struct {
	MaxPriceCap float64 `mapstructure:"max_price_cap"`
	MaxTTFTMS   int     `mapstructure:"max_ttft_ms"`
}

type CircuitBreakerConfig struct {
	MinSamples               int     `mapstructure:"min_samples"`
	OpenFailureRate          float64 `mapstructure:"open_failure_rate"`
	OpenMinFailures          int     `mapstructure:"open_min_failures"`
	AuthFailureThreshold     int     `mapstructure:"auth_failure_threshold"`
	InitialCoolingSeconds    int     `mapstructure:"initial_cooling_seconds"`
	MaxCoolingSeconds        int     `mapstructure:"max_cooling_seconds"`
	CoolingBackoff           []int   `mapstructure:"cooling_backoff"`
	HalfOpenProbeCount       int     `mapstructure:"half_open_probe_count"`
	RecoverySuccessThreshold int     `mapstructure:"recovery_success_threshold"`
}

type BalancedWeightsConfig struct {
	Cost        float64 `mapstructure:"cost"`
	Reliability float64 `mapstructure:"reliability"`
	Latency     float64 `mapstructure:"latency"`
	Load        float64 `mapstructure:"load"`
}

type LoggingConfig struct {
	Level  string `mapstructure:"level"`
	Format string `mapstructure:"format"`
}

type SecurityConfig struct {
	// EncryptionKey base64 编码的 32 字节 AES-256 密钥，用于上游凭据信封加密。
	// 空 = 明文透传（本地开发）。生产建议通过环境变量 SR_ENC_KEY 注入。
	EncryptionKey string `mapstructure:"encryption_key"`
}

var validStrategies = map[string]bool{
	"custom_priority": true, "price_first": true, "latency_first": true,
	"reliability_first": true, "balanced": true,
}

func Load(configPath string) (*Config, error) {
	viper.SetConfigFile(configPath)
	viper.SetConfigType("yaml")

	// 环境变量覆盖
	viper.AutomaticEnv()
	viper.SetEnvPrefix("ROUTER")

	// 允许通过环境变量覆盖数据库配置
	viper.BindEnv("database.postgres.host", "DATABASE_HOST")
	viper.BindEnv("database.postgres.port", "DATABASE_PORT")
	viper.BindEnv("database.postgres.user", "DATABASE_USER")
	viper.BindEnv("database.postgres.password", "DATABASE_PASSWORD")
	viper.BindEnv("database.postgres.dbname", "DATABASE_NAME")
	viper.BindEnv("database.redis.host", "REDIS_HOST")
	viper.BindEnv("database.redis.port", "REDIS_PORT")
	viper.BindEnv("security.encryption_key", "SR_ENC_KEY")

	if err := viper.ReadInConfig(); err != nil {
		return nil, err
	}

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, err
	}

	applyDefaults(&cfg)

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("配置校验失败: %w", err)
	}

	return &cfg, nil
}

// applyDefaults 为新引入的可选字段填充安全默认值（配置未提供时）。
func applyDefaults(cfg *Config) {
	if cfg.Server.ReadHeaderTimeout <= 0 {
		cfg.Server.ReadHeaderTimeout = 10 * time.Second
	}
	if cfg.Server.IdleTimeout <= 0 {
		cfg.Server.IdleTimeout = 120 * time.Second
	}
	if cfg.Server.MaxHeaderBytes <= 0 {
		cfg.Server.MaxHeaderBytes = 1 << 20 // 1 MiB
	}
	if cfg.Checker.RetentionDays == 0 {
		cfg.Checker.RetentionDays = 30
	}
}

// Validate 启动时配置校验（fail fast），避免错误配置在运行期才暴露。
func (c *Config) Validate() error {
	if c.Server.Port < 1 || c.Server.Port > 65535 {
		return fmt.Errorf("server.port 必须在 1-65535 之间，当前 %d", c.Server.Port)
	}
	if c.Server.ReadTimeout <= 0 {
		return fmt.Errorf("server.read_timeout 必须大于 0")
	}
	if c.Server.ReadHeaderTimeout <= 0 {
		return fmt.Errorf("server.read_header_timeout 必须大于 0")
	}
	if c.Server.MaxHeaderBytes < 1024 || c.Server.MaxHeaderBytes > 1<<22 {
		return fmt.Errorf("server.max_header_bytes 必须在 1024-4194304 之间")
	}
	if c.Server.MetricsToken != "" && len(c.Server.MetricsToken) < 16 {
		return fmt.Errorf("server.metrics_token 长度不足 16 字符")
	}
	if c.Database.Postgres.Port < 1 || c.Database.Postgres.Port > 65535 {
		return fmt.Errorf("database.postgres.port 非法: %d", c.Database.Postgres.Port)
	}
	if c.Database.Redis.Port < 1 || c.Database.Redis.Port > 65535 {
		return fmt.Errorf("database.redis.port 非法: %d", c.Database.Redis.Port)
	}

	if c.Checker.AliveInterval <= 0 || c.Checker.PricingInterval <= 0 ||
		c.Checker.ProbeInterval <= 0 || c.Checker.BalanceInterval <= 0 {
		return fmt.Errorf("checker 各检测间隔必须大于 0")
	}
	if c.Checker.DailyProbeBudget < 0 {
		return fmt.Errorf("checker.daily_probe_budget 不能为负")
	}
	if c.Checker.RetentionDays < 0 {
		return fmt.Errorf("checker.retention_days 不能为负")
	}
	if c.Checker.ProbeModel == "" {
		return fmt.Errorf("checker.probe_model 不能为空")
	}

	if !validStrategies[c.Routing.DefaultStrategy] {
		return fmt.Errorf("routing.default_strategy 非法: %q（可选: custom_priority/price_first/latency_first/reliability_first/balanced）", c.Routing.DefaultStrategy)
	}
	if c.Routing.MaxAttempts < 1 || c.Routing.MaxAttempts > 10 {
		return fmt.Errorf("routing.max_attempts 必须在 1-10 之间")
	}
	if c.Routing.TotalBudgetMS < 1000 || c.Routing.TotalBudgetMS > 120000 {
		return fmt.Errorf("routing.total_budget_ms 必须在 1000-120000 之间")
	}
	if c.Routing.Filter.MaxPriceCap <= 0 {
		return fmt.Errorf("routing.filter.max_price_cap 必须大于 0")
	}
	if c.Routing.Filter.MaxTTFTMS <= 0 {
		return fmt.Errorf("routing.filter.max_ttft_ms 必须大于 0")
	}

	cb := c.Routing.CircuitBreaker
	if cb.MinSamples < 1 {
		return fmt.Errorf("circuit_breaker.min_samples 必须 >= 1")
	}
	if cb.OpenFailureRate <= 0 || cb.OpenFailureRate > 1 {
		return fmt.Errorf("circuit_breaker.open_failure_rate 必须在 (0,1] 之间")
	}
	if cb.OpenMinFailures < 1 {
		return fmt.Errorf("circuit_breaker.open_min_failures 必须 >= 1")
	}
	if cb.InitialCoolingSeconds <= 0 || cb.MaxCoolingSeconds <= 0 {
		return fmt.Errorf("circuit_breaker 冷却时长必须大于 0")
	}
	if len(cb.CoolingBackoff) == 0 {
		// 空退避序列会在 half_open 探测失败时引发运行期 panic（nextCoolingDuration 索引越界）
		return fmt.Errorf("circuit_breaker.cooling_backoff 不能为空")
	}
	for i, s := range cb.CoolingBackoff {
		if s <= 0 {
			return fmt.Errorf("circuit_breaker.cooling_backoff[%d] 必须大于 0", i)
		}
	}
	if cb.RecoverySuccessThreshold < 1 {
		return fmt.Errorf("circuit_breaker.recovery_success_threshold 必须 >= 1")
	}

	bw := c.Routing.BalancedWeights
	if bw.Cost < 0 || bw.Reliability < 0 || bw.Latency < 0 || bw.Load < 0 {
		return fmt.Errorf("balanced_weights 各权重不能为负")
	}
	if bw.Cost+bw.Reliability+bw.Latency+bw.Load <= 0 {
		return fmt.Errorf("balanced_weights 权重之和必须大于 0")
	}

	// 加密密钥：配置了就必须是合法的 32 字节密钥
	if c.Security.EncryptionKey != "" {
		if _, err := crypto.DecodeKey(c.Security.EncryptionKey); err != nil {
			return fmt.Errorf("security.encryption_key 非法: %w", err)
		}
	}

	return nil
}

// UsesInsecureDefaults 生产环境（非 bootstrap）使用默认数据库/Redis 凭据时为 true，
// 由启动逻辑输出安全告警（P2-01）。
func (c *Config) UsesInsecureDefaults() bool {
	return c.Database.Postgres.Password == "gateway_pass" || c.Database.Redis.Password == ""
}

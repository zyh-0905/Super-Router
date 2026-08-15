package config

import (
	"time"

	"github.com/spf13/viper"
)

type Config struct {
	Server   ServerConfig   `mapstructure:"server"`
	Database DatabaseConfig `mapstructure:"database"`
	Checker  CheckerConfig  `mapstructure:"checker"`
	Routing  RoutingConfig  `mapstructure:"routing"`
	Logging  LoggingConfig  `mapstructure:"logging"`
}

type ServerConfig struct {
	Port                 int           `mapstructure:"port"`
	ReadTimeout          time.Duration `mapstructure:"read_timeout"`
	WriteTimeout         time.Duration `mapstructure:"write_timeout"`
	BootstrapDefaultKeys bool          `mapstructure:"bootstrap_default_keys"` // 空库时是否写入开发用默认 Key（仅限本地开发）
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

	if err := viper.ReadInConfig(); err != nil {
		return nil, err
	}

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

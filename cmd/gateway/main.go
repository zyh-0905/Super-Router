package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"smart-router/internal/api"
	"smart-router/internal/checker"
	"smart-router/internal/config"
	"smart-router/internal/logger"
	"smart-router/internal/metrics"
	"smart-router/internal/migrate"
	"smart-router/internal/router"
	"smart-router/internal/store"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/zap"
)

// fileExists 检查文件是否存在且为常规文件
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func main() {
	// 解析命令行参数
	configPath := flag.String("config", "configs/config.yaml", "配置文件路径")
	webDir := flag.String("web-dir", "web", "Web 前端目录路径")
	flag.Parse()

	// 加载配置（含启动时 schema/范围校验，P2-13）
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// 初始化日志
	zapLogger, err := logger.NewLogger(cfg.Logging.Level)
	if err != nil {
		log.Fatalf("Failed to initialize logger: %v", err)
	}
	defer zapLogger.Sync()

	zapLogger.Info("Starting Smart Router Gateway",
		zap.String("version", "0.2.0"),
		zap.Int("port", cfg.Server.Port),
	)

	// 初始化数据库连接
	db, err := store.NewPostgres(cfg.Database.Postgres)
	if err != nil {
		zapLogger.Fatal("Failed to connect to database", zap.Error(err))
	}
	defer db.Close()

	// 初始化 Redis 连接
	redisClient, err := store.NewRedis(cfg.Database.Redis)
	if err != nil {
		zapLogger.Fatal("Failed to connect to redis", zap.Error(err))
	}
	defer redisClient.Close()

	zapLogger.Info("Database and Redis connected")

	// 启动时执行版本化迁移（P2-12：带 advisory lock，多实例并发安全；
	// 存量 initdb 数据卷按迁移效果基线登记，未应用的迁移自动补齐）
	mctx, mcancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer mcancel()
	if err := migrate.Up(mctx, db.Pool, zapLogger); err != nil {
		zapLogger.Fatal("Failed to run migrations", zap.Error(err))
	}

	// 生产环境安全告警（P2-01/P1-07）
	if !cfg.Server.BootstrapDefaultKeys {
		if cfg.UsesInsecureDefaults() {
			zapLogger.Warn("SECURITY: running with default database/redis credentials in production mode; use env overrides and set a redis password")
		}
		if cfg.Security.EncryptionKey == "" {
			zapLogger.Warn("SECURITY: security.encryption_key is empty; upstream credentials stored in plaintext. Set SR_ENC_KEY (base64 32-byte key) and rotate existing credentials")
		}
	}

	// 初始化管理员 Key（生产模式空库时生成随机 Key，仅打印一次）
	generatedKey, err := api.EnsureDefaultKeys(db, cfg.Server.BootstrapDefaultKeys)
	if err != nil {
		zapLogger.Warn("Failed to ensure api keys", zap.Error(err))
	}
	if generatedKey != "" {
		zapLogger.Warn("Generated initial admin API key (shown once, store it securely)",
			zap.String("admin_key", generatedKey))
	}

	// 启动 Prometheus metrics 后台收集器
	go metrics.StartCollector(db, 30*time.Second)

	// 注入 metrics 记录器到 router 包（避免循环依赖）
	router.SetMetricsRecorders(
		metrics.RecordRoutingDuration,
		metrics.RecordSnapshotLoadDuration,
	)

	// 初始化路由器
	routerEngine := router.NewRouter(db, redisClient)

	// 注入系统级策略默认值（DB 无策略记录时的兜底，来自配置文件）
	routerEngine.SetPolicyDefaults(router.PolicyDefaults{
		DefaultStrategy:    cfg.Routing.DefaultStrategy,
		MaxAttempts:        cfg.Routing.MaxAttempts,
		TotalBudgetMS:      cfg.Routing.TotalBudgetMS,
		MaxPriceCap:        cfg.Routing.Filter.MaxPriceCap,
		MaxTTFTMS:          cfg.Routing.Filter.MaxTTFTMS,
		HalfOpenProbeCount: cfg.Routing.CircuitBreaker.HalfOpenProbeCount,
		BalancedWeights: map[string]float64{
			"cost":        cfg.Routing.BalancedWeights.Cost,
			"reliability": cfg.Routing.BalancedWeights.Reliability,
			"latency":     cfg.Routing.BalancedWeights.Latency,
			"load":        cfg.Routing.BalancedWeights.Load,
		},
	})

	// 初始化熔断管理器（分组级参数在请求时按 group 覆盖；状态按分组桶隔离，P1-04）
	circuitManager := api.NewCircuitBreakerManager(db, zapLogger.Named("circuit"), api.CircuitBreakerConfig{
		MinSamples:               cfg.Routing.CircuitBreaker.MinSamples,
		OpenFailureRate:          cfg.Routing.CircuitBreaker.OpenFailureRate,
		OpenMinFailures:          cfg.Routing.CircuitBreaker.OpenMinFailures,
		AuthFailureThreshold:     cfg.Routing.CircuitBreaker.AuthFailureThreshold,
		InitialCoolingSeconds:    cfg.Routing.CircuitBreaker.InitialCoolingSeconds,
		MaxCoolingSeconds:        cfg.Routing.CircuitBreaker.MaxCoolingSeconds,
		CoolingBackoff:           cfg.Routing.CircuitBreaker.CoolingBackoff,
		HalfOpenProbeCount:       cfg.Routing.CircuitBreaker.HalfOpenProbeCount,
		RecoverySuccessThreshold: cfg.Routing.CircuitBreaker.RecoverySuccessThreshold,
	})

	// 初始化处理器
	proxyHandler := api.NewProxyHandler(routerEngine, db, zapLogger.Named("proxy"), circuitManager, cfg.Security.EncryptionKey)
	adminHandler := api.NewAdminHandler(db, redisClient, cfg, zapLogger.Named("admin"))

	// 实时倍率：按需手动实测（复用 checker 探测逻辑，运行在 Gateway 内）
	ratioProbe := checker.NewProbeChecker(db, zapLogger.Named("ratio-probe"), cfg.Security.EncryptionKey, redisClient)
	ratioProbe.SetProbeModel(cfg.Checker.ProbeModel)
	ratioHandler := api.NewRatioHandler(db, redisClient, cfg, ratioProbe, zapLogger.Named("ratio"))

	// 初始化 HTTP 服务器
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	// 添加 trace_id 中间件
	r.Use(func(c *gin.Context) {
		traceID := uuid.New().String()
		c.Set("trace_id", traceID)
		c.Writer.Header().Set("X-Trace-ID", traceID)

		// 创建带有 trace_id 的日志器
		requestLogger := logger.WithTraceID(zapLogger, traceID)
		c.Set("logger", requestLogger)

		c.Next()
	})

	// 添加 Prometheus metrics 中间件（P2-08：业务指标与管理指标分离）
	r.Use(metrics.PrometheusMiddleware())

	// 添加 CORS 中间件（P2-03：可信 Origin 白名单 + 分组头补全 + Vary）
	r.Use(corsMiddleware(cfg))

	// 健康检查端点（liveness，无需认证）
	r.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"status":    "ok",
			"timestamp": time.Now().Unix(),
		})
	})

	// 就绪检查端点（readiness，P2-09：检查 PostgreSQL/Redis 可用性）
	r.GET("/ready", readinessHandler(db, redisClient, zapLogger))

	// Prometheus metrics 端点（P2-09：配置 metrics_token 时要求 Bearer 认证）
	metricsGroup := r.Group("/metrics")
	if cfg.Server.MetricsToken != "" {
		metricsGroup.Use(metricsAuthMiddleware(cfg.Server.MetricsToken))
	}
	metricsGroup.GET("", gin.WrapH(promhttp.Handler()))

	// API 路由（需要认证）
	apiGroup := r.Group("/v1")
	apiGroup.Use(api.AuthMiddleware(db))
	{
		apiGroup.POST("/chat/completions", proxyHandler.HandleChatCompletion)
	}

	// 管理接口（需要 admin 角色）
	adminGroup := r.Group("/admin")
	adminGroup.Use(api.AuthMiddleware(db))
	adminGroup.Use(api.RequireRole("admin"))
	{
		adminGroup.POST("/channels", adminHandler.CreateChannel)
		adminGroup.GET("/channels", adminHandler.ListChannels)
		adminGroup.GET("/channels/:id", adminHandler.GetChannel)
		adminGroup.PATCH("/channels/:id", adminHandler.UpdateChannel)
		adminGroup.DELETE("/channels/:id", adminHandler.DeleteChannel)
		adminGroup.GET("/channels/:id/models", adminHandler.GetUpstreamModels)
		adminGroup.POST("/upstream/models", adminHandler.ProbeUpstreamModels)
		adminGroup.GET("/channels/:id/ratio", ratioHandler.GetRatio)
		adminGroup.GET("/channel-metrics", ratioHandler.GetChannelMetrics)
		adminGroup.GET("/model-prices", ratioHandler.ListModelPrices)
		adminGroup.POST("/model-prices", ratioHandler.UpsertModelPrice)
		adminGroup.DELETE("/model-prices/:model", ratioHandler.DeleteModelPrice)
		adminGroup.POST("/channels/:id/probe-ratio", ratioHandler.ProbeRatio)
		adminGroup.POST("/channels/:id/ratio-groups", ratioHandler.CreateRatioGroup)
		adminGroup.PATCH("/channels/:id/ratio-groups/:gid", ratioHandler.UpdateRatioGroup)
		adminGroup.DELETE("/channels/:id/ratio-groups/:gid", ratioHandler.DeleteRatioGroup)
		adminGroup.POST("/channels/:id/ratio-groups/:gid/probe", ratioHandler.ProbeRatioGroup)
		adminGroup.GET("/health/:channel_id", adminHandler.GetHealth)
		adminGroup.GET("/channels/:id/balance", adminHandler.GetChannelBalance)
		adminGroup.GET("/settings", adminHandler.GetSettings)
		adminGroup.PATCH("/settings", adminHandler.UpdateSettings)
		adminGroup.GET("/decisions", adminHandler.GetDecisions)
		adminGroup.DELETE("/decisions", adminHandler.DeleteDecisions)
		adminGroup.GET("/circuit", adminHandler.GetCircuitStates)
		adminGroup.POST("/circuit/:channel_id/reset", adminHandler.ResetCircuit)
		adminGroup.GET("/stats", adminHandler.GetStats)
		adminGroup.GET("/alerts", adminHandler.GetAlerts)
		adminGroup.GET("/keys", adminHandler.ListKeys)
		adminGroup.POST("/keys", adminHandler.CreateKey)
		adminGroup.PATCH("/keys/:id", adminHandler.UpdateKey)
		adminGroup.DELETE("/keys/:id", adminHandler.DeleteKey)
		adminGroup.GET("/config", adminHandler.GetConfig)
		adminGroup.GET("/groups", adminHandler.ListGroups)
		adminGroup.POST("/groups", adminHandler.CreateGroup)
		adminGroup.PATCH("/groups/:id", adminHandler.UpdateGroup)
		adminGroup.DELETE("/groups/:id", adminHandler.DeleteGroup)
		adminGroup.GET("/policies", adminHandler.GetSystemPolicy)
		adminGroup.PUT("/policies", adminHandler.UpdateSystemPolicy)
		adminGroup.GET("/groups/:id/strategy", adminHandler.GetGroupStrategy)
		adminGroup.PUT("/groups/:id/strategy", adminHandler.UpdateGroupStrategy)
	}

	// 静态托管 Web 管理界面（同一端口，免 CORS）
	// 优先使用 Vite 构建产物 web/dist；未构建时回退到旧的单文件版本
	distIndex := filepath.Join(*webDir, "dist", "index.html")
	legacyIndex := filepath.Join(*webDir, "index.html")
	switch {
	case fileExists(distIndex):
		r.StaticFile("/", distIndex)
		r.StaticFile("/index.html", distIndex)
		r.Static("/assets", filepath.Join(*webDir, "dist", "assets"))
		zapLogger.Info("Web dashboard served", zap.String("root", filepath.Join(*webDir, "dist")))
	case fileExists(legacyIndex):
		r.StaticFile("/", legacyIndex)
		r.StaticFile("/index.html", legacyIndex)
		r.Static("/vendor", filepath.Join(*webDir, "vendor"))
		zapLogger.Info("Web dashboard served (legacy single-file)", zap.String("root", *webDir))
	default:
		zapLogger.Warn("Web dashboard directory not found, serving API only", zap.String("web_dir", *webDir))
	}

	// 启动服务器（P2-06：补充 header/idle/max-header 边界；WriteTimeout 保持 0，
	// SSE 长流式的写超时由每尝试 TTFB 计时器 + 空闲连接策略控制）
	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	zapLogger.Info("HTTP server starting", zap.String("addr", addr))

	srv := &http.Server{
		Addr:              addr,
		Handler:           r,
		ReadTimeout:       cfg.Server.ReadTimeout,
		WriteTimeout:      cfg.Server.WriteTimeout,
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout,
		IdleTimeout:       cfg.Server.IdleTimeout,
		MaxHeaderBytes:    cfg.Server.MaxHeaderBytes,
	}

	// 优雅关闭
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			zapLogger.Fatal("Failed to start server", zap.Error(err))
		}
	}()

	// 等待中断信号
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	zapLogger.Info("Shutting down server...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		zapLogger.Fatal("Server forced to shutdown", zap.Error(err))
	}

	zapLogger.Info("Server exited")
}

// corsMiddleware CORS 处理（P2-03）：
//   - allowed_origins 为空（默认）时允许任意来源，但设置 Vary: Origin；
//   - 配置白名单时仅回显受信来源，未受信来源不设置 ACAO（浏览器阻断）；
//   - Allow-Headers / Expose-Headers 补全 X-Group、X-Request-ID。
func corsMiddleware(cfg *config.Config) gin.HandlerFunc {
	allowed := map[string]bool{}
	for _, o := range cfg.Server.AllowedOrigins {
		allowed[strings.TrimRight(o, "/")] = true
	}
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		c.Writer.Header().Set("Vary", "Origin")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Origin, Content-Type, Authorization, X-Group, X-Request-ID")
		c.Writer.Header().Set("Access-Control-Expose-Headers", "Content-Length, X-Trace-ID, X-Request-ID, X-Selected-Channel, X-Selected-Channel-Id, X-Strategy, X-Group")

		if len(allowed) > 0 {
			if origin != "" && allowed[origin] {
				c.Writer.Header().Set("Access-Control-Allow-Origin", origin)
				c.Writer.Header().Set("Access-Control-Allow-Credentials", "true")
			}
		} else {
			c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		}

		if c.Request.Method == "OPTIONS" {
			if len(allowed) > 0 && origin != "" && !allowed[origin] {
				c.AbortWithStatus(403)
				return
			}
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	}
}

// readinessHandler 就绪检查：PostgreSQL + Redis 均可用才返回 200（P2-09）。
func readinessHandler(db *store.DB, redis *store.RedisClient, logger *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()

		if err := db.Pool.Ping(ctx); err != nil {
			logger.Warn("Readiness failed: postgres", zap.Error(err))
			c.JSON(503, gin.H{"status": "not_ready", "dependency": "postgres"})
			return
		}
		if redis == nil || redis.Client == nil {
			c.JSON(503, gin.H{"status": "not_ready", "dependency": "redis"})
			return
		}
		if err := redis.Client.Ping(ctx).Err(); err != nil {
			logger.Warn("Readiness failed: redis", zap.Error(err))
			c.JSON(503, gin.H{"status": "not_ready", "dependency": "redis"})
			return
		}
		c.JSON(200, gin.H{"status": "ready"})
	}
}

// metricsAuthMiddleware /metrics Bearer 认证（配置 metrics_token 时启用）。
func metricsAuthMiddleware(token string) gin.HandlerFunc {
	return func(c *gin.Context) {
		auth := c.GetHeader("Authorization")
		if auth != "Bearer "+token {
			c.AbortWithStatusJSON(401, gin.H{"error": "unauthorized"})
			return
		}
		c.Next()
	}
}

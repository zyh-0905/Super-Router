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
	"syscall"
	"time"

	"smart-router/internal/api"
	"smart-router/internal/checker"
	"smart-router/internal/config"
	"smart-router/internal/logger"
	"smart-router/internal/metrics"
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

	// 加载配置
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
		zap.String("version", "0.1.0"),
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

	// 初始化熔断管理器（分组级参数在请求时按 group 覆盖）
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
	proxyHandler := api.NewProxyHandler(routerEngine, db, zapLogger.Named("proxy"), circuitManager)
	adminHandler := api.NewAdminHandler(db, cfg, zapLogger.Named("admin"))

	// 实时倍率：按需手动实测（复用 checker 探测逻辑，运行在 Gateway 内）
	ratioProbe := checker.NewProbeChecker(db, zapLogger.Named("ratio-probe"))
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

	// 添加 Prometheus metrics 中间件
	r.Use(metrics.PrometheusMiddleware())

	// 添加 CORS 中间件
	r.Use(func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Origin, Content-Type, Authorization")
		c.Writer.Header().Set("Access-Control-Expose-Headers", "Content-Length, X-Trace-ID, X-Request-ID, X-Selected-Channel, X-Selected-Channel-Id, X-Strategy")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	})

	// 健康检查端点（无需认证）
	r.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{
			"status":    "ok",
			"timestamp": time.Now().Unix(),
		})
	})

	// Prometheus metrics 端点（无需认证）
	r.GET("/metrics", gin.WrapH(promhttp.Handler()))

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
		adminGroup.GET("/circuit", adminHandler.GetCircuitStates)
		adminGroup.POST("/circuit/:channel_id/reset", adminHandler.ResetCircuit)
		adminGroup.GET("/stats", adminHandler.GetStats)
		adminGroup.GET("/keys", adminHandler.ListKeys)
		adminGroup.POST("/keys", adminHandler.CreateKey)
		adminGroup.PATCH("/keys/:id", adminHandler.UpdateKey)
		adminGroup.DELETE("/keys/:id", adminHandler.DeleteKey)
		adminGroup.GET("/config", adminHandler.GetConfig)
		adminGroup.GET("/groups", adminHandler.ListGroups)
		adminGroup.POST("/groups", adminHandler.CreateGroup)
		adminGroup.PATCH("/groups/:id", adminHandler.UpdateGroup)
		adminGroup.DELETE("/groups/:id", adminHandler.DeleteGroup)
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

	// 启动服务器
	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	zapLogger.Info("HTTP server starting", zap.String("addr", addr))

	srv := &http.Server{
		Addr:         addr,
		Handler:      r,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
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

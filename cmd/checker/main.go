package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"smart-router/internal/alert"
	"smart-router/internal/checker"
	"smart-router/internal/config"
	"smart-router/internal/crypto"
	"smart-router/internal/migrate"
	"smart-router/internal/quality"
	"smart-router/internal/safenet"
	"smart-router/internal/store"
	"smart-router/internal/telegram"

	"go.uber.org/zap"
)

func main() {
	// 解析命令行参数
	configPath := flag.String("config", "configs/config.yaml", "配置文件路径")
	flag.Parse()

	// 加载配置
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// 初始化日志
	var logger *zap.Logger
	if cfg.Logging.Level == "debug" {
		logger, _ = zap.NewDevelopment()
	} else {
		logger, _ = zap.NewProduction()
	}
	defer logger.Sync()

	logger.Info("Starting Health Checker (group-aware scheduler)",
		zap.Duration("alive_interval", cfg.Checker.AliveInterval),
		zap.Duration("pricing_interval", cfg.Checker.PricingInterval),
		zap.Duration("probe_interval", cfg.Checker.ProbeInterval),
	)

	// 初始化数据库连接
	db, err := store.NewPostgres(cfg.Database.Postgres)
	if err != nil {
		logger.Fatal("Failed to connect to database", zap.Error(err))
	}
	defer db.Close()

	logger.Info("Database connected")

	// 启动时执行版本化迁移（P2-12：带 advisory lock，多实例并发安全）
	mctx, mcancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer mcancel()
	if err := migrate.Up(mctx, db.Pool, logger); err != nil {
		logger.Fatal("Failed to run migrations", zap.Error(err))
	}

	// 初始化 Redis（探针预算原子记账用；不可用时回退数据库汇总检查）
	redisClient, err := store.NewRedis(cfg.Database.Redis)
	if err != nil {
		logger.Warn("Redis unavailable, probe budget falls back to DB summary checks", zap.Error(err))
		redisClient = &store.RedisClient{}
	} else {
		defer redisClient.Close()
	}

	// 生产环境安全告警（P2-01）
	if !cfg.Server.BootstrapDefaultKeys && cfg.UsesInsecureDefaults() {
		logger.Warn("SECURITY: running with default database/redis credentials in production mode; use env overrides and set redis password",
			zap.String("db_password_default", boolToStr(cfg.Database.Postgres.Password == "gateway_pass")),
			zap.String("redis_password_empty", boolToStr(cfg.Database.Redis.Password == "")))
	}
	if !cfg.Server.BootstrapDefaultKeys && cfg.Security.EncryptionKey == "" {
		logger.Warn("SECURITY: security.encryption_key is empty; upstream credentials stored in plaintext. Set SR_ENC_KEY (base64 32-byte key)")
	}

	// 初始化 checker
	aliveChecker := checker.NewAliveChecker(db, logger.Named("alive"), cfg.Security.EncryptionKey)
	pricingChecker := checker.NewPricingChecker(db, logger.Named("pricing"), cfg.Security.EncryptionKey)
	probeChecker := checker.NewProbeChecker(db, logger.Named("probe"), cfg.Security.EncryptionKey, redisClient)
	balanceChecker := checker.NewBalanceChecker(db, logger.Named("balance"), cfg.Security.EncryptionKey, redisClient)
	probeChecker.SetProbeModel(cfg.Checker.ProbeModel)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sched := newScheduler(db, redisClient, logger, cfg, aliveChecker, pricingChecker, probeChecker, balanceChecker)
	go sched.run(ctx)

	// 告警 Reconciler：评估并持久化告警生命周期（启动立即一次 + 每 30s 一轮）。
	// 使用固定 30s 间隔独立于调度 tick，且自身带 advisory lock 保证多实例只有一方写库。
	alertReconciler := alert.NewReconciler(alert.NewEvaluator(db), alert.NewSQLStore(db), logger.Named("alert"))
	go alertReconciler.Run(ctx, 30*time.Second)

	// Telegram Worker：默认关闭（telegram_config.enabled=false 时 idle）。
	// 配置启用后长轮询命令 + 每小时整点汇总；advisory lock 保证多实例只有一个 poller/report owner。
	// Telegram 失败只记日志，不影响 alive/pricing/probe/balance。
	tgStore := telegram.NewSQLConfigStore(db)
	if tgCfg, terr := tgStore.LoadConfig(ctx); terr != nil {
		logger.Warn("Telegram config load failed, worker disabled", zap.Error(terr))
	} else if tgCfg.Enabled {
		token, terr := tgStore.BotToken(ctx, crypto.Decrypt, cfg.Security.EncryptionKey)
		if terr != nil || token == "" {
			logger.Warn("Telegram enabled but bot token unavailable, worker disabled", zap.Error(terr))
		} else {
			telegram.RegisterAlertQueries(db)
			cmds := telegram.NewCommandService(telegram.NewSQLQueryService(db))
			// /sitetest 站点直达测试：复用 ProbeChecker（余额读取 + 站点探测锁），
			// 与定时/手动探针同源；SSRF 口径与质量检测一致。结果经 Worker 异步推送。
			cmds.SetSiteTestRunner(telegram.NewSiteTestRunner(db, probeChecker, cfg.Security.EncryptionKey,
				logger.Named("site-test"),
				safenet.Options{AllowHTTP: cfg.Server.AllowHTTPUpstream, AllowPrivate: cfg.Server.AllowPrivateUpstream}))
			cmds.SetLogger(logger.Named("telegram"))
			tgWorker := telegram.NewWorker(tgStore, telegram.NewClient(token), cmds, logger.Named("telegram"))
			cmds.SetAsyncSender(tgWorker) // 异步推送结果（/sitetest 长任务）
			tgWorker.SetReportBuilder(telegram.NewReportBuilder(db))
			tgWorker.SetLock(pgAdvisoryLock(db))
			go tgWorker.Run(ctx)
			logger.Info("Telegram worker started (long polling + hourly reports)")
		}
	}

	// Quality Worker：领取并执行 API 接口质量检测任务（默认 3 并发）。
	// 与 Telegram/告警 Worker 相互独立，任一失败不影响其他。
	qualityRepo := quality.NewPostgresRepository(db)
	qualityExecutor := quality.NewExecutor(db, qualityRepo,
		quality.NewRedisPublisher(redisClient), cfg.Security.EncryptionKey, cfg.Checker.ProbeModel, logger.Named("quality"))
	qualityExecutor.SetAlertSink(alert.NewQualityAlertSink(alert.NewSQLStore(db)))
	// H3：质量检测出站 HTTP 与网关写入路径同口径的 SSRF 校验
	// （开发配置可放宽私网/http，生产默认仅 https 公网上游）。
	qualityExecutor.SetSafenetOptions(safenet.Options{
		AllowHTTP:    cfg.Server.AllowHTTPUpstream,
		AllowPrivate: cfg.Server.AllowPrivateUpstream,
	})
	go quality.NewWorker(qualityRepo, qualityExecutor, checkerWorkerID(), logger.Named("quality-worker")).Run(ctx)
	logger.Info("Quality worker started (max 3 concurrent)")

	logger.Info("Scheduler started (tick 5s), alert reconciler started (30s)")

	// 存活检查端点（D2）：checker 无业务端口，暴露最小 HTTP 端点供
	// Compose healthcheck 探活；进程崩溃即不可达，Compose 可据此重启。
	// 仅监听容器内 8081，不映射宿主机端口。
	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		})
		srv := &http.Server{
			Addr:              ":8081",
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
		}
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("checker health server failed", zap.Error(err))
		}
	}()

	// 等待中断信号
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("Shutting down checker...")
	cancel()
	time.Sleep(time.Second) // 给 goroutine 时间清理
	logger.Info("Checker exited")
}

func boolToStr(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// checkerWorkerID 质量 Worker 实例标识（进程级唯一；多实例时用于心跳所有权校验）。
func checkerWorkerID() string {
	host, _ := os.Hostname()
	return fmt.Sprintf("%s-%d", host, os.Getpid())
}

// pgAdvisoryLock 返回基于 PostgreSQL 会话级 advisory lock 的选主函数
// （连接断开时锁自动释放，其他 Checker 可接管）。
func pgAdvisoryLock(db *store.DB) telegram.LockFn {
	return func(ctx context.Context, key int64) (func(), error) {
		conn, err := db.Pool.Acquire(ctx)
		if err != nil {
			return nil, err
		}
		if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, key); err != nil {
			conn.Release()
			return nil, err
		}
		return func() {
			unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, _ = conn.Exec(unlockCtx, `SELECT pg_advisory_unlock($1)`, key)
			conn.Release()
		}, nil
	}
}

// ============================================================
// 分组感知调度器：每个渠道按其所属分组的有效间隔独立调度
// ============================================================

type scheduler struct {
	db     *store.DB
	logger *zap.Logger
	cfg    *config.Config

	alive   *checker.AliveChecker
	pricing *checker.PricingChecker
	probe   *checker.ProbeChecker
	balance *checker.BalanceChecker

	// budget 探针预算原子记账（Redis；不可用时为 nil 走 DB 汇总回退，P1-06）
	budget *checker.BudgetTracker

	// 每个渠道每个任务的上次执行时间
	lastRun map[int]map[string]time.Time
	// pendingEpoch 本轮的工作 epoch（H11：先写结果、轮末发布）
	pendingEpoch int64

	// A1：本 tick 全局探针预算的进程内粗闸门（微美元，近似值）——
	// Redis 原子预留才是硬闸门；内存值只用于跳过明知无预算的站点。
	// 并发探测时用原子计数维护（原串行版本共享 float64 指针）。
	probeBudgetMicro atomic.Int64
	// H8：预算结算失败 → 禁用本 tick 剩余探测（并发安全版 globalBudgetLeft=0）。
	probeDisabled atomic.Bool

	lastCleanup time.Time // 保留策略清理（每日一次）
}

func probeDue(now time.Time, lastRun map[string]time.Time, interval, failedBackoff time.Duration) bool {
	lastProbe := lastRun["probe"]
	if lastProbe.IsZero() {
		return true
	}

	wait := interval
	if failedAt := lastRun["probe_failed"]; failedBackoff > 0 && failedAt.Equal(lastProbe) {
		wait = failedBackoff
	}
	return now.Sub(lastProbe) >= wait
}

func remainingProbeBudget(current, cost float64) float64 {
	if cost <= 0 {
		return current
	}
	return current - cost
}

func probeBudgetLeft(budget, spent float64, err error) (float64, bool) {
	if err != nil {
		return 0, false
	}
	return budget - spent, true
}

func accountProbeResult(current, cost float64, err error) (float64, bool) {
	return remainingProbeBudget(current, cost), err != nil
}

func newScheduler(db *store.DB, redis *store.RedisClient, logger *zap.Logger, cfg *config.Config,
	alive *checker.AliveChecker, pricing *checker.PricingChecker,
	probe *checker.ProbeChecker, balance *checker.BalanceChecker) *scheduler {
	return &scheduler{
		db:      db,
		logger:  logger,
		cfg:     cfg,
		alive:   alive,
		pricing: pricing,
		probe:   probe,
		balance: balance,
		budget:  checker.NewBudgetTracker(redis),
		lastRun: map[int]map[string]time.Time{},
	}
}

func (s *scheduler) run(ctx context.Context) {
	// 启动时立即对所有渠道做一次存活探测（快速获得初始数据）
	s.runAliveForAll(ctx)
	s.runCleanup(ctx) // 启动时清理一次过期数据

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tick(ctx)
		}
	}
}

// runCleanup 按保留策略清理过期历史数据（每日一次；表名/列名均为常量）。
// 决策日志清理后同步删除无引用的快照归档行（P1-05 孤儿归档回收）。
func (s *scheduler) runCleanup(ctx context.Context) {
	if time.Since(s.lastCleanup) < 24*time.Hour {
		return
	}
	days := s.cfg.Checker.RetentionDays
	if days <= 0 {
		days = 30
	}
	targets := []struct{ table, col string }{
		{"health_checks", "checked_at"},
		{"declared_prices", "checked_at"},
		{"probe_results", "checked_at"},
		{"balance_checks", "checked_at"},
		{"request_history", "created_at"},
		{"decision_logs", "decided_at"},
		{"quality_check_runs", "created_at"}, // 质量检测结果经外键级联删除
	}
	for _, t := range targets {
		ct, err := s.db.Pool.Exec(ctx,
			fmt.Sprintf("DELETE FROM %s WHERE %s < NOW() - make_interval(days => $1)", t.table, t.col), days)
		if err != nil {
			s.logger.Warn("Retention cleanup failed", zap.String("table", t.table), zap.Error(err))
			continue
		}
		s.logger.Debug("Retention cleanup done", zap.String("table", t.table), zap.Int64("deleted", ct.RowsAffected()))
	}

	// 快照归档孤儿清理：仅保留仍被决策日志引用的归档
	if _, err := s.db.Pool.Exec(ctx, `
		DELETE FROM snapshot_archive a
		WHERE NOT EXISTS (
			SELECT 1 FROM decision_logs d WHERE d.snapshot_checksum = a.checksum
		)`); err != nil {
		s.logger.Warn("Snapshot archive cleanup failed", zap.Error(err))
	}

	s.lastCleanup = time.Now()
}

func (s *scheduler) runAliveForAll(ctx context.Context) {
	schedules, err := checker.LoadSchedules(ctx, s.db,
		s.cfg.Checker.AliveInterval, s.cfg.Checker.PricingInterval,
		s.cfg.Checker.ProbeInterval, s.cfg.Checker.BalanceInterval, s.cfg.Checker.DailyProbeBudget,
		s.cfg.Security.EncryptionKey)
	if err != nil {
		s.logger.Error("Load schedules failed", zap.Error(err))
		return
	}
	if len(schedules) == 0 {
		s.logger.Warn("No enabled channels found")
		return
	}

	// H11：工作 epoch 先写后发布——探测结果以 pendingEpoch 写入，
	// 全部写完后才把 epoch 发布到 epoch_counter，快照读取端永远
	// 不会看到「半轮」的混合状态。
	pendingEpoch, err := s.db.PeekNextEpoch(ctx)
	if err != nil {
		s.logger.Error("Peek working epoch failed, initial checks skipped", zap.Error(err))
		return
	}
	now := time.Now()
	for _, sch := range schedules {
		s.lastRun[sch.ID] = map[string]time.Time{
			"alive":   now,
			"pricing": now,
			"probe":   now,
			"balance": {}, // 零值：第一个 tick 立即执行余额检测
		}
		if err := s.alive.CheckChannel(ctx, sch.Upstream, pendingEpoch); err != nil {
			s.logger.Debug("Initial alive check failed",
				zap.String("channel", sch.Name), zap.Error(err))
		}
	}
	if _, err := s.db.PublishEpoch(ctx, pendingEpoch); err != nil {
		s.logger.Error("Publish epoch failed (initial checks)", zap.Int64("epoch", pendingEpoch), zap.Error(err))
	}
	s.logger.Info("Initial alive check completed", zap.Int("channels", len(schedules)))
}

func (s *scheduler) tick(ctx context.Context) {
	schedules, err := checker.LoadSchedules(ctx, s.db,
		s.cfg.Checker.AliveInterval, s.cfg.Checker.PricingInterval,
		s.cfg.Checker.ProbeInterval, s.cfg.Checker.BalanceInterval, s.cfg.Checker.DailyProbeBudget,
		s.cfg.Security.EncryptionKey)
	if err != nil {
		s.logger.Error("Load schedules failed", zap.Error(err))
		return
	}
	if len(schedules) == 0 {
		return
	}

	now := time.Now()
	s.runCleanup(ctx) // 自守卫：每日一次

	// 初始化新渠道的 lastRun
	for _, sch := range schedules {
		if s.lastRun[sch.ID] == nil {
			s.lastRun[sch.ID] = map[string]time.Time{}
		}
	}

	// E4：回收已删除站点的 lastRun（长期增删站点会累积进程内状态）
	for id := range s.lastRun {
		found := false
		for _, sch := range schedules {
			if sch.ID == id {
				found = true
				break
			}
		}
		if !found {
			delete(s.lastRun, id)
		}
	}

	// 预计算到期任务（纯内存判定，主 goroutine 读 lastRun 无竞态）
	var dueAlive []checker.ChannelSchedule
	type channelTasks struct {
		sch     checker.ChannelSchedule
		pricing bool
		probe   bool
		balance bool
	}
	var dueTasks []channelTasks
	for _, sch := range schedules {
		last := s.lastRun[sch.ID]
		if now.Sub(last["alive"]) >= sch.AliveInterval {
			dueAlive = append(dueAlive, sch)
		}
		t := channelTasks{sch: sch}
		if now.Sub(last["pricing"]) >= sch.PricingInterval {
			t.pricing = true
		}
		if probeDue(now, last, sch.ProbeInterval, s.cfg.Checker.ProbeFailedBackoff) {
			t.probe = true
		}
		if now.Sub(last["balance"]) >= sch.BalanceInterval {
			t.balance = true
		}
		if t.pricing || t.probe || t.balance {
			dueTasks = append(dueTasks, t)
		}
	}

	// A2：本轮没有任何到期任务——跳过 epoch 读取与发布。此前每 5s 无条件
	// bump epoch，快照缓存（TTL 10s）到期后必然全量重建（6 个批量查询 +
	// 归档），即使数据无任何变化。
	if len(dueAlive) == 0 && len(dueTasks) == 0 {
		return
	}

	// H11：整轮工作 epoch 先写后发布。读取失败则跳过本轮写入
	// （fail-closed，不复用旧 epoch 写入混合数据）。
	pendingEpoch, err := s.db.PeekNextEpoch(ctx)
	if err != nil {
		s.logger.Error("Peek working epoch failed, tick skipped", zap.Error(err))
		return
	}
	s.pendingEpoch = pendingEpoch // 供 runProbe 使用

	// 全局探针预算粗闸门（DB 汇总；Redis 原子预留是硬闸门）。
	// 读取失败 → 本 tick 禁用付费探针（fail-closed），价格/余额不受影响。
	s.probeDisabled.Store(false)
	globalSpent, err := s.probe.TodaySpent(ctx)
	if err != nil {
		s.logger.Error("Failed to read global probe spending; paid probes disabled for this tick", zap.Error(err))
		s.probeDisabled.Store(true)
	} else {
		left := checker.ToBudgetUnits(s.cfg.Checker.DailyProbeBudget) - checker.ToBudgetUnits(globalSpent)
		if left < 0 {
			left = 0
		}
		s.probeBudgetMicro.Store(left)
	}

	// 存活探测：收集到期渠道并并发执行（单个黑洞上游不再拖慢整轮）
	if len(dueAlive) > 0 {
		var wg sync.WaitGroup
		sem := make(chan struct{}, 8) // 并发上限
		for _, sch := range dueAlive {
			wg.Add(1)
			sem <- struct{}{}
			go func(u checker.Upstream, name string) {
				defer wg.Done()
				defer func() { <-sem }()
				if err := s.alive.CheckChannel(ctx, u, pendingEpoch); err != nil {
					s.logger.Debug("Alive check failed", zap.String("channel", name), zap.Error(err))
				}
			}(sch.Upstream, sch.Name)
		}
		wg.Wait()
		doneAt := time.Now()
		for _, sch := range dueAlive {
			s.lastRun[sch.ID]["alive"] = doneAt
		}
	}

	// A1：价格同步/推理探针/余额检测按站点并发执行。每站点一个 goroutine
	// 保持站内任务顺序（pricing→probe→balance），跨站并发由 semaphore 4 限流。
	// 此前串行执行：多站点探针同时到期时单轮 tick 可达 N×30s，期间 epoch
	// 不发布、快照停更、其余站点任务全部延迟。并发安全性由每站点
	// advisory lock（跨进程串行化同一站点探测）+ Redis 预算原子预留保证。
	if len(dueTasks) > 0 {
		type taskResult struct {
			id          int
			pricing     bool
			probe       bool // 已处理本次到期（含预算跳过等占位，用于推进 lastRun）
			probeFailed bool
			balance     bool
			probeCost   int64 // 微美元（成功探测的实际成本）
		}
		var wg sync.WaitGroup
		sem := make(chan struct{}, 4)
		results := make(chan taskResult, len(dueTasks))
		for _, t := range dueTasks {
			wg.Add(1)
			go func(t channelTasks) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				r := taskResult{id: t.sch.ID}
				if t.pricing {
					if err := s.pricing.SyncChannel(ctx, t.sch.Upstream, pendingEpoch); err != nil {
						s.logger.Debug("Pricing sync failed", zap.String("channel", t.sch.Name), zap.Error(err))
					}
					r.pricing = true
				}
				if t.probe && s.probeBudgetOK() {
					r.probe, r.probeFailed, r.probeCost = s.runProbe(ctx, t.sch)
				}
				if t.balance {
					if err := s.balance.CheckChannel(ctx, t.sch.Upstream); err != nil {
						s.logger.Debug("Balance check failed", zap.String("channel", t.sch.Name), zap.Error(err))
					}
					r.balance = true
				}
				results <- r
			}(t)
		}
		wg.Wait()
		close(results)

		// 汇总：lastRun 只在主 goroutine 更新（goroutine 不碰共享 map）
		for r := range results {
			last := s.lastRun[r.id]
			if r.pricing {
				last["pricing"] = now
			}
			if r.probe {
				last["probe"] = now
			}
			if r.probeFailed {
				last["probe_failed"] = now
			} else if r.probe {
				delete(last, "probe_failed")
			}
			if r.balance {
				last["balance"] = now
			}
			if r.probeCost > 0 {
				s.probeBudgetMicro.Add(-r.probeCost)
			}
		}
	}

	// H11：整轮写入完成后发布 epoch——发布之前，快照读取端
	// （WHERE epoch <= current）看不到任何本轮数据；发布之后整轮原子可见。
	if _, err := s.db.PublishEpoch(ctx, pendingEpoch); err != nil {
		s.logger.Error("Publish epoch failed", zap.Int64("epoch", pendingEpoch), zap.Error(err))
	}
}

// runProbe 执行单渠道推理探针（P1-06：预算原子预留 → 探测 → 按实际成本结算）。
// 并发安全：不修改任何共享可变状态，lastRun/预算通过返回值由调度器主循环汇总。
// 返回 (markedDone, failed, costMicro)：
//   - markedDone = 本次到期已处理（成功/失败/预算跳过等所有占位路径），
//     调度器据此推进 lastRun，避免每 5s tick 重复重试；
//   - failed = 探测真实失败（写 probe_failed 退避）；
//   - costMicro = 成功探测的微美元实际成本（预算粗闸门扣减用）。
func (s *scheduler) runProbe(ctx context.Context, sch checker.ChannelSchedule) (markedDone, failed bool, costMicro int64) {
	// DB 汇总粗闸门（渠道有效预算）
	upstreamSpent, err := s.probe.UpstreamTodaySpent(ctx, sch.ID)
	if err != nil {
		s.logger.Error("Failed to read channel probe spending; paid probe skipped",
			zap.String("channel", sch.Name), zap.Error(err))
		return true, false, 0
	}
	if upstreamSpent >= sch.EffectiveBudget {
		s.logger.Debug("Channel probe budget exceeded, skipped",
			zap.String("channel", sch.Name),
			zap.Float64("spent", upstreamSpent),
			zap.Float64("budget", sch.EffectiveBudget))
		return true, false, 0
	}

	// H7：Redis 不可用时失败关闭付费探针——DB check-then-act 无原子性，
	// 多实例/并发手动探测会突破预算，宁可跳过也不超支。
	if s.budget == nil || !s.budget.Available() {
		s.logger.Warn("Budget tracker unavailable, paid probe skipped (fail-closed)",
			zap.String("channel", sch.Name))
		return true, false, 0
	}

	// 估算单次探测成本并做原子预留（模型与 ProbeChannel 的选择一致）
	probeModel := s.probe.ScheduledProbeModel(sch.Upstream)
	estCost, err := s.probe.EstimateProbeCost(ctx, probeModel, 16, 16)
	if err != nil {
		s.logger.Warn("Estimate probe cost failed, probe skipped", zap.String("channel", sch.Name), zap.Error(err))
		return true, false, 0
	}
	estimateUnits := checker.ToBudgetUnits(estCost)
	// C3：DB 当日已消费金额作为 Redis 键缺失时的播种基线
	globalSpent, gerr := s.probe.TodaySpent(ctx)
	if gerr != nil {
		s.logger.Warn("Failed to read global probe spending; probe skipped", zap.Error(gerr))
		return true, false, 0
	}
	globalSpentUnits := checker.ToBudgetUnits(globalSpent)
	channelSpentUnits := checker.ToBudgetUnits(upstreamSpent)
	ok, day, rerr := s.budget.Reserve(ctx, sch.ID, estimateUnits,
		checker.ToBudgetUnits(sch.EffectiveBudget),
		checker.ToBudgetUnits(s.cfg.Checker.DailyProbeBudget),
		channelSpentUnits, globalSpentUnits)
	if rerr != nil {
		s.logger.Warn("Budget reservation failed, probe skipped", zap.String("channel", sch.Name), zap.Error(rerr))
		return true, false, 0
	}
	if !ok {
		s.logger.Info("Probe budget exhausted (concurrent reservations), skipped",
			zap.String("channel", sch.Name),
			zap.Float64("effective_budget", sch.EffectiveBudget))
		return true, false, 0
	}

	// 执行探测
	res, err := s.probe.ProbeChannel(ctx, sch.Upstream, s.pendingEpoch)
	// 结算：BudgetSettlement 决定按实际成本结算或保守保留预留
	// （C5 扩展：聊天已发起即可能已扣费——chat 失败/余额后读取
	// 失败/成功但成本不可测都保留预留，绝不「已扣费但账目为零」；
	// 仅 balance_before 失败全额退款）。
	keep, costUSD := res.BudgetSettlement()
	actualUnits := checker.ToBudgetUnits(costUSD)
	if keep {
		actualUnits = estimateUnits
	}
	// H8：结算失败必须告警并保守处置——补扣失败意味着 Redis 少记消费，
	// 立即失败关闭本 tick 剩余探测（probeDisabled 并发安全版），防预算继续超支。
	if aerr := s.budget.Adjust(ctx, sch.ID, day, actualUnits-estimateUnits); aerr != nil {
		s.logger.Error("Budget adjustment failed; paid probes disabled for this tick",
			zap.Int64("channel_id", int64(sch.ID)), zap.Error(aerr))
		s.probeDisabled.Store(true)
	}
	if err != nil {
		s.logger.Debug("Probe failed", zap.String("channel", sch.Name), zap.Error(err))
		return true, true, 0
	}
	cost := int64(0)
	if res != nil {
		cost = checker.ToBudgetUnits(res.Cost)
	}
	return true, false, cost
}

// probeBudgetOK 探针粗闸门：本 tick 未被结算失败禁用且内存预算剩余 > 0。
// tick 级闸门不占位（不推进 lastRun）——预算恢复后下一 tick 即可重试。
func (s *scheduler) probeBudgetOK() bool {
	return !s.probeDisabled.Load() && s.probeBudgetMicro.Load() > 0
}

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"smart-router/internal/checker"
	"smart-router/internal/config"
	"smart-router/internal/store"

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

	// 初始化 checker
	aliveChecker := checker.NewAliveChecker(db, logger.Named("alive"))
	pricingChecker := checker.NewPricingChecker(db, logger.Named("pricing"))
	probeChecker := checker.NewProbeChecker(db, logger.Named("probe"))
	balanceChecker := checker.NewBalanceChecker(db, logger.Named("balance"))
	probeChecker.SetProbeModel(cfg.Checker.ProbeModel)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sched := newScheduler(db, logger, cfg, aliveChecker, pricingChecker, probeChecker, balanceChecker)
	go sched.run(ctx)

	logger.Info("Scheduler started (tick 5s)")

	// 等待中断信号
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("Shutting down checker...")
	cancel()
	time.Sleep(time.Second) // 给 goroutine 时间清理
	logger.Info("Checker exited")
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

	// 每个渠道每个任务的上次执行时间
	lastRun map[int]map[string]time.Time
	epoch   int64

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

func newScheduler(db *store.DB, logger *zap.Logger, cfg *config.Config,
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

// runCleanup 按保留策略清理过期历史数据（每日一次；表名/列名均为常量）
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
	s.lastCleanup = time.Now()
}

func (s *scheduler) runAliveForAll(ctx context.Context) {
	schedules, err := checker.LoadSchedules(ctx, s.db,
		s.cfg.Checker.AliveInterval, s.cfg.Checker.PricingInterval,
		s.cfg.Checker.ProbeInterval, s.cfg.Checker.BalanceInterval, s.cfg.Checker.DailyProbeBudget)
	if err != nil {
		s.logger.Error("Load schedules failed", zap.Error(err))
		return
	}
	if len(schedules) == 0 {
		s.logger.Warn("No enabled channels found")
		return
	}

	s.epoch, _ = s.db.IncrementEpoch(ctx)
	now := time.Now()
	for _, sch := range schedules {
		s.lastRun[sch.ID] = map[string]time.Time{
			"alive":   now,
			"pricing": now,
			"probe":   now,
			"balance": {}, // 零值：第一个 tick 立即执行余额检测
		}
		if err := s.alive.CheckChannel(ctx, sch.Upstream, s.epoch); err != nil {
			s.logger.Debug("Initial alive check failed",
				zap.String("channel", sch.Name), zap.Error(err))
		}
	}
	s.logger.Info("Initial alive check completed", zap.Int("channels", len(schedules)))
}

func (s *scheduler) tick(ctx context.Context) {
	schedules, err := checker.LoadSchedules(ctx, s.db,
		s.cfg.Checker.AliveInterval, s.cfg.Checker.PricingInterval,
		s.cfg.Checker.ProbeInterval, s.cfg.Checker.BalanceInterval, s.cfg.Checker.DailyProbeBudget)
	if err != nil {
		s.logger.Error("Load schedules failed", zap.Error(err))
		return
	}
	if len(schedules) == 0 {
		return
	}

	now := time.Now()
	s.runCleanup(ctx) // 自守卫：每日一次

	// 全局探针预算检查
	globalSpent, err := s.probe.TodaySpent(ctx)
	globalBudgetLeft, globalBudgetAvailable := probeBudgetLeft(s.cfg.Checker.DailyProbeBudget, globalSpent, err)
	if !globalBudgetAvailable {
		s.logger.Error("Failed to read global probe spending; paid probes disabled for this tick", zap.Error(err))
	}

	// 初始化新渠道的 lastRun
	for _, sch := range schedules {
		if s.lastRun[sch.ID] == nil {
			s.lastRun[sch.ID] = map[string]time.Time{}
		}
	}

	// 存活探测：收集到期渠道并并发执行（单个黑洞上游不再拖慢整轮）
	var dueAlive []checker.ChannelSchedule
	for _, sch := range schedules {
		if now.Sub(s.lastRun[sch.ID]["alive"]) >= sch.AliveInterval {
			dueAlive = append(dueAlive, sch)
		}
	}
	if len(dueAlive) > 0 {
		if ep, err := s.db.IncrementEpoch(ctx); err == nil {
			s.epoch = ep
		} else {
			s.logger.Error("Failed to increment epoch", zap.Error(err))
		}
		var wg sync.WaitGroup
		sem := make(chan struct{}, 8) // 并发上限
		for _, sch := range dueAlive {
			wg.Add(1)
			sem <- struct{}{}
			go func(u checker.Upstream, name string) {
				defer wg.Done()
				defer func() { <-sem }()
				if err := s.alive.CheckChannel(ctx, u, s.epoch); err != nil {
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

	for _, sch := range schedules {
		last := s.lastRun[sch.ID]

		// 价格同步
		if now.Sub(last["pricing"]) >= sch.PricingInterval {
			if err := s.pricing.SyncChannel(ctx, sch.Upstream, s.epoch); err != nil {
				s.logger.Debug("Pricing sync failed", zap.String("channel", sch.Name), zap.Error(err))
			}
			last["pricing"] = now
		}

		// 推理探针（预算控制：全局 + 渠道/分组有效预算）
		if globalBudgetLeft > 0 && probeDue(now, last, sch.ProbeInterval, s.cfg.Checker.ProbeFailedBackoff) {
			upstreamSpent, err := s.probe.UpstreamTodaySpent(ctx, sch.ID)
			if err != nil {
				s.logger.Error("Failed to read channel probe spending; paid probe skipped",
					zap.String("channel", sch.Name), zap.Error(err))
			} else if upstreamSpent < sch.EffectiveBudget {
				cost, err := s.probe.ProbeChannel(ctx, sch.Upstream, s.epoch)
				var failed bool
				globalBudgetLeft, failed = accountProbeResult(globalBudgetLeft, cost, err)
				if failed {
					last["probe_failed"] = now
					s.logger.Debug("Probe failed", zap.String("channel", sch.Name), zap.Error(err))
				} else {
					delete(last, "probe_failed")
				}
				last["probe"] = now
			}
		}

		// 余额检测（轻量 GET，不花钱）
		if now.Sub(last["balance"]) >= sch.BalanceInterval {
			if err := s.balance.CheckChannel(ctx, sch.Upstream); err != nil {
				s.logger.Debug("Balance check failed", zap.String("channel", sch.Name), zap.Error(err))
			}
			last["balance"] = now
		}
	}
}

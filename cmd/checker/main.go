package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
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
	aliveRan := false

	// 全局探针预算检查
	globalSpent, err := s.probe.TodaySpent(ctx)
	globalBudgetLeft, globalBudgetAvailable := probeBudgetLeft(s.cfg.Checker.DailyProbeBudget, globalSpent, err)
	if !globalBudgetAvailable {
		s.logger.Error("Failed to read global probe spending; paid probes disabled for this tick", zap.Error(err))
	}

	for _, sch := range schedules {
		last := s.lastRun[sch.ID]
		if last == nil {
			// 新渠道：立即执行一轮全部检测（存活/价格/余额，探针受预算保护）
			last = map[string]time.Time{}
			s.lastRun[sch.ID] = last
		}

		// 存活探测
		if now.Sub(last["alive"]) >= sch.AliveInterval {
			if !aliveRan {
				s.epoch, _ = s.db.IncrementEpoch(ctx)
				aliveRan = true
			}
			if err := s.alive.CheckChannel(ctx, sch.Upstream, s.epoch); err != nil {
				s.logger.Debug("Alive check failed", zap.String("channel", sch.Name), zap.Error(err))
			}
			last["alive"] = now
		}

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

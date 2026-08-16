package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"smart-router/internal/config"
	"smart-router/internal/migrate"
	"smart-router/internal/replay"
	"smart-router/internal/router"
	"smart-router/internal/store"

	"go.uber.org/zap"
)

func main() {
	// 命令行参数
	configPath := flag.String("config", "configs/config.yaml", "配置文件路径")
	startTime := flag.String("start", "", "开始时间 (RFC3339 格式，例如: 2024-01-01T10:00:00Z)")
	endTime := flag.String("end", "", "结束时间 (RFC3339 格式)")
	requestIDs := flag.String("requests", "", "请求 ID 列表（逗号分隔）")
	limit := flag.Int("limit", 1000, "最大重放数量")
	newStrategy := flag.String("strategy", "", "测试新策略（可选）")
	outputJSON := flag.String("json", "", "输出 JSON 文件路径")
	outputMD := flag.String("markdown", "", "输出 Markdown 文件路径")
	showTable := flag.Bool("table", true, "在控制台显示表格")

	flag.Parse()

	// 加载配置
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: 加载配置失败: %v\n", err)
		os.Exit(1)
	}

	// 连接数据库
	db, err := store.NewPostgres(cfg.Database.Postgres)
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: 连接 PostgreSQL 失败: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	// 启动时执行版本化迁移（P2-12）
	mctx, mcancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer mcancel()
	if err := migrate.Up(mctx, db.Pool, zap.NewNop()); err != nil {
		fmt.Fprintf(os.Stderr, "错误: 数据库迁移失败: %v\n", err)
		os.Exit(1)
	}

	redis, err := store.NewRedis(cfg.Database.Redis)
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: 连接 Redis 失败: %v\n", err)
		os.Exit(1)
	}
	defer redis.Close()

	// 解析参数
	req, err := parseRequest(*startTime, *endTime, *requestIDs, *limit, *newStrategy)
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: 参数解析失败: %v\n", err)
		fmt.Fprintf(os.Stderr, "\n使用示例:\n")
		fmt.Fprintf(os.Stderr, "  # 重放最近 1 小时的决策\n")
		fmt.Fprintf(os.Stderr, "  replay --start 2024-01-01T10:00:00Z --end 2024-01-01T11:00:00Z\n\n")
		fmt.Fprintf(os.Stderr, "  # 重放特定请求\n")
		fmt.Fprintf(os.Stderr, "  replay --requests req1,req2,req3\n\n")
		fmt.Fprintf(os.Stderr, "  # 测试新策略\n")
		fmt.Fprintf(os.Stderr, "  replay --start 2024-01-01T10:00:00Z --end 2024-01-01T11:00:00Z --strategy price_first\n\n")
		os.Exit(1)
	}

	// 创建重放器
	replayer := replay.NewReplayer(db, redis)

	// 注入与网关一致的系统级策略默认值（保证重放与生产决策可比）
	replayer.SetPolicyDefaults(router.PolicyDefaults{
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

	// 执行重放
	fmt.Println("🔄 开始重放决策...")
	fmt.Println()

	ctx := context.Background()
	report, err := replayer.Replay(ctx, *req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: 重放失败: %v\n", err)
		os.Exit(1)
	}

	// 输出结果
	if *showTable {
		report.PrintTable()
	}

	// 导出 JSON
	if *outputJSON != "" {
		if err := report.ExportJSON(*outputJSON); err != nil {
			fmt.Fprintf(os.Stderr, "警告: 导出 JSON 失败: %v\n", err)
		} else {
			fmt.Printf("✅ JSON 报告已保存到: %s\n", *outputJSON)
		}
	}

	// 导出 Markdown
	if *outputMD != "" {
		if err := report.ExportMarkdown(*outputMD); err != nil {
			fmt.Fprintf(os.Stderr, "警告: 导出 Markdown 失败: %v\n", err)
		} else {
			fmt.Printf("✅ Markdown 报告已保存到: %s\n", *outputMD)
		}
	}

	fmt.Println("✅ 重放完成！")
}

// parseRequest 解析命令行参数为重放请求
func parseRequest(startStr, endStr, requestIDsStr string, limit int, newStrategy string) (*replay.ReplayRequest, error) {
	req := &replay.ReplayRequest{
		Limit:       limit,
		NewStrategy: newStrategy,
	}

	// 解析请求 ID 列表
	if requestIDsStr != "" {
		req.RequestIDs = strings.Split(requestIDsStr, ",")
		for i := range req.RequestIDs {
			req.RequestIDs[i] = strings.TrimSpace(req.RequestIDs[i])
		}
		return req, nil
	}

	// 解析时间范围
	if startStr == "" && endStr == "" {
		return nil, fmt.Errorf("必须指定 --start 和 --end，或者 --requests")
	}

	if startStr != "" {
		t, err := time.Parse(time.RFC3339, startStr)
		if err != nil {
			return nil, fmt.Errorf("解析开始时间失败: %w (格式应为: 2006-01-02T15:04:05Z)", err)
		}
		req.StartTime = t
	}

	if endStr != "" {
		t, err := time.Parse(time.RFC3339, endStr)
		if err != nil {
			return nil, fmt.Errorf("解析结束时间失败: %w (格式应为: 2006-01-02T15:04:05Z)", err)
		}
		req.EndTime = t
	}

	// 如果只指定了开始时间，结束时间默认为现在
	if req.EndTime.IsZero() && !req.StartTime.IsZero() {
		req.EndTime = time.Now()
	}

	// 如果只指定了结束时间，开始时间默认为 1 小时前
	if req.StartTime.IsZero() && !req.EndTime.IsZero() {
		req.StartTime = req.EndTime.Add(-1 * time.Hour)
	}

	// 验证时间范围
	if !req.StartTime.IsZero() && !req.EndTime.IsZero() && req.StartTime.After(req.EndTime) {
		return nil, fmt.Errorf("开始时间不能晚于结束时间")
	}

	return req, nil
}

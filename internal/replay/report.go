package replay

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
)

// parseJSONArray 解析 JSON 数组
func parseJSONArray(data []byte, v interface{}) error {
	if len(data) == 0 {
		return nil
	}
	return json.Unmarshal(data, v)
}

// PrintMarkdown 生成 Markdown 格式的报告
func (r *Report) PrintMarkdown() string {
	var sb strings.Builder

	sb.WriteString("# 决策重放报告\n\n")

	// 基本信息
	sb.WriteString("## 📊 概览\n\n")
	sb.WriteString(fmt.Sprintf("- **总请求数**: %d\n", r.TotalRequests))
	sb.WriteString(fmt.Sprintf("- **成功重放**: %d\n", r.SuccessfulReplays))
	sb.WriteString(fmt.Sprintf("- **失败重放**: %d\n", r.FailedReplays))
	sb.WriteString(fmt.Sprintf("- **渠道变化数**: %d\n", r.ChannelChangedCount))
	sb.WriteString(fmt.Sprintf("- **渠道变化率**: %.2f%%\n", r.ChannelChangedRate*100))
	if r.StrategyUsed != "" {
		sb.WriteString(fmt.Sprintf("- **测试策略**: %s\n", r.StrategyUsed))
	}
	sb.WriteString(fmt.Sprintf("- **时间范围**: %s\n", r.TimeRange))
	sb.WriteString(fmt.Sprintf("- **生成时间**: %s\n\n", r.GeneratedAt.Format("2006-01-02 15:04:05")))

	// 变化详情
	if r.ChannelChangedCount > 0 {
		sb.WriteString("## 🔄 渠道变化详情\n\n")
		sb.WriteString("| 请求 ID | 模型 | 原渠道 | 新渠道 | 决策时间 |\n")
		sb.WriteString("|---------|------|--------|--------|----------|\n")

		for _, detail := range r.Details {
			if detail.ChannelChanged {
				sb.WriteString(fmt.Sprintf("| %s | %s | %d | %d | %s |\n",
					truncate(detail.RequestID, 20),
					detail.Model,
					detail.OriginalChannel,
					detail.ReplayChannel,
					detail.DecidedAt.Format("15:04:05"),
				))
			}
		}
		sb.WriteString("\n")
	}

	// 未变化统计
	unchangedCount := r.SuccessfulReplays - r.ChannelChangedCount
	if unchangedCount > 0 {
		sb.WriteString(fmt.Sprintf("## ✅ 未变化请求: %d (%.2f%%)\n\n",
			unchangedCount,
			float64(unchangedCount)/float64(r.SuccessfulReplays)*100,
		))
	}

	// 结论
	sb.WriteString("## 📝 结论\n\n")
	if r.ChannelChangedRate < 0.05 {
		sb.WriteString("✅ **策略稳定**：变化率低于 5%，决策一致性良好。\n")
	} else if r.ChannelChangedRate < 0.20 {
		sb.WriteString("⚠️ **轻微变化**：变化率在 5-20% 之间，建议审查变化原因。\n")
	} else {
		sb.WriteString("🔴 **显著变化**：变化率超过 20%，需要仔细评估影响。\n")
	}

	return sb.String()
}

// PrintTable 在控制台打印表格
func (r *Report) PrintTable() {
	fmt.Println("\n=== 决策重放报告 ===\n")
	fmt.Printf("总请求数: %d\n", r.TotalRequests)
	fmt.Printf("成功重放: %d\n", r.SuccessfulReplays)
	fmt.Printf("失败重放: %d\n", r.FailedReplays)
	fmt.Printf("渠道变化: %d (%.2f%%)\n", r.ChannelChangedCount, r.ChannelChangedRate*100)
	if r.StrategyUsed != "" {
		fmt.Printf("测试策略: %s\n", r.StrategyUsed)
	}
	fmt.Printf("时间范围: %s\n", r.TimeRange)
	fmt.Println()

	if len(r.Details) == 0 {
		fmt.Println("没有重放结果。")
		return
	}

	// 打印变化的请求
	if r.ChannelChangedCount > 0 {
		fmt.Println("渠道变化详情:")
		fmt.Println()

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "请求ID\t模型\t原渠道\t新渠道\t变化\t时间")
		fmt.Fprintln(w, "------\t----\t------\t------\t----\t----")

		for _, detail := range r.Details {
			if detail.ChannelChanged {
				changed := "✓"
				fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%s\t%s\n",
					truncate(detail.RequestID, 20),
					detail.Model,
					detail.OriginalChannel,
					detail.ReplayChannel,
					changed,
					detail.DecidedAt.Format("15:04:05"),
				)
			}
		}
		w.Flush()
		fmt.Println()
	}

	// 统计信息
	fmt.Println("--- 分析 ---")
	unchangedCount := r.SuccessfulReplays - r.ChannelChangedCount
	if unchangedCount > 0 {
		fmt.Printf("未变化请求: %d (%.2f%%)\n",
			unchangedCount,
			float64(unchangedCount)/float64(r.SuccessfulReplays)*100,
		)
	}

	if r.ChannelChangedRate < 0.05 {
		fmt.Println("✅ 策略稳定：变化率低于 5%")
	} else if r.ChannelChangedRate < 0.20 {
		fmt.Println("⚠️  轻微变化：变化率在 5-20% 之间")
	} else {
		fmt.Println("🔴 显著变化：变化率超过 20%")
	}
	fmt.Println()
}

// ExportJSON 导出为 JSON 文件
func (r *Report) ExportJSON(path string) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write file: %w", err)
	}

	return nil
}

// ExportMarkdown 导出为 Markdown 文件
func (r *Report) ExportMarkdown(path string) error {
	content := r.PrintMarkdown()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return fmt.Errorf("write file: %w", err)
	}
	return nil
}

// truncate 截断字符串
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

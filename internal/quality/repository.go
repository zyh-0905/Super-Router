package quality

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"smart-router/internal/store"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrChannelBusy 同站点已有活跃任务（HTTP 409 依据）。
type ErrChannelBusy struct {
	ExistingRunID int64
}

func (e *ErrChannelBusy) Error() string {
	return fmt.Sprintf("channel already has an active quality check (run %d)", e.ExistingRunID)
}

// ErrLostOwnership 写入时任务已不属于本 worker：
// 心跳超时被 RecoverStale 回收并由其它 worker 重新领取后，
// 旧执行体仍尝试写结果/终态。调用方必须停止执行，不得继续写库。
var ErrLostOwnership = errors.New("quality run ownership lost")

// Repository 质量任务持久化接口。
// 终态/结果写入均携带 workerID 并校验所有权（C3）：
// 谓词不匹配（已回收/被接管）时 RowsAffected=0 → ErrLostOwnership。
type Repository interface {
	Create(ctx context.Context, channelID int, model, depth, requesterHash string) (*Run, error)
	Get(ctx context.Context, id int64) (*Run, []StageResult, error)
	ListByChannel(ctx context.Context, channelID, limit int) ([]Run, error)
	FindActiveByChannel(ctx context.Context, channelID int) (*Run, error)
	ClaimNext(ctx context.Context, workerID string) (*Run, error)
	UpsertResult(ctx context.Context, runID int64, workerID string, result StageResult) error
	SetProgress(ctx context.Context, runID int64, stage string, progress int) error
	Heartbeat(ctx context.Context, runID int64, workerID string) error
	RequestCancel(ctx context.Context, runID int64) error
	// CancelIfRequested 检查取消请求并原子置为 cancelled：
	// 返回 (是否已取消, error)。单语句完成检查+推进，消除
	// 「读到 cancel_requested 后再无条件 Cancel」的竞态窗口。
	CancelIfRequested(ctx context.Context, runID int64, workerID string) (bool, error)
	Complete(ctx context.Context, runID int64, workerID string, overall OverallStatus) error
	Fail(ctx context.Context, runID int64, workerID string, message string) error
	Cancel(ctx context.Context, runID int64, workerID string) error
	RecoverStale(ctx context.Context, olderThan time.Time, maxAttempts int) (int64, error)
}

// PostgresRepository Repository 的 PostgreSQL 实现。
type PostgresRepository struct {
	Pool *pgxpool.Pool
}

// NewPostgresRepository 创建 Repository（db 为 nil 时返回 nil，便于测试注入）。
func NewPostgresRepository(db *store.DB) *PostgresRepository {
	if db == nil {
		return nil
	}
	return &PostgresRepository{Pool: db.Pool}
}

// Create 创建任务；捕获活跃任务唯一索引冲突并转换为 ErrChannelBusy。
func (r *PostgresRepository) Create(ctx context.Context, channelID int, model, depth, requesterHash string) (*Run, error) {
	var id int64
	err := r.Pool.QueryRow(ctx, `
		INSERT INTO quality_check_runs (channel_id, model, depth, status, requested_by_key_hash)
		VALUES ($1, $2, $3, 'queued', $4)
		RETURNING id
	`, channelID, model, depth, requesterHash).Scan(&id)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			// 部分唯一索引冲突 → 找到已有活跃任务
			active, ferr := r.FindActiveByChannel(ctx, channelID)
			if ferr == nil && active != nil {
				return nil, &ErrChannelBusy{ExistingRunID: active.ID}
			}
			return nil, &ErrChannelBusy{}
		}
		return nil, fmt.Errorf("create run: %w", err)
	}
	run, _, err := r.Get(ctx, id)
	return run, err
}

// Get 返回任务及其阶段结果（按阶段顺序排列）。
func (r *PostgresRepository) Get(ctx context.Context, id int64) (*Run, []StageResult, error) {
	run, err := scanRun(r.Pool.QueryRow(ctx, `
		SELECT id, channel_id, model, depth, status, COALESCE(overall_status, ''), current_stage,
		       progress, attempt_count, worker_id, heartbeat_at, requested_by_key_hash, error,
		       created_at, started_at, finished_at
		FROM quality_check_runs WHERE id = $1
	`, id))
	if err != nil {
		return nil, nil, err
	}
	results, err := r.results(ctx, id)
	return run, results, err
}

// scanRun 扫描单行任务。
func scanRun(row rowScanner) (*Run, error) {
	var run Run
	var overall, currentStage, workerID, requesterHash, errMsg string
	if err := row.Scan(
		&run.ID, &run.ChannelID, &run.Model, &run.Depth, &run.Status, &overall, &currentStage,
		&run.Progress, &run.AttemptCount, &workerID, &run.HeartbeatAt, &requesterHash, &errMsg,
		&run.CreatedAt, &run.StartedAt, &run.FinishedAt,
	); err != nil {
		return nil, err
	}
	run.OverallStatus = OverallStatus(overall)
	run.CurrentStage = currentStage
	run.WorkerID = workerID
	run.RequestedByKeyHash = requesterHash
	run.Error = errMsg
	return &run, nil
}

// results 读取任务全部阶段结果（阶段固定顺序 + created_at 保证稳定性）。
func (r *PostgresRepository) results(ctx context.Context, runID int64) ([]StageResult, error) {
	rows, err := r.Pool.Query(ctx, `
		SELECT stage, check_name, status, http_status, latency_ms, ttfb_ms, actual_model,
		       prompt_tokens, completion_tokens, total_tokens, COALESCE(details::text, '{}'), error
		FROM quality_check_results
		WHERE run_id = $1
		ORDER BY created_at, id
	`, runID)
	if err != nil {
		return nil, fmt.Errorf("query results: %w", err)
	}
	defer rows.Close()

	var out []StageResult
	for rows.Next() {
		var sr StageResult
		var detailsJSON string
		if err := rows.Scan(&sr.Stage, &sr.CheckName, &sr.Status, &sr.HTTPStatus, &sr.LatencyMS, &sr.TTFBMS,
			&sr.ActualModel, &sr.PromptTokens, &sr.CompletionTokens, &sr.TotalTokens, &detailsJSON, &sr.Error); err != nil {
			continue
		}
		sr.Details = map[string]interface{}{}
		_ = json.Unmarshal([]byte(detailsJSON), &sr.Details)
		out = append(out, sr)
	}
	return out, rows.Err()
}

// ListByChannel 站点最近任务（limit 上限 100）。
func (r *PostgresRepository) ListByChannel(ctx context.Context, channelID, limit int) ([]Run, error) {
	if limit <= 0 {
		limit = 5
	}
	if limit > 100 {
		limit = 100
	}
	rows, err := r.Pool.Query(ctx, `
		SELECT id, channel_id, model, depth, status, COALESCE(overall_status, ''), current_stage,
		       progress, attempt_count, worker_id, heartbeat_at, requested_by_key_hash, error,
		       created_at, started_at, finished_at
		FROM quality_check_runs
		WHERE channel_id = $1
		ORDER BY created_at DESC
		LIMIT $2
	`, channelID, limit)
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	defer rows.Close()

	var out []Run
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			continue
		}
		out = append(out, *run)
	}
	return out, rows.Err()
}

// FindActiveByChannel 返回站点当前活跃任务（无则 nil）。
func (r *PostgresRepository) FindActiveByChannel(ctx context.Context, channelID int) (*Run, error) {
	run, err := scanRun(r.Pool.QueryRow(ctx, `
		SELECT id, channel_id, model, depth, status, COALESCE(overall_status, ''), current_stage,
		       progress, attempt_count, worker_id, heartbeat_at, requested_by_key_hash, error,
		       created_at, started_at, finished_at
		FROM quality_check_runs
		WHERE channel_id = $1 AND status IN ('queued', 'running', 'cancel_requested')
		ORDER BY id DESC LIMIT 1
	`, channelID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return run, err
}

// ClaimNext 领取队首任务（SKIP LOCKED，多 Worker 不重复领取）。
func (r *PostgresRepository) ClaimNext(ctx context.Context, workerID string) (*Run, error) {
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin claim: %w", err)
	}
	defer tx.Rollback(ctx)

	var id int64
	err = tx.QueryRow(ctx, `
		SELECT id FROM quality_check_runs
		WHERE status = 'queued'
		ORDER BY created_at
		FOR UPDATE SKIP LOCKED
		LIMIT 1
	`).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim next: %w", err)
	}

	now := time.Now()
	if _, err := tx.Exec(ctx, `
		UPDATE quality_check_runs
		SET status = 'running', attempt_count = attempt_count + 1,
		    worker_id = $2, started_at = COALESCE(started_at, $3), heartbeat_at = $3
		WHERE id = $1
	`, id, workerID, now); err != nil {
		return nil, fmt.Errorf("mark running: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit claim: %w", err)
	}
	run, results, err := r.Get(ctx, id)
	_ = results
	return run, err
}

// UpsertResult 写入/更新单阶段结果（C3：所有权谓词——被回收的任务
// 不允许继续写结果，避免「已完成任务的结果仍在变化」）。
func (r *PostgresRepository) UpsertResult(ctx context.Context, runID int64, workerID string, result StageResult) error {
	detailsJSON := []byte("{}")
	if result.Details != nil {
		detailsJSON, _ = json.Marshal(result.Details)
	}
	ct, err := r.Pool.Exec(ctx, `
		INSERT INTO quality_check_results
			(run_id, stage, check_name, status, http_status, latency_ms, ttfb_ms, actual_model,
			 prompt_tokens, completion_tokens, total_tokens, details, error)
		SELECT $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13
		WHERE EXISTS (
			SELECT 1 FROM quality_check_runs
			WHERE id = $1 AND worker_id = $14 AND status IN ('running', 'cancel_requested')
		)
		ON CONFLICT (run_id, stage, check_name)
		DO UPDATE SET status = EXCLUDED.status, http_status = EXCLUDED.http_status,
			latency_ms = EXCLUDED.latency_ms, ttfb_ms = EXCLUDED.ttfb_ms,
			actual_model = EXCLUDED.actual_model, prompt_tokens = EXCLUDED.prompt_tokens,
			completion_tokens = EXCLUDED.completion_tokens, total_tokens = EXCLUDED.total_tokens,
			details = EXCLUDED.details, error = EXCLUDED.error, updated_at = NOW()
	`, runID, result.Stage, result.CheckName, string(result.Status), result.HTTPStatus,
		result.LatencyMS, result.TTFBMS, result.ActualModel, result.PromptTokens,
		result.CompletionTokens, result.TotalTokens, detailsJSON, result.Error, workerID)
	if err != nil {
		return fmt.Errorf("upsert result: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("upsert result for run %d: %w", runID, ErrLostOwnership)
	}
	return nil
}

// SetProgress 更新阶段与进度。
func (r *PostgresRepository) SetProgress(ctx context.Context, runID int64, stage string, progress int) error {
	_, err := r.Pool.Exec(ctx, `
		UPDATE quality_check_runs SET current_stage = $2, progress = $3 WHERE id = $1
	`, runID, stage, progress)
	return err
}

// Heartbeat 心跳（同时校验所有权，避免已回收任务继续写结果）。
func (r *PostgresRepository) Heartbeat(ctx context.Context, runID int64, workerID string) error {
	ct, err := r.Pool.Exec(ctx, `
		UPDATE quality_check_runs SET heartbeat_at = NOW()
		WHERE id = $1 AND worker_id = $2 AND status = 'running'
	`, runID, workerID)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("heartbeat lost ownership of run %d", runID)
	}
	return nil
}

// RequestCancel 请求取消（仅活跃任务）。
// queued 任务没有执行者会推进状态：直接置为 cancelled 终态，
// 否则任务永远卡在 cancel_requested（ClaimNext 只领 queued），
// 并持续占用同渠道活跃任务唯一索引（C2）。
func (r *PostgresRepository) RequestCancel(ctx context.Context, runID int64) error {
	// 1. queued → 直接取消
	ct, err := r.Pool.Exec(ctx, `
		UPDATE quality_check_runs
		SET status = 'cancelled', finished_at = NOW(), heartbeat_at = NOW()
		WHERE id = $1 AND status = 'queued'
	`, runID)
	if err != nil {
		return err
	}
	if ct.RowsAffected() > 0 {
		return nil
	}

	// 2. running → 请求取消（执行者在阶段间检查并推进到 cancelled）
	ct, err = r.Pool.Exec(ctx, `
		UPDATE quality_check_runs
		SET status = 'cancel_requested'
		WHERE id = $1 AND status = 'running'
	`, runID)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("run %d is not cancellable", runID)
	}
	return nil
}

// CancelIfRequested 检查取消请求并原子推进为 cancelled（M9 整改）：
// 单语句完成「检查 + 取消」，取代「读 IsCancelRequested 再无条件 Cancel」
// 的 check-then-act——后者在任务已被 RecoverStale 回收时会覆写他人状态。
// 返回 (是否已取消, error)；未请求取消或已失去所有权返回 (false, nil)。
func (r *PostgresRepository) CancelIfRequested(ctx context.Context, runID int64, workerID string) (bool, error) {
	ct, err := r.Pool.Exec(ctx, `
		UPDATE quality_check_runs
		SET status = 'cancelled', finished_at = NOW(), heartbeat_at = NOW()
		WHERE id = $1 AND worker_id = $2 AND status = 'cancel_requested'
	`, runID, workerID)
	if err != nil {
		return false, err
	}
	return ct.RowsAffected() > 0, nil
}

// Complete 标记完成（C3：所有权+状态谓词，被回收的任务不可写终态）。
func (r *PostgresRepository) Complete(ctx context.Context, runID int64, workerID string, overall OverallStatus) error {
	ct, err := r.Pool.Exec(ctx, `
		UPDATE quality_check_runs
		SET status = 'completed', overall_status = $3, progress = 100,
		    finished_at = NOW(), heartbeat_at = NOW()
		WHERE id = $1 AND worker_id = $2 AND status IN ('running', 'cancel_requested')
	`, runID, workerID, string(overall))
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("complete run %d: %w", runID, ErrLostOwnership)
	}
	return nil
}

// Fail 标记失败（C3：所有权+状态谓词）。
func (r *PostgresRepository) Fail(ctx context.Context, runID int64, workerID string, message string) error {
	ct, err := r.Pool.Exec(ctx, `
		UPDATE quality_check_runs
		SET status = 'failed', error = $3, finished_at = NOW(), heartbeat_at = NOW()
		WHERE id = $1 AND worker_id = $2 AND status IN ('running', 'cancel_requested')
	`, runID, workerID, message)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("fail run %d: %w", runID, ErrLostOwnership)
	}
	return nil
}

// Cancel 标记已取消（C3：所有权+状态谓词；已被回收则静默幂等）。
func (r *PostgresRepository) Cancel(ctx context.Context, runID int64, workerID string) error {
	_, err := r.Pool.Exec(ctx, `
		UPDATE quality_check_runs
		SET status = 'cancelled', finished_at = NOW(), heartbeat_at = NOW()
		WHERE id = $1 AND worker_id = $2 AND status IN ('running', 'cancel_requested')
	`, runID, workerID)
	return err
}

// RecoverStale 回收心跳超时的 running 任务：
//   - 未超重试次数 → 回 queued；
//   - 已超重试次数 → 标 expired。
// 心跳超时的 cancel_requested 任务（执行者已崩溃）直接置 cancelled，
// 否则同样永久卡在非终态并占用活跃任务唯一索引（C2）。
// 回 queued 时若该站点已有新活跃任务（唯一索引会冲突），改置 expired：
// 单条 UPDATE 批量执行时一行唯一索引冲突会使整条语句失败、所有
// stale 任务卡死，逐行回收可隔离冲突。
// 返回回收数量。
func (r *PostgresRepository) RecoverStale(ctx context.Context, olderThan time.Time, maxAttempts int) (int64, error) {
	rows, err := r.Pool.Query(ctx, `
		SELECT id, channel_id, attempt_count, status
		FROM quality_check_runs
		WHERE status IN ('running', 'cancel_requested')
		  AND heartbeat_at < $1
		FOR UPDATE
	`, olderThan)
	if err != nil {
		return 0, fmt.Errorf("recover stale: %w", err)
	}
	type staleRun struct {
		id          int64
		channelID   int
		attempts    int
		cancelReq   bool
	}
	var stale []staleRun
	for rows.Next() {
		var s staleRun
		var status string
		if err := rows.Scan(&s.id, &s.channelID, &s.attempts, &status); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan stale run: %w", err)
		}
		s.cancelReq = RunStatus(status) == RunCancelRequested
		stale = append(stale, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("scan stale runs: %w", err)
	}
	if len(stale) == 0 {
		return 0, nil
	}

	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin recover: %w", err)
	}
	defer tx.Rollback(ctx)

	var recovered int64
	for _, s := range stale {
		switch {
		case s.cancelReq:
			if _, err := tx.Exec(ctx, `
				UPDATE quality_check_runs
				SET status = 'cancelled', worker_id = '', finished_at = NOW(), heartbeat_at = NOW()
				WHERE id = $1 AND status = 'cancel_requested'
			`, s.id); err != nil {
				return recovered, fmt.Errorf("recover stale cancel %d: %w", s.id, err)
			}
			recovered++
		case s.attempts < maxAttempts:
			// 回 queued 前检查同站点是否已有活跃任务：有则直接 expired，
			// 避免唯一索引冲突（M12）。
			var exists bool
			if err := tx.QueryRow(ctx, `
				SELECT EXISTS(
					SELECT 1 FROM quality_check_runs
					WHERE channel_id = $1 AND id <> $2
					  AND status IN ('queued', 'running', 'cancel_requested')
				)
			`, s.channelID, s.id).Scan(&exists); err != nil {
				return recovered, fmt.Errorf("check active run %d: %w", s.id, err)
			}
			if exists {
				if _, err := tx.Exec(ctx, `
					UPDATE quality_check_runs
					SET status = 'expired', worker_id = '', finished_at = NOW(), heartbeat_at = NOW()
					WHERE id = $1 AND status = 'running'
				`, s.id); err != nil {
					return recovered, fmt.Errorf("expire conflicting run %d: %w", s.id, err)
				}
			} else if _, err := tx.Exec(ctx, `
				UPDATE quality_check_runs
				SET status = 'queued', worker_id = '', finished_at = NULL, heartbeat_at = NOW()
				WHERE id = $1 AND status = 'running'
			`, s.id); err != nil {
				return recovered, fmt.Errorf("requeue stale run %d: %w", s.id, err)
			}
			recovered++
		default:
			if _, err := tx.Exec(ctx, `
				UPDATE quality_check_runs
				SET status = 'expired', worker_id = '', finished_at = NOW(), heartbeat_at = NOW()
				WHERE id = $1 AND status = 'running'
			`, s.id); err != nil {
				return recovered, fmt.Errorf("expire stale run %d: %w", s.id, err)
			}
			recovered++
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit recover: %w", err)
	}
	return recovered, nil
}

// rowScanner 行扫描接口（pgx.Row / pgx.Rows 都满足）。
type rowScanner interface {
	Scan(dest ...interface{}) error
}

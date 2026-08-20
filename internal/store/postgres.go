package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"smart-router/internal/config"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type DB struct {
	Pool *pgxpool.Pool
}

func NewPostgres(cfg config.PostgresConfig) (*DB, error) {
	dsn := fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=disable",
		cfg.Host, cfg.Port, cfg.User, cfg.Password, cfg.DBName,
	)

	poolConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	// 连接池配置
	poolConfig.MaxConns = 20
	poolConfig.MinConns = 5
	poolConfig.MaxConnLifetime = time.Hour
	poolConfig.MaxConnIdleTime = 30 * time.Minute
	poolConfig.HealthCheckPeriod = time.Minute

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}

	// 测试连接
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}

	return &DB{Pool: pool}, nil
}

func (db *DB) Close() {
	if db.Pool != nil {
		db.Pool.Close()
	}
}

// IncrementEpoch 原子递增 epoch 并返回新值
func (db *DB) IncrementEpoch(ctx context.Context) (int64, error) {
	var epoch int64
	err := db.Pool.QueryRow(ctx, `
		UPDATE epoch_counter
		SET current_epoch = current_epoch + 1, updated_at = NOW()
		WHERE id = 1
		RETURNING current_epoch
	`).Scan(&epoch)
	return epoch, err
}

// PeekNextEpoch 读取下一个工作 epoch（不发布）。
// H11：探测结果以工作 epoch 写入，整轮完成后由 PublishEpoch 发布；
// 发布前快照读取端（WHERE epoch <= current）看不到任何本轮数据。
func (db *DB) PeekNextEpoch(ctx context.Context) (int64, error) {
	var epoch int64
	err := db.Pool.QueryRow(ctx, `
		SELECT current_epoch + 1 FROM epoch_counter WHERE id = 1
	`).Scan(&epoch)
	return epoch, err
}

// PublishEpoch 条件发布工作 epoch：只向前推进（幂等）。
// 返回 (是否发布, error)。多实例并发发布同一值只有一个生效。
func (db *DB) PublishEpoch(ctx context.Context, epoch int64) (bool, error) {
	var published int64
	err := db.Pool.QueryRow(ctx, `
		UPDATE epoch_counter
		SET current_epoch = $1, updated_at = NOW()
		WHERE id = 1 AND current_epoch < $1
		RETURNING current_epoch
	`, epoch).Scan(&published)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil // 已有其它实例发布过 ≥ epoch
	}
	return err == nil, err
}

// GetCurrentEpoch 获取当前 epoch
func (db *DB) GetCurrentEpoch(ctx context.Context) (int64, error) {
	var epoch int64
	err := db.Pool.QueryRow(ctx, `
		SELECT current_epoch FROM epoch_counter WHERE id = 1
	`).Scan(&epoch)
	return epoch, err
}

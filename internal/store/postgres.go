package store

import (
	"context"
	"fmt"
	"time"

	"smart-router/internal/config"

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

// GetCurrentEpoch 获取当前 epoch
func (db *DB) GetCurrentEpoch(ctx context.Context) (int64, error) {
	var epoch int64
	err := db.Pool.QueryRow(ctx, `
		SELECT current_epoch FROM epoch_counter WHERE id = 1
	`).Scan(&epoch)
	return epoch, err
}

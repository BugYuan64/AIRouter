package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DB 封装 PostgreSQL 连接池。
type DB struct {
	pool *pgxpool.Pool
}

// PoolOptions 配置连接池。数据面与管理面共用主池，
// 用量写入使用较小的独立池，避免慢查询占满数据面连接。
type PoolOptions struct {
	MaxConns int32
	MinConns int32
}

// Open 建立连接池并验证连通性。
// dsn 形如 postgres://user:pass@host:5432/db?sslmode=disable。
// 密码来自环境变量，不写入日志或错误信息。
func Open(ctx context.Context, dsn string, opts PoolOptions) (*DB, error) {
	if dsn == "" {
		return nil, errors.New("database DSN is required")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		// 不回显 DSN：其中可能包含密码
		return nil, errors.New("invalid database DSN")
	}
	if opts.MaxConns > 0 {
		cfg.MaxConns = opts.MaxConns
	}
	if opts.MinConns >= 0 {
		cfg.MinConns = opts.MinConns
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, errors.New("cannot create database pool")
	}
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, errors.New("cannot reach database")
	}
	return &DB{pool: pool}, nil
}

// Close 释放连接池。
func (d *DB) Close() {
	if d.pool != nil {
		d.pool.Close()
	}
}

// Ping 用于健康检查。
func (d *DB) Ping(ctx context.Context) error {
	return d.pool.Ping(ctx)
}

// UsageInserter 返回一个写入器，内部复用同一个连接池。
func (d *DB) UsageInserter() Inserter { return &usageInserter{pool: d.pool} }

type usageInserter struct{ pool *pgxpool.Pool }

// InsertUsage 在一个事务内批量插入。
// 任一字段为 nil 时写入 SQL NULL，不写 0。
func (u *usageInserter) InsertUsage(ctx context.Context, events []UsageEvent) error {
	if len(events) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	const q = `INSERT INTO usage_events
        (client_id, model, protocol, status, committed, stream,
         input_tokens, output_tokens, cache_read_tokens, cost_micros,
         latency_ms, first_byte_ms, created_at)
        VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`
	for _, e := range events {
		if e.Protocol != "" && !e.Protocol.Valid() {
			return fmt.Errorf("invalid protocol %q", e.Protocol)
		}
		batch.Queue(q,
			e.ClientID, e.Model, string(e.Protocol), e.Status, e.Committed, e.Stream,
			e.InputTokens, e.OutputTokens, e.CacheTokens, e.CostMicros,
			e.LatencyMS, e.FirstByteMS, e.CreatedAt,
		)
	}
	tx, err := u.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	results := tx.SendBatch(ctx, batch)
	for range events {
		if _, err := results.Exec(); err != nil {
			_ = results.Close()
			return err
		}
	}
	if err := results.Close(); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// UsageReader 提供用量查询，供管理面使用。
//
// clientID 为空表示查询全部账户（仅管理员可用）；
// 非空时只返回该账户的记录。调用方必须传入已认证的身份，
// 不能使用请求参数里的值。
type UsageReader interface {
	UsageSummary(ctx context.Context, clientID string, from, to time.Time) ([]UsageSummaryRow, error)
	RecentUsage(ctx context.Context, clientID string, limit int) ([]UsageDetailRow, error)
}

// UsageSummaryRow 是按客户端与协议聚合的一行。
type UsageSummaryRow struct {
	ClientID     string `json:"clientId"`
	Protocol     string `json:"protocol"`
	Requests     int64  `json:"requests"`
	Errors       int64  `json:"errors"`
	Streams      int64  `json:"streams"`
	AvgLatencyMS int64  `json:"avgLatencyMs"`
}

// UsageDetailRow 是单条用量明细。
type UsageDetailRow struct {
	ID           int64     `json:"id"`
	ClientID     string    `json:"clientId"`
	Model        string    `json:"model"`
	Protocol     string    `json:"protocol"`
	Status       int       `json:"status"`
	Stream       bool      `json:"stream"`
	InputTokens  *int64    `json:"inputTokens"`
	OutputTokens *int64    `json:"outputTokens"`
	CostMicros   *int64    `json:"costMicros"`
	LatencyMS    int       `json:"latencyMs"`
	FirstByteMS  *int      `json:"firstByteMs"`
	CreatedAt    time.Time `json:"createdAt"`
}

func (d *DB) UsageSummary(ctx context.Context, clientID string, from, to time.Time) ([]UsageSummaryRow, error) {
	// clientID 为空时 $3 为 NULL，条件恒真；非空时按账户过滤。
	var scope *string
	if clientID != "" {
		scope = &clientID
	}
	const q = `SELECT client_id, protocol,
        COUNT(*)::bigint,
        COUNT(*) FILTER (WHERE status >= 400)::bigint,
        COUNT(*) FILTER (WHERE stream)::bigint,
        COALESCE(AVG(latency_ms), 0)::bigint
      FROM usage_events
      WHERE created_at >= $1 AND created_at < $2
        AND ($3::text IS NULL OR client_id = $3)
      GROUP BY client_id, protocol
      ORDER BY client_id, protocol`
	rows, err := d.pool.Query(ctx, q, from, to, scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UsageSummaryRow{}
	for rows.Next() {
		var r UsageSummaryRow
		if err := rows.Scan(&r.ClientID, &r.Protocol, &r.Requests, &r.Errors, &r.Streams, &r.AvgLatencyMS); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (d *DB) RecentUsage(ctx context.Context, clientID string, limit int) ([]UsageDetailRow, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	var scope *string
	if clientID != "" {
		scope = &clientID
	}
	const q = `SELECT id, client_id, model, protocol, status, stream,
        input_tokens, output_tokens, cost_micros, latency_ms, first_byte_ms, created_at
      FROM usage_events
      WHERE ($2::text IS NULL OR client_id = $2)
      ORDER BY id DESC LIMIT $1`
	rows, err := d.pool.Query(ctx, q, limit, scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UsageDetailRow{}
	for rows.Next() {
		var r UsageDetailRow
		if err := rows.Scan(&r.ID, &r.ClientID, &r.Model, &r.Protocol, &r.Status, &r.Stream,
			&r.InputTokens, &r.OutputTokens, &r.CostMicros, &r.LatencyMS, &r.FirstByteMS, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Migrate 按文件名顺序执行 -- +goose Up 段落中的 SQL。
//
// 不使用自动迁移的原因：容器重启循环中反复执行迁移会放大故障。
// 此函数需由显式命令调用，不在进程启动时自动运行。
func (d *DB) Migrate(ctx context.Context, files map[string]string) ([]string, error) {
	if _, err := d.pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
        version TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		return nil, errors.New("cannot ensure schema_migrations table")
	}

	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)

	applied := []string{}
	for _, name := range names {
		var exists bool
		if err := d.pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = $1)`, name).Scan(&exists); err != nil {
			return applied, err
		}
		if exists {
			continue
		}
		up, err := extractUp(files[name])
		if err != nil {
			return applied, fmt.Errorf("%s: %w", name, err)
		}
		tx, err := d.pool.Begin(ctx)
		if err != nil {
			return applied, err
		}
		if _, err := tx.Exec(ctx, up); err != nil {
			_ = tx.Rollback(ctx)
			return applied, fmt.Errorf("%s: migration failed", name)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, name); err != nil {
			_ = tx.Rollback(ctx)
			return applied, fmt.Errorf("%s: cannot record migration", name)
		}
		if err := tx.Commit(ctx); err != nil {
			return applied, err
		}
		applied = append(applied, name)
	}
	return applied, nil
}

// extractUp 取出 goose 迁移文件里 Up 段落的 SQL（排除注释标记）。
func extractUp(content string) (string, error) {
	const upMark, downMark = "-- +goose Up", "-- +goose Down"
	start := strings.Index(content, upMark)
	if start < 0 {
		return "", errors.New("missing -- +goose Up marker")
	}
	body := content[start+len(upMark):]
	if end := strings.Index(body, downMark); end >= 0 {
		body = body[:end]
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return "", errors.New("Up section is empty")
	}
	return body, nil
}

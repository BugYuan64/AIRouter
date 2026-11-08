package store

import (
	"os"
	"testing"
)

// 集成测试只在显式设置 DSN 时运行，普通测试不依赖数据库。
// 运行方式：
//
//	AIROUTER_TEST_DATABASE_URL='postgres://airouter:devpass123@127.0.0.1:55432/airouter?sslmode=disable' go test ./internal/store -run TestPostgres -v
func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("AIROUTER_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("AIROUTER_TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}
	return dsn
}

func TestPostgresMigrateAndInsert(t *testing.T) {
	dsn := testDSN(t)
	ctx := t.Context()

	db, err := Open(ctx, dsn, PoolOptions{MaxConns: 4})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	files := map[string]string{
		"0001_init.sql": readMigration(t, "../../migrations/0001_init.sql"),
	}
	applied, err := db.Migrate(ctx, files)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Logf("applied migrations: %v", applied)

	// 重复执行应为空操作（幂等）。
	again, err := db.Migrate(ctx, files)
	if err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("second run applied %v; must be idempotent", again)
	}

	// 写入一条 token 未知的记录，验证 NULL 而非 0。
	inserter := db.UsageInserter()
	err = inserter.InsertUsage(ctx, []UsageEvent{{
		ClientID: "test-client", Model: "gpt-6.1-sol", Protocol: ProtocolChat,
		Status: 200, Committed: true, Stream: true, LatencyMS: 1234,
	}})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	var inputTokens, cost *int64
	var latency int
	err = db.pool.QueryRow(ctx,
		`SELECT input_tokens, cost_micros, latency_ms FROM usage_events
         WHERE client_id = 'test-client' ORDER BY id DESC LIMIT 1`).Scan(&inputTokens, &cost, &latency)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if inputTokens != nil || cost != nil {
		t.Fatalf("unknown token/cost must be NULL, got input=%v cost=%v", inputTokens, cost)
	}
	if latency != 1234 {
		t.Fatalf("want latency 1234, got %d", latency)
	}

	if _, err := db.pool.Exec(ctx, `DELETE FROM usage_events WHERE client_id = 'test-client'`); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
}

// 非法协议值必须在写入前被拒绝，而不是让 PostgreSQL 报约束错误。
func TestPostgresRejectsInvalidProtocol(t *testing.T) {
	dsn := testDSN(t)
	ctx := t.Context()
	db, err := Open(ctx, dsn, PoolOptions{MaxConns: 2})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	err = db.UsageInserter().InsertUsage(ctx, []UsageEvent{{
		ClientID: "c", Model: "m", Protocol: "bogus", Status: 200,
	}})
	if err == nil {
		t.Fatal("invalid protocol must be rejected")
	}
}

// 写入失败不能暴露 DSN 中的密码。
func TestOpenRejectsInvalidDSNWithoutLeaking(t *testing.T) {
	ctx := t.Context()
	_, err := Open(ctx, "postgres://user:secretpass@127.0.0.1:1/db?sslmode=disable", PoolOptions{MaxConns: 1})
	if err == nil {
		t.Skip("expected connection failure")
	}
	if msg := err.Error(); contains(msg, "secretpass") {
		t.Fatalf("error leaked password: %s", msg)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

func readMigration(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

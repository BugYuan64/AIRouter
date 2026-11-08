package store

import (
	"context"
	"os"
	"strings"
	"testing"
)

func setupSchema(t *testing.T) *DB {
	t.Helper()
	dsn := testDSN(t)
	ctx := t.Context()
	db, err := Open(ctx, dsn, PoolOptions{MaxConns: 4})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(db.Close)
	if _, err := db.Migrate(ctx, map[string]string{
		"0001_init.sql": readMigration(t, "../../migrations/0001_init.sql"),
	}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// 每个测试从干净状态开始
	if _, err := db.pool.Exec(ctx, `TRUNCATE clients, client_models, usage_events, audit_log, usage_daily RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return db
}

// 创建后明文 Key 只能拿到一次；列表接口永不返回它。
func TestCreateClientReturnsKeyOnce(t *testing.T) {
	db := setupSchema(t)
	ctx := t.Context()

	client, key, err := db.CreateClient(ctx, "alice", "Alice", []string{"m1", "m2"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.HasPrefix(key, "sk-ar-") {
		t.Fatalf("key format unexpected: %s", key)
	}
	if client.KeyPrefix != key[:KeyPrefixLen] {
		t.Fatalf("prefix mismatch: %s vs %s", client.KeyPrefix, key[:KeyPrefixLen])
	}

	listed, err := db.ListClients(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("want 1 client, got %d", len(listed))
	}
	// 列表结构体没有 Key 明文字段；确认前缀不足以还原
	if len(listed[0].KeyPrefix) >= len(key) {
		t.Fatal("prefix must not be the full key")
	}
	if len(listed[0].Models) != 2 {
		t.Fatalf("want 2 models, got %v", listed[0].Models)
	}
}

func TestCreateClientRejectsDuplicate(t *testing.T) {
	db := setupSchema(t)
	ctx := t.Context()
	if _, _, err := db.CreateClient(ctx, "dup", "", []string{"m"}); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if _, _, err := db.CreateClient(ctx, "dup", "", []string{"m"}); err != ErrClientExists {
		t.Fatalf("want ErrClientExists, got %v", err)
	}
}

// 认证查询按摘要进行，吊销后立即返回 nil。
func TestLookupAndRevoke(t *testing.T) {
	db := setupSchema(t)
	ctx := t.Context()
	_, key, err := db.CreateClient(ctx, "bob", "", []string{"m1"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	digest := HashKey(key)

	got, err := db.LookupByDigest(ctx, digest)
	if err != nil || got == nil {
		t.Fatalf("active key must resolve: %v", err)
	}
	if got.ID != "bob" || len(got.Models) != 1 {
		t.Fatalf("unexpected account: %+v", got)
	}

	if err := db.RevokeClient(ctx, "bob"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	// 撤权必须立即生效，不能因缓存继续放行
	after, err := db.LookupByDigest(ctx, digest)
	if err != nil {
		t.Fatalf("lookup after revoke: %v", err)
	}
	if after != nil {
		t.Fatal("revoked key must not authenticate")
	}
}

// 吊销保留记录与用量归属，不做物理删除。
func TestRevokeKeepsRecord(t *testing.T) {
	db := setupSchema(t)
	ctx := t.Context()
	_, _, err := db.CreateClient(ctx, "carol", "", []string{"m"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.RevokeClient(ctx, "carol"); err != nil {
		t.Fatal(err)
	}
	listed, err := db.ListClients(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Status != "revoked" || listed[0].RevokedAt == nil {
		t.Fatalf("revoked client must remain listed: %+v", listed)
	}
}

func TestRevokeUnknownClient(t *testing.T) {
	db := setupSchema(t)
	if err := db.RevokeClient(t.Context(), "nobody"); err != ErrClientNotFound {
		t.Fatalf("want ErrClientNotFound, got %v", err)
	}
}

// 旧 clients.json 只有摘要，迁移必须保留原摘要使现有客户端无需换 Key。
func TestMigrateLegacyClientsPreservesDigest(t *testing.T) {
	db := setupSchema(t)
	ctx := t.Context()

	legacy := []LegacyClient{{
		ID: "legacy-user", KeySHA256: HashKey("existing-secret"), AllowedModels: []string{"m1", "m2"},
	}}
	inserted, skipped, err := db.MigrateClients(ctx, legacy)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if inserted != 1 || skipped != 0 {
		t.Fatalf("want inserted=1 skipped=0, got %d/%d", inserted, skipped)
	}

	// 原 Key 仍可认证
	got, err := db.LookupByDigest(ctx, HashKey("existing-secret"))
	if err != nil || got == nil {
		t.Fatalf("legacy key must still authenticate: %v", err)
	}
	if got.ID != "legacy-user" || len(got.Models) != 2 {
		t.Fatalf("unexpected: %+v", got)
	}
	if got.KeyPrefix != "legacy" {
		t.Fatalf("legacy prefix expected, got %q", got.KeyPrefix)
	}
	if got.IsAdmin {
		t.Fatal("is_admin should default to false when not specified")
	}

	// 重复导入幂等
	again, skipped2, err := db.MigrateClients(ctx, legacy)
	if err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if again != 0 || skipped2 != 1 {
		t.Fatalf("want inserted=0 skipped=1, got %d/%d", again, skipped2)
	}
}

// 迁移产生审计记录，便于追溯账户来源。
func TestMigrateLegacyWritesAudit(t *testing.T) {
	db := setupSchema(t)
	ctx := t.Context()
	if _, _, err := db.MigrateClients(ctx, []LegacyClient{{ID: "u1", KeySHA256: HashKey("k"), AllowedModels: []string{"m"}}}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.pool.QueryRow(ctx, `SELECT COUNT(*) FROM audit_log WHERE action='client.migrate'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("want 1 audit row, got %d", count)
	}
}

// 认证失败不应因数据库不可用而放行；这条在 access 层测试，此处验证
// store 在未知摘要时返回 nil 而不是错误。
func TestLookupUnknownDigestReturnsNil(t *testing.T) {
	db := setupSchema(t)
	got, err := db.LookupByDigest(t.Context(), strings.Repeat("0", 64))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatal("unknown digest must return nil")
	}
}

var _ = context.Background
var _ = os.Getenv

// 旧文件的 is_admin 必须被保留，否则管理员用量会归到普通账户。
func TestMigrateLegacyPreservesAdminFlag(t *testing.T) {
	db := setupSchema(t)
	ctx := t.Context()
	_, _, err := db.MigrateClients(ctx, []LegacyClient{{
		ID: "owner", KeySHA256: HashKey("owner-secret"), AllowedModels: []string{"m"}, IsAdmin: true,
	}})
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	got, err := db.LookupByDigest(ctx, HashKey("owner-secret"))
	if err != nil || got == nil {
		t.Fatalf("lookup: %v", err)
	}
	if !got.IsAdmin {
		t.Fatal("is_admin must be preserved through migration")
	}
}

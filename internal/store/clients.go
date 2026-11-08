package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Client 是一个调用者账户。
type Client struct {
	ID        string     `json:"id"`
	KeyPrefix string     `json:"keyPrefix"`
	Label     string     `json:"label"`
	IsAdmin   bool       `json:"isAdmin"`
	Status    string     `json:"status"`
	Models    []string   `json:"models"`
	CreatedAt time.Time  `json:"createdAt"`
	RevokedAt *time.Time `json:"revokedAt"`
}

// ClientStore 管理调用者与其模型权限。
type ClientStore interface {
	ListClients(ctx context.Context) ([]Client, error)
	// CreateClient 生成新 Key，明文只在此次返回值中给出一次。
	CreateClient(ctx context.Context, id, label string, models []string) (Client, string, error)
	RevokeClient(ctx context.Context, id string) error
	// LookupByDigest 在认证时按摘要查账户；吊销的账户返回 nil。
	LookupByDigest(ctx context.Context, digest string) (*Client, error)
}

var (
	ErrClientExists   = errors.New("client already exists")
	ErrClientNotFound = errors.New("client not found")
)

// KeyPrefixLen 是展示用前缀长度。前缀不足以还原原始 Key。
const KeyPrefixLen = 11

// HashKey 返回网关 Key 的 SHA256 十六进制摘要。
func HashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// generateKey 生成形如 sk-ar-<32字节 base64url> 的随机 Key。
func generateKey() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "sk-ar-" + base64.RawURLEncoding.EncodeToString(buf), nil
}

func keyPrefix(key string) string {
	if len(key) <= KeyPrefixLen {
		return key
	}
	return key[:KeyPrefixLen]
}

func (d *DB) ListClients(ctx context.Context) ([]Client, error) {
	const q = `SELECT c.id, c.key_prefix, c.label, c.is_admin, c.status, c.created_at, c.revoked_at,
        COALESCE(array_agg(m.model_id ORDER BY m.model_id) FILTER (WHERE m.model_id IS NOT NULL), '{}')
      FROM clients c LEFT JOIN client_models m ON m.client_id = c.id
      GROUP BY c.id ORDER BY c.created_at`
	rows, err := d.pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Client{}
	for rows.Next() {
		var c Client
		if err := rows.Scan(&c.ID, &c.KeyPrefix, &c.Label, &c.IsAdmin, &c.Status, &c.CreatedAt, &c.RevokedAt, &c.Models); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (d *DB) CreateClient(ctx context.Context, id, label string, models []string) (Client, string, error) {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(id) != id {
		return Client{}, "", errors.New("client id must be nonempty without surrounding space")
	}
	key, err := generateKey()
	if err != nil {
		return Client{}, "", err
	}
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return Client{}, "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var c Client
	err = tx.QueryRow(ctx, `INSERT INTO clients (id, key_sha256, key_prefix, label)
        VALUES ($1,$2,$3,$4)
        ON CONFLICT (id) DO NOTHING
        RETURNING id, key_prefix, label, is_admin, status, created_at`,
		id, HashKey(key), keyPrefix(key), label,
	).Scan(&c.ID, &c.KeyPrefix, &c.Label, &c.IsAdmin, &c.Status, &c.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Client{}, "", ErrClientExists
	}
	if err != nil {
		return Client{}, "", err
	}
	for _, m := range models {
		if _, err := tx.Exec(ctx, `INSERT INTO client_models (client_id, model_id) VALUES ($1,$2)`, id, m); err != nil {
			return Client{}, "", err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO audit_log (actor, action, subject, detail) VALUES ($1,$2,$3,$4)`,
		"admin", "client.create", id, "models="+strings.Join(models, ",")); err != nil {
		return Client{}, "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return Client{}, "", err
	}
	c.Models = models
	return c, key, nil
}

// RevokeClient 标记吊销而不删除，保留用量归属。
func (d *DB) RevokeClient(ctx context.Context, id string) error {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `UPDATE clients SET status='revoked', revoked_at=now()
        WHERE id=$1 AND status='active'`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrClientNotFound
	}
	if _, err := tx.Exec(ctx, `INSERT INTO audit_log (actor, action, subject) VALUES ('admin','client.revoke',$1)`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// LookupByDigest 返回有效账户；吊销后返回 nil，使撤权立即生效。
func (d *DB) LookupByDigest(ctx context.Context, digest string) (*Client, error) {
	var c Client
	err := d.pool.QueryRow(ctx, `SELECT id, key_prefix, label, is_admin, status, created_at
        FROM clients WHERE key_sha256=$1 AND status='active'`, digest).
		Scan(&c.ID, &c.KeyPrefix, &c.Label, &c.IsAdmin, &c.Status, &c.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := d.pool.Query(ctx, `SELECT model_id FROM client_models WHERE client_id=$1 ORDER BY model_id`, c.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	c.Models = []string{}
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		c.Models = append(c.Models, m)
	}
	return &c, rows.Err()
}

// MigrateClients 把旧 clients.json 导入数据库。
//
// 旧文件只存摘要而没有原始 Key，因此导入时保留原摘要、不生成新 Key：
// 现有客户端无需换 Key 即可继续使用。key_prefix 记为 "legacy"，
// 因为无法从摘要还原前缀。
//
// 幂等：已存在的 id 跳过，不覆盖数据库中可能已更新的状态。
func (d *DB) MigrateClients(ctx context.Context, clients []LegacyClient) (inserted int, skipped int, err error) {
	for _, lc := range clients {
		tx, beginErr := d.pool.Begin(ctx)
		if beginErr != nil {
			return inserted, skipped, beginErr
		}
		tag, execErr := tx.Exec(ctx, `INSERT INTO clients (id, key_sha256, key_prefix, label, is_admin, status)
            VALUES ($1,$2,'legacy',$3,$4,'active')
            ON CONFLICT (id) DO NOTHING`, lc.ID, lc.KeySHA256, lc.ID, lc.IsAdmin)
		if execErr != nil {
			_ = tx.Rollback(ctx)
			return inserted, skipped, execErr
		}
		if tag.RowsAffected() == 0 {
			_ = tx.Rollback(ctx)
			skipped++
			continue
		}
		for _, m := range lc.AllowedModels {
			if _, execErr := tx.Exec(ctx, `INSERT INTO client_models (client_id, model_id) VALUES ($1,$2)
                ON CONFLICT DO NOTHING`, lc.ID, m); execErr != nil {
				_ = tx.Rollback(ctx)
				return inserted, skipped, execErr
			}
		}
		if _, execErr := tx.Exec(ctx, `INSERT INTO audit_log (actor, action, subject, detail)
            VALUES ('system','client.migrate',$1,'from clients.json')`, lc.ID); execErr != nil {
			_ = tx.Rollback(ctx)
			return inserted, skipped, execErr
		}
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return inserted, skipped, commitErr
		}
		inserted++
	}
	return inserted, skipped, nil
}

// LegacyClient 是旧 clients.json 的一条记录。
type LegacyClient struct {
	ID            string
	KeySHA256     string
	AllowedModels []string
	// IsAdmin 来自旧文件的 is_admin 字段；缺失时为 false。
	IsAdmin bool
}

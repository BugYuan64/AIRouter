package access

import (
	"airouter/internal/config"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"strings"
)

type Principal struct {
	ID      string
	IsAdmin bool
	allowed map[string]struct{}
}

func (p Principal) Allows(model string) bool { _, ok := p.allowed[model]; return ok }

type entry struct {
	digest    []byte
	principal Principal
}
type Authenticator struct{ entries []entry }

func New(clients []config.Client) (*Authenticator, error) {
	if err := config.ValidateClients(clients, ""); err != nil {
		return nil, err
	}
	a := &Authenticator{}
	for _, c := range clients {
		digest, _ := hex.DecodeString(c.KeySHA256)
		p := Principal{ID: c.ID, IsAdmin: c.IsAdmin, allowed: map[string]struct{}{}}
		for _, model := range c.AllowedModels {
			p.allowed[model] = struct{}{}
		}
		a.entries = append(a.entries, entry{digest, p})
	}
	return a, nil
}

func (a *Authenticator) Authenticate(authorization string) (Principal, bool) {
	parts := strings.Fields(authorization)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || strings.Contains(parts[1], ",") {
		return Principal{}, false
	}
	digest := sha256.Sum256([]byte(parts[1]))
	found := -1
	for i, e := range a.entries {
		if subtle.ConstantTimeCompare(digest[:], e.digest) == 1 {
			found = i
		}
	}
	if found < 0 {
		return Principal{}, false
	}
	return a.entries[found].principal, true
}

// Store 是按摘要查询账户的最小接口，由数据库实现。
// 定义在 access 而不是 store，避免 access 依赖具体存储。
type Store interface {
	// Lookup 返回原始 Key 的 SHA256 摘要对应的账户；未知或已吊销返回 nil。
	Lookup(digest string) (*Account, error)
}

// Account 是认证所需的账户信息。
type Account struct {
	ID      string
	IsAdmin bool
	Models  []string
}

// Dynamic 用数据库作为认证来源，支持运行时新增与吊销 Key。
//
// 与静态 Authenticator 的取舍：
//   - 静态版本无每请求 I/O，但修改需重启；
//   - 动态版本每请求查库（有索引，微秒级），换来实现即时撤权。
//
// 认证失败时不区分「Key 不存在」与「已吊销」，避免泄露账户状态。
type Dynamic struct {
	store Store
}

func NewDynamic(store Store) (*Dynamic, error) {
	if store == nil {
		return nil, errors.New("authentication store is required")
	}
	return &Dynamic{store: store}, nil
}

func (d *Dynamic) Authenticate(authorization string) (Principal, bool) {
	parts := strings.Fields(authorization)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || strings.Contains(parts[1], ",") {
		return Principal{}, false
	}
	sum := sha256.Sum256([]byte(parts[1]))
	account, err := d.store.Lookup(hex.EncodeToString(sum[:]))
	if err != nil || account == nil {
		return Principal{}, false
	}
	allowed := make(map[string]struct{}, len(account.Models))
	for _, m := range account.Models {
		allowed[m] = struct{}{}
	}
	return Principal{ID: account.ID, IsAdmin: account.IsAdmin, allowed: allowed}, true
}

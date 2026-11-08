package access

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
)

type fakeStore struct {
	accounts map[string]*Account
	err      error
}

func (f *fakeStore) Lookup(digest string) (*Account, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.accounts[digest], nil
}

func digestOf(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func TestDynamicAuthenticatesKnownKey(t *testing.T) {
	store := &fakeStore{accounts: map[string]*Account{
		digestOf("good-key"): {ID: "alice", Models: []string{"m1", "m2"}},
	}}
	a, err := NewDynamic(store)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := a.Authenticate("Bearer good-key")
	if !ok {
		t.Fatal("known key must authenticate")
	}
	if p.ID != "alice" || !p.Allows("m1") || p.Allows("m3") {
		t.Fatalf("principal mismatch: %+v", p)
	}
}

func TestDynamicRejectsUnknownKey(t *testing.T) {
	store := &fakeStore{accounts: map[string]*Account{}}
	a, _ := NewDynamic(store)
	if _, ok := a.Authenticate("Bearer unknown"); ok {
		t.Fatal("unknown key must be rejected")
	}
}

// 吊销由 store 返回 nil 表示；不能把 nil 当成错误放行。
func TestDynamicRejectedWhenStoreReturnsNil(t *testing.T) {
	a, _ := NewDynamic(&fakeStore{accounts: map[string]*Account{}})
	if _, ok := a.Authenticate("Bearer revoked"); ok {
		t.Fatal("nil account must be rejected")
	}
}

// 数据库错误时拒绝认证，不能 fail-open。
func TestDynamicFailsClosedOnStoreError(t *testing.T) {
	a, _ := NewDynamic(&fakeStore{err: errors.New("db down")})
	if _, ok := a.Authenticate("Bearer any"); ok {
		t.Fatal("store error must fail closed, never authenticate")
	}
}

// 格式不合法的认证头一律拒绝。
func TestDynamicRejectsMalformedAuthorization(t *testing.T) {
	store := &fakeStore{accounts: map[string]*Account{digestOf("k"): {ID: "a"}}}
	a, _ := NewDynamic(store)
	for _, header := range []string{"", "k", "Basic k", "Bearer", "Bearer a b", "Bearer k,other"} {
		if _, ok := a.Authenticate(header); ok {
			t.Errorf("malformed header %q must be rejected", header)
		}
	}
}

func TestNewDynamicRequiresStore(t *testing.T) {
	if _, err := NewDynamic(nil); err == nil {
		t.Fatal("nil store must be rejected")
	}
}

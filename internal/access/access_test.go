package access

import (
	"airouter/internal/config"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func record(id, key string, models ...string) config.Client {
	s := sha256.Sum256([]byte(key))
	return config.Client{ID: id, KeySHA256: hex.EncodeToString(s[:]), AllowedModels: models}
}

func TestAuthenticateAndIsolatePermissions(t *testing.T) {
	a, err := New([]config.Client{record("alice", "alice-key", "model-a"), record("bob", "bob-key", "model-b")})
	if err != nil {
		t.Fatal(err)
	}
	p, ok := a.Authenticate("Bearer alice-key")
	if !ok || p.ID != "alice" || !p.Allows("model-a") || p.Allows("model-b") {
		t.Fatal("wrong alice identity or permissions")
	}
	q, ok := a.Authenticate("bearer bob-key")
	if !ok || q.ID != "bob" || !q.Allows("model-b") || q.Allows("model-a") {
		t.Fatal("wrong bob identity or permissions")
	}
	for _, bad := range []string{"", "Bearer wrong", "Basic alice-key", "Bearer alice-key extra", "Bearer", "Bearer alice-key, Bearer bob-key"} {
		if _, ok := a.Authenticate(bad); ok {
			t.Fatalf("accepted invalid authorization %q", bad)
		}
	}
}

func TestNewRejectsInvalidAndDuplicateRecords(t *testing.T) {
	for _, list := range [][]config.Client{nil, {{ID: "a", KeySHA256: "bad"}}, {record("a", "a"), record("a", "b")}, {record("a", "a"), record("b", "a")}} {
		if _, err := New(list); err == nil {
			t.Fatal("accepted invalid records")
		}
	}
}

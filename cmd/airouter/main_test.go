package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"airouter/internal/config"
)

func TestRunStartsRealListenerAndShutsDown(t *testing.T) {
	sum := sha256.Sum256([]byte("test-client"))
	raw, _ := json.Marshal([]config.Client{{ID: "local", KeySHA256: hex.EncodeToString(sum[:]), AllowedModels: []string{"m"}}})
	path := filepath.Join(t.TempDir(), "clients.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"AIROUTER_LISTEN_ADDR": "127.0.0.1:0", "CPA_BASE_URL": "http://127.0.0.1:1", "CPA_API_KEY": "test-upstream", "AIROUTER_CLIENTS_FILE": path} {
		t.Setenv(k, v)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan net.Addr, 1)
	done := make(chan error, 1)
	go func() { done <- run(ctx, func(addr net.Addr) { started <- addr }) }()
	var addr net.Addr
	select {
	case addr = <-started:
	case err := <-done:
		t.Fatal(err)
	case <-time.After(2 * time.Second):
		t.Fatal("did not start")
	}
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Get("http://" + addr.String() + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal("not healthy")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("did not shut down")
	}
}

func TestRunRejectsInvalidConfiguration(t *testing.T) {
	t.Setenv("CPA_BASE_URL", "")
	if err := run(context.Background(), func(net.Addr) { t.Error("listened with invalid config") }); err == nil {
		t.Fatal("accepted invalid config")
	}
}

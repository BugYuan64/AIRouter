package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T) (map[string]string, []byte) {
	t.Helper()
	sum := sha256.Sum256([]byte("gateway-test-key"))
	body, _ := json.Marshal([]Client{{ID: "alice", KeySHA256: hex.EncodeToString(sum[:]), AllowedModels: []string{"model-a"}}})
	return map[string]string{"CPA_BASE_URL": "http://127.0.0.1:8317", "CPA_API_KEY": "upstream-test-key", "AIROUTER_CLIENTS_FILE": "clients.json"}, body
}

func loadFixture(env map[string]string, body []byte) (Config, error) {
	return Load(func(k string) string { return env[k] }, func(string) ([]byte, error) { return body, nil })
}

func TestLoadDefaultsAndSeparateTimeouts(t *testing.T) {
	env, body := fixture(t)
	c, err := loadFixture(env, body)
	if err != nil {
		t.Fatal(err)
	}
	if c.ListenAddr != "127.0.0.1:8080" || c.UpstreamTimeout != 10*time.Second || c.RequestTimeout != 10*time.Minute || c.MaxRequestBytes != 16<<20 {
		t.Fatalf("unexpected defaults: listen=%s timeouts=%v/%v max=%d", c.ListenAddr, c.UpstreamTimeout, c.RequestTimeout, c.MaxRequestBytes)
	}
	if len(c.Clients) != 1 || c.Clients[0].AllowedModels[0] != "model-a" {
		t.Fatal("lost explicit permissions")
	}
}

func TestLoadRejectsInvalidSettingsWithoutLeakingValues(t *testing.T) {
	for _, tc := range []struct{ name, key, value string }{
		{"missing target", "CPA_BASE_URL", ""}, {"remote cleartext", "CPA_BASE_URL", "http://example.com"},
		{"userinfo", "CPA_BASE_URL", "https://user:private-value@example.com"}, {"query", "CPA_BASE_URL", "https://example.com?token=private-value"},
		{"fragment", "CPA_BASE_URL", "https://example.com/#private-value"}, {"path", "CPA_BASE_URL", "https://example.com/v1"},
		{"missing key", "CPA_API_KEY", ""}, {"missing clients", "AIROUTER_CLIENTS_FILE", ""},
		{"zero timeout", "AIROUTER_UPSTREAM_TIMEOUT", "0s"}, {"negative request timeout", "AIROUTER_REQUEST_TIMEOUT", "-1s"},
		{"bad limit", "AIROUTER_MAX_REQUEST_BYTES", "0"}, {"bad listen", "AIROUTER_LISTEN_ADDR", "missing-port"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, body := fixture(t)
			env[tc.key] = tc.value
			_, err := loadFixture(env, body)
			if err == nil {
				t.Fatal("accepted invalid configuration")
			}
			if strings.Contains(err.Error(), "private-value") || strings.Contains(err.Error(), "upstream-test-key") {
				t.Fatal("leaked configuration")
			}
		})
	}
}

func TestLoadValidatesPermissionRecords(t *testing.T) {
	env, body := fixture(t)
	var clients []Client
	_ = json.Unmarshal(body, &clients)
	for _, name := range []string{"bad digest", "duplicate id", "duplicate digest", "empty model", "duplicate model", "upstream key reuse", "empty clients", "unknown field"} {
		t.Run(name, func(t *testing.T) {
			list := append([]Client(nil), clients...)
			list[0].AllowedModels = append([]string(nil), clients[0].AllowedModels...)
			switch name {
			case "bad digest":
				list[0].KeySHA256 = "private-value"
			case "duplicate id":
				s := sha256.Sum256([]byte("other"))
				x := list[0]
				x.KeySHA256 = hex.EncodeToString(s[:])
				list = append(list, x)
			case "duplicate digest":
				x := list[0]
				x.ID = "bob"
				list = append(list, x)
			case "empty model":
				list[0].AllowedModels = []string{""}
			case "duplicate model":
				list[0].AllowedModels = []string{"a", "a"}
			case "upstream key reuse":
				s := sha256.Sum256([]byte(env["CPA_API_KEY"]))
				list[0].KeySHA256 = hex.EncodeToString(s[:])
			case "empty clients":
				list = nil
			}
			raw, _ := json.Marshal(list)
			if name == "unknown field" {
				raw = []byte(`[{"id":"alice","key_sha256":"bad","allowed_models":[],"allow_all":true}]`)
			}
			_, err := loadFixture(env, raw)
			if err == nil {
				t.Fatal("accepted invalid permissions")
			}
			if strings.Contains(err.Error(), "private-value") {
				t.Fatal("leaked digest")
			}
		})
	}
}

func TestLoadAllowsExplicitEmptyPermissionsAndIPv6Loopback(t *testing.T) {
	env, body := fixture(t)
	env["CPA_BASE_URL"] = "http://[::1]:8317/"
	var clients []Client
	_ = json.Unmarshal(body, &clients)
	clients[0].AllowedModels = []string{}
	body, _ = json.Marshal(clients)
	if _, err := loadFixture(env, body); err != nil {
		t.Fatal(err)
	}
	_, err := Load(func(k string) string { return env[k] }, func(string) ([]byte, error) { return nil, errors.New("private-file-content") })
	if err == nil || strings.Contains(err.Error(), "private-file-content") {
		t.Fatal("must sanitize file read failure")
	}
}

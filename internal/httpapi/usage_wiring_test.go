package httpapi

import (
	"airouter/internal/access"
	"airouter/internal/config"
	"airouter/internal/cpa"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"airouter/internal/store"
)

// captureRecorder 记录传出的用量事件。
type captureRecorder struct {
	mu     sync.Mutex
	events []store.UsageEvent
}

func (c *captureRecorder) Record(e store.UsageEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, e)
}

func (c *captureRecorder) all() []store.UsageEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]store.UsageEvent, len(c.events))
	copy(out, c.events)
	return out
}

func gatewayWithUsage(t *testing.T, up http.Handler) (http.Handler, *captureRecorder) {
	t.Helper()
	server := httptest.NewServer(up)
	t.Cleanup(server.Close)
	client, err := cpa.New(server.URL, "cpa-key", 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)

	key := sha256.Sum256([]byte("alice-key"))
	auth, err := access.New([]config.Client{{
		ID: "alice", KeySHA256: hex.EncodeToString(key[:]), AllowedModels: []string{"exact-model"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	rec := &captureRecorder{}
	return New(auth, client, Options{
		MaxRequestBytes: 4096, RequestTimeout: 2 * time.Second, Usage: rec,
	}), rec
}

// 成功转发的用量记录必须带调用者身份、模型、协议与状态。
func TestUsageRecordedForSuccessfulForward(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"model":"exact-model","choices":[]}`))
	})
	h, rec := gatewayWithUsage(t, upstream)

	w := perform(h, "POST", "/v1/chat/completions", `{"model":"exact-model","messages":[]}`, "alice-key")
	if w.Code != 200 {
		t.Fatalf("want 200, got %d", w.Code)
	}

	events := rec.all()
	if len(events) != 1 {
		t.Fatalf("want 1 usage event, got %d", len(events))
	}
	e := events[0]
	if e.ClientID != "alice" {
		t.Fatalf("client_id must come from authentication, got %q", e.ClientID)
	}
	if e.Model != "exact-model" {
		t.Fatalf("want exact-model, got %q", e.Model)
	}
	if e.Protocol != store.ProtocolChat {
		t.Fatalf("want chat, got %q", e.Protocol)
	}
	if e.Status != 200 || !e.Committed {
		t.Fatalf("want 200 committed, got %d committed=%v", e.Status, e.Committed)
	}
	// token 与成本尚未解析，必须为 nil
	if e.InputTokens != nil || e.OutputTokens != nil || e.CostMicros != nil {
		t.Fatal("unparsed token/cost must stay nil, not zero")
	}
}

// 三种协议都要映射到正确的枚举，否则数据库 CHECK 会拒绝写入。
func TestUsageProtocolMapping(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	})
	cases := map[string]store.Protocol{
		"/v1/chat/completions":      store.ProtocolChat,
		"/v1/responses":             store.ProtocolResponses,
		"/v1/messages":              store.ProtocolMessages,
		"/v1/messages/count_tokens": store.ProtocolCountTokens,
	}
	for path, want := range cases {
		h, rec := gatewayWithUsage(t, upstream)
		var body string
		switch path {
		case "/v1/responses":
			body = `{"model":"exact-model","input":"hi","store":false}`
		case "/v1/messages/count_tokens":
			body = `{"model":"exact-model","messages":[]}`
		default:
			body = `{"model":"exact-model","messages":[]}`
		}
		w := perform(h, "POST", path, body, "alice-key")
		if w.Code != 200 {
			t.Fatalf("%s: want 200, got %d body=%s", path, w.Code, w.Body.String())
		}
		events := rec.all()
		if len(events) != 1 {
			t.Fatalf("%s: want 1 event, got %d", path, len(events))
		}
		if events[0].Protocol != want {
			t.Errorf("%s: want protocol %q, got %q", path, want, events[0].Protocol)
		}
	}
}

// 认证失败或权限拒绝不得产生用量记录：没有可信的调用者身份。
func TestNoUsageRecordedBeforeAuthorization(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	})
	h, rec := gatewayWithUsage(t, upstream)

	perform(h, "POST", "/v1/chat/completions", `{"model":"exact-model","messages":[]}`, "")        // 无 Key
	perform(h, "POST", "/v1/chat/completions", `{"model":"forbidden","messages":[]}`, "alice-key") // 越权模型

	if events := rec.all(); len(events) != 0 {
		t.Fatalf("rejected requests must not be recorded, got %d events", len(events))
	}
}

// 流式请求要标记 stream，并记录首字节时间。
func TestUsageRecordsStreamFlagAndFirstByte(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = w.Write([]byte("data: {\"model\":\"exact-model\"}\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	})
	h, rec := gatewayWithUsage(t, upstream)

	w := perform(h, "POST", "/v1/chat/completions", `{"model":"exact-model","messages":[],"stream":true}`, "alice-key")
	if w.Code != 200 {
		t.Fatalf("want 200, got %d", w.Code)
	}
	events := rec.all()
	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}
	if !events[0].Stream {
		t.Fatal("stream flag must be recorded")
	}
	if events[0].FirstByteMS == nil {
		t.Fatal("first byte time must be recorded for streaming")
	}
}

// 非流式请求不应记录首字节时间：正文一次性到达，
// 「首字节」等于总耗时，记录它会产生误导性指标。
func TestUsageOmitsFirstByteForNonStream(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"model":"exact-model"}`))
	})
	h, rec := gatewayWithUsage(t, upstream)

	perform(h, "POST", "/v1/chat/completions", `{"model":"exact-model","messages":[]}`, "alice-key")
	events := rec.all()
	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}
	if events[0].Stream {
		t.Fatal("non-stream request must not be flagged as stream")
	}
	if events[0].FirstByteMS != nil {
		t.Fatalf("non-stream first byte must be nil, got %d", *events[0].FirstByteMS)
	}
}

// 上游失败时仍要记录，但状态码必须真实反映失败，不能记成 200。
func TestUsageRecordsUpstreamFailure(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	})
	h, rec := gatewayWithUsage(t, upstream)

	perform(h, "POST", "/v1/chat/completions", `{"model":"exact-model","messages":[]}`, "alice-key")

	events := rec.all()
	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}
	if events[0].Status < 500 {
		t.Fatalf("failure must be recorded with 5xx, got %d", events[0].Status)
	}
}

// 用量记录失败不能影响响应：Recorder 的 Record 是非阻塞的空操作时也要正常转发。
func TestForwardingWorksWithoutRecorder(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	server := httptest.NewServer(upstream)
	defer server.Close()
	client, err := cpa.New(server.URL, "cpa-key", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	key := sha256.Sum256([]byte("alice-key"))
	auth, _ := access.New([]config.Client{{ID: "alice", KeySHA256: hex.EncodeToString(key[:]), AllowedModels: []string{"exact-model"}}})
	h := New(auth, client, Options{MaxRequestBytes: 4096, RequestTimeout: time.Second})

	w := perform(h, "POST", "/v1/chat/completions", `{"model":"exact-model","messages":[]}`, "alice-key")
	if w.Code != 200 {
		t.Fatalf("want 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "ok") {
		t.Fatal("body must pass through unchanged")
	}
}

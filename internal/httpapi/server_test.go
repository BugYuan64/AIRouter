package httpapi

import (
	"airouter/internal/access"
	"airouter/internal/config"
	"airouter/internal/cpa"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func gateway(t *testing.T, up http.Handler) (http.Handler, *atomic.Int32) {
	t.Helper()
	calls := &atomic.Int32{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); up.ServeHTTP(w, r) }))
	t.Cleanup(server.Close)
	client, err := cpa.New(server.URL, "cpa-key", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	akey := sha256.Sum256([]byte("alice-key"))
	bkey := sha256.Sum256([]byte("bob-key"))
	auth, err := access.New([]config.Client{{ID: "alice", KeySHA256: hex.EncodeToString(akey[:]), AllowedModels: []string{"exact-model"}}, {ID: "bob", KeySHA256: hex.EncodeToString(bkey[:]), AllowedModels: []string{"other-model"}}})
	if err != nil {
		t.Fatal(err)
	}
	return New(auth, client, Options{MaxRequestBytes: 4096, RequestTimeout: 2 * time.Second}), calls
}

func perform(h http.Handler, method, path, body, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

func TestNativeRequestsKeepExactModelBodyAndProtocol(t *testing.T) {
	fixtures := map[string]string{
		"/v1/chat/completions":      `{ "model":"exact-model", "messages":[{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}],"reasoning_effort":"low","custom":1 }`,
		"/v1/responses":             `{"model":"exact-model","input":[{"type":"function_call_output","call_id":"call-1","output":"result"}],"tools":[{"type":"function","name":"f"}],"reasoning":{"effort":"low"},"store":false}`,
		"/v1/messages":              `{"model":"exact-model","max_tokens":16,"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool-1","content":"result"}]}],"tools":[{"name":"f","input_schema":{"type":"object"}}]}`,
		"/v1/messages/count_tokens": `{"model":"exact-model","messages":[{"role":"user","content":"hello"}]}`,
	}
	for path, body := range fixtures {
		t.Run(path, func(t *testing.T) {
			h, calls := gateway(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got, _ := io.ReadAll(r.Body)
				if string(got) != body || r.URL.Path != path {
					t.Error("rewrote raw request")
				}
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"ok":true,"model":"exact-model"}`)
			}))
			w := perform(h, "POST", path, body, "alice-key")
			if w.Code != 200 || calls.Load() != 1 || w.Body.String() != `{"ok":true,"model":"exact-model"}` {
				t.Fatalf("status=%d calls=%d body=%s", w.Code, calls.Load(), w.Body.String())
			}
		})
	}
}

func TestAuthenticationAndModelPermissionsRunBeforeCPA(t *testing.T) {
	h, calls := gateway(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected CPA access") }))
	for _, tc := range []struct {
		name, key, body string
		status          int
	}{
		{"missing key", "", `{"model":"exact-model"}`, 401}, {"bad key", "wrong", `{"model":"exact-model"}`, 401},
		{"wrong model", "alice-key", `{"model":"other-model"}`, 403}, {"cross client", "bob-key", `{"model":"exact-model"}`, 403},
		{"missing model", "alice-key", `{"messages":[]}`, 400}, {"null model", "alice-key", `{"model":null}`, 400},
		{"malformed", "alice-key", `{"model":`, 400}, {"trailing JSON", "alice-key", `{"model":"exact-model"}{}`, 400},
		{"duplicate model", "alice-key", `{"model":"other-model","model":"exact-model"}`, 400},
		{"duplicate escaped model", "alice-key", `{"model":"other-model","\u006dodel":"exact-model"}`, 400},
		{"duplicate field", "alice-key", `{"model":"exact-model","stream":false,"stream":true}`, 400},
		{"oversized", "alice-key", `{"model":"exact-model","input":"` + strings.Repeat("x", 4096) + `"}`, 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := perform(h, "POST", "/v1/responses", tc.body, tc.key)
			if w.Code != tc.status {
				t.Fatalf("got %d want %d body=%s", w.Code, tc.status, w.Body.String())
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatal("rejected input reached CPA")
	}
}

func TestMessagesAcceptsAPIKeyButRejectsAmbiguousAuthentication(t *testing.T) {
	h, calls := gateway(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"type":"message"}`) }))
	req := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"exact-model"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", "alice-key")
	req.Header.Set("Anthropic-Version", "2023-06-01")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	req.Header.Set("Authorization", "Bearer alice-key")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatal("accepted ambiguous auth")
	}
	req.Header.Del("Authorization")
	req.Header.Add("X-Api-Key", "bob-key")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatal("accepted repeated API key")
	}
	req = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"exact-model"}`))
	req.Header.Set("X-Api-Key", "alice-key")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatal("accepted Messages key on unrelated protocol")
	}
	if calls.Load() != 1 {
		t.Fatal("ambiguous requests reached CPA")
	}
}

func TestRejectsUnsafeStateAndTransportFeatures(t *testing.T) {
	h, calls := gateway(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("unsupported feature reached CPA") }))
	for _, body := range []string{
		`{"model":"exact-model","previous_response_id":"resp-other"}`, `{"model":"exact-model","conversation":"conv-other"}`,
		`{"model":"exact-model","background":true}`, `{"model":"exact-model","store":true}`,
		`{"model":"exact-model","input":[{"type":"item_reference","id":"item-other"}]}`,
	} {
		w := perform(h, "POST", "/v1/responses", body, "alice-key")
		if w.Code != 501 {
			t.Fatalf("got %d for unsupported state: %s", w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/v1/responses/resp-other", "/v1/responses/resp-other/cancel"} {
		w := perform(h, "GET", path, "", "alice-key")
		if w.Code != 501 {
			t.Fatal("state route not explicitly rejected")
		}
	}
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"exact-model"}`))
	req.Header.Set("Authorization", "Bearer alice-key")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Upgrade", "websocket")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 501 {
		t.Fatal("accepted upgrade")
	}
	if calls.Load() != 0 {
		t.Fatal("unsupported features reached CPA")
	}
}

func TestRejectsMediaTypeEncodingAndUnsupportedMethod(t *testing.T) {
	h, calls := gateway(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("invalid transport reached CPA") }))
	for _, headers := range []map[string]string{{"Content-Type": "text/plain"}, {"Content-Type": "application/json", "Content-Encoding": "gzip"}} {
		r := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"exact-model"}`))
		r.Header.Set("Authorization", "Bearer alice-key")
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 415 {
			t.Fatal("accepted unsupported encoding")
		}
	}
	if w := perform(h, "GET", "/v1/chat/completions", "", "alice-key"); w.Code != 405 {
		t.Fatal("accepted wrong method")
	}
	if calls.Load() != 0 {
		t.Fatal("invalid transport reached CPA")
	}
}

func TestCatalogFiltersPerCallerAndLivenessDoesNotCallCPA(t *testing.T) {
	h, calls := gateway(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"object":"list","data":[{"id":"exact-model","secret":"private"},{"id":"other-model"},{"id":"new-unapproved-model"}]}`)
	}))
	for _, path := range []string{"/healthz", "/v1/models"} {
		w := perform(h, "GET", path, "", "")
		want := 200
		if path == "/v1/models" {
			want = 401
		}
		if w.Code != want {
			t.Fatal("wrong anonymous health or directory behavior")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("liveness or unauthorized request accessed CPA")
	}
	for _, tc := range []struct{ key, model string }{{"alice-key", "exact-model"}, {"bob-key", "other-model"}} {
		w := perform(h, "GET", "/v1/models", "", tc.key)
		var payload struct {
			Data []cpa.Model `json:"data"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &payload)
		if w.Code != 200 || len(payload.Data) != 1 || payload.Data[0].ID != tc.model || strings.Contains(w.Body.String(), "private") {
			t.Fatalf("wrong directory %s", w.Body.String())
		}
	}
	if w := perform(h, "GET", "/readyz", "", ""); w.Code != 200 {
		t.Fatal("readiness failed")
	}
}

func TestGatewayGenerationDeadlineIsNotDirectoryTimeout(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		select {
		case <-time.After(80 * time.Millisecond):
			io.WriteString(w, `{"ok":true}`)
		case <-r.Context().Done():
		}
	}))
	defer up.Close()
	client, err := cpa.New(up.URL, "cpa-key", 15*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	s := sha256.Sum256([]byte("key"))
	auth, _ := access.New([]config.Client{{ID: "one", KeySHA256: hex.EncodeToString(s[:]), AllowedModels: []string{"exact-model"}}})
	h := New(auth, client, Options{MaxRequestBytes: 4096, RequestTimeout: time.Second})
	if w := perform(h, "POST", "/v1/chat/completions", `{"model":"exact-model"}`, "key"); w.Code != 200 {
		t.Fatalf("directory timeout truncated generation: %d", w.Code)
	}
	h = New(auth, client, Options{MaxRequestBytes: 4096, RequestTimeout: 15 * time.Millisecond})
	if w := perform(h, "POST", "/v1/chat/completions", `{"model":"exact-model"}`, "key"); w.Code != 504 {
		t.Fatalf("generation deadline not enforced: %d", w.Code)
	}
}

func TestReadinessTimeoutDoesNotExposeCPA(t *testing.T) {
	h, _ := gateway(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		io.WriteString(w, "cpa-key private-url")
	}))
	w := perform(h, "GET", "/readyz", "", "")
	if w.Code != 503 || strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "cpa-key") {
		t.Fatal("invalid readiness failure")
	}
}

func TestRequestCancellationCrossesGateway(t *testing.T) {
	entered := make(chan struct{})
	canceled := make(chan struct{})
	h, calls := gateway(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		close(entered)
		<-r.Context().Done()
		close(canceled)
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"exact-model"}`)).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer alice-key")
	r.Header.Set("Content-Type", "application/json")
	done := make(chan struct{})
	go func() { defer close(done); h.ServeHTTP(httptest.NewRecorder(), r) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("CPA not entered")
	}
	cancel()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("CPA not canceled")
	}
	<-done
	if calls.Load() != 1 {
		t.Fatal("canceled request replayed")
	}
}

func TestRejectsNestedReferenceTypeShadowingAndDuplicateKeys(t *testing.T) {
	h, calls := gateway(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("reference reached CPA") }))
	for _, tc := range []struct {
		body   string
		status int
	}{
		{`{"model":"exact-model","input":[{"type":"item_reference","TYPE":"message","id":"item-other"}]}`, 501},
		{`{"model":"exact-model","input":[{"type":"item_reference","type":"message","id":"item-other"}]}`, 400},
		{`{"model":"exact-model","input":[{"id":"item-other"}]}`, 501},
	} {
		w := perform(h, "POST", "/v1/responses", tc.body, "alice-key")
		if w.Code != tc.status {
			t.Errorf("got %d want %d", w.Code, tc.status)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("unsafe reference sent to CPA")
	}
}

func TestRejectsProtocolServerResourcesButPreservesBusinessFields(t *testing.T) {
	h, calls := gateway(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"ok":true}`) }))
	for _, tc := range []struct{ path, body string }{
		{"/v1/chat/completions", `{"model":"exact-model","messages":[{"role":"user","content":[{"type":"file","file":{"file_id":"file-other"}}]}]}`},
		{"/v1/messages", `{"model":"exact-model","messages":[{"role":"user","content":[{"type":"document","source":{"type":"file","file_id":"file-other"}}]}]}`},
		{"/v1/responses", `{"model":"exact-model","input":[{"type":"message","role":"user","content":[{"type":"input_file","file_id":"file-other"}]}]}`},
		{"/v1/responses", `{"model":"exact-model","input":[{"type":"message","role":"user","content":[{"type":"input_image","file_id":"file-other"}]}]}`},
		{"/v1/responses", `{"model":"exact-model","tools":[{"type":"file_search","vector_store_ids":["vs-other"]}]}`},
		{"/v1/responses", `{"model":"exact-model","tools":[{"type":"code_interpreter","container":"cntr-other"}]}`},
		{"/v1/responses", `{"model":"exact-model","tools":[{"type":"code_interpreter","container":{"type":"auto","file_ids":["file-other"]}}]}`},
	} {
		w := perform(h, "POST", tc.path, tc.body, "alice-key")
		if w.Code != 501 {
			t.Errorf("resource not rejected for %s: %d", tc.path, w.Code)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("server resource was forwarded")
	}
	// A valid schema containing the ordinary business property file_id must survive.
	allowed := `{"model":"exact-model","messages":[{"role":"user","content":[{"type":"file","file":{"file_data":"data:text/plain;base64,b2s=","filename":"a.txt"}}]}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object","properties":{"file_id":{"type":"string"}}}}}]}`
	w := perform(h, "POST", "/v1/chat/completions", allowed, "alice-key")
	if w.Code != 200 || calls.Load() != 1 {
		t.Fatalf("blocked inline data or ordinary schema field: %d", w.Code)
	}
}

func TestChunkedRequestTrailersNeverReachCPA(t *testing.T) {
	h, calls := gateway(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("trailer request reached CPA") }))
	server := httptest.NewServer(h)
	defer server.Close()
	r, _ := http.NewRequest("POST", server.URL+"/v1/chat/completions", io.NopCloser(strings.NewReader(`{"model":"exact-model"}`)))
	r.Header.Set("Authorization", "Bearer alice-key")
	r.Header.Set("Content-Type", "application/json")
	r.Trailer = http.Header{"Cookie": []string{"client-cookie"}, "Authorization": []string{"client-secret"}, "Forwarded": []string{"spoofed"}}
	response, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	io.Copy(io.Discard, response.Body)
	if response.StatusCode != 400 || calls.Load() != 0 {
		t.Fatalf("trailer request accepted: status=%d calls=%d", response.StatusCode, calls.Load())
	}
}

func TestGenerationDeadlineUnblocksNonReadingDownstream(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		chunk := []byte(strings.Repeat("x", 64<<10))
		for i := 0; i < 4096; i++ {
			if _, err := w.Write(chunk); err != nil {
				return
			}
			w.(http.Flusher).Flush()
		}
	}))
	defer up.Close()
	client, err := cpa.New(up.URL, "cpa-key", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	hash := sha256.Sum256([]byte("key"))
	auth, _ := access.New([]config.Client{{ID: "a", KeySHA256: hex.EncodeToString(hash[:]), AllowedModels: []string{"exact-model"}}})
	handler := New(auth, client, Options{RequestTimeout: 100 * time.Millisecond, MaxRequestBytes: 4096})
	done := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { defer close(done); handler.ServeHTTP(w, r) }))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	conn, err := net.Dial("tcp", target.Host)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetReadBuffer(1024)
	}
	body := `{"model":"exact-model","stream":true}`
	_, err = fmt.Fprintf(conn, "POST /v1/chat/completions HTTP/1.1\r\nHost: localhost\r\nAuthorization: Bearer key\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("generation deadline left a blocked downstream writer")
	}
}

func TestOpaqueValidJSONNumbersRemainUnchanged(t *testing.T) {
	body := `{"model":"exact-model","extension":{"large_number":1e1000}}`
	h, _ := gateway(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if string(raw) != body {
			t.Error("opaque number changed")
		}
		io.WriteString(w, `{"ok":true}`)
	}))
	if w := perform(h, "POST", "/v1/chat/completions", body, "alice-key"); w.Code != 200 {
		t.Fatalf("opaque valid JSON number rejected: %d", w.Code)
	}
}

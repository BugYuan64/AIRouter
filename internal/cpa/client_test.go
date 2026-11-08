package cpa

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/textproto"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newClient(t *testing.T, upstream *httptest.Server) *Client {
	t.Helper()
	c, err := New(upstream.URL, "cpa-test-secret", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	return c
}

func TestListSanitizesCatalogAndUsesOnlyCPAAuthentication(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Method != "GET" || r.Header.Get("Authorization") != "Bearer cpa-test-secret" {
			t.Error("wrong upstream directory request")
		}
		io.WriteString(w, `{"object":"list","data":[{"id":"z","object":"model","owned_by":"provider","secret":"private"},{"id":"a","created":123}]}`)
	}))
	defer up.Close()
	c := newClient(t, up)
	models, err := c.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].ID != "a" || models[1].OwnedBy != "provider" {
		t.Fatal("incorrect catalog")
	}
}

func TestListRejectsInvalidCatalogsAndDoesNotLeakBody(t *testing.T) {
	for _, body := range []string{`not-json-private`, `{"data":null}`, `{"data":[{"id":""}]}`, `{"data":[{"id":"a"},{"id":"a"}]}`, `{"data":[{"id":2}]}`, `{"data":[]} {}`, strings.Repeat("x", (1<<20)+1)} {
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) }))
		c := newClient(t, up)
		_, err := c.List(context.Background())
		if err == nil || strings.Contains(err.Error(), "private") {
			t.Fatal("invalid catalog accepted or leaked")
		}
		up.Close()
	}
}

func TestListRejectsRedirectWithoutSendingKeyToAnotherHost(t *testing.T) {
	var stolen atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { stolen.Add(1) }))
	defer other.Close()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, 302) }))
	defer up.Close()
	c := newClient(t, up)
	if _, err := c.List(context.Background()); err == nil {
		t.Fatal("accepted redirect")
	}
	if stolen.Load() != 0 {
		t.Fatal("followed redirect")
	}
}

func TestListUsesContextTimeoutAndNoRetry(t *testing.T) {
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); <-r.Context().Done() }))
	defer up.Close()
	c := newClient(t, up)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := c.List(ctx)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("expected sanitized timeout, got %v", err)
	}
	if calls.Load() != 1 {
		t.Fatal("retried directory")
	}
}

func TestForwardPreservesNativeProtocolAndReplacesSecrets(t *testing.T) {
	for _, path := range []string{"/v1/chat/completions", "/v1/responses", "/v1/messages", "/v1/messages/count_tokens"} {
		t.Run(path, func(t *testing.T) {
			body := []byte("{\n \"model\":\"exact-model\", \"tools\":[{\"type\":\"custom\"}],\"extension\":{\"keep\":true}}")
			response := `{"id":"reply","model":"exact-model","usage":{"input_tokens":1},"custom":"keep"}`
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got, _ := io.ReadAll(r.Body)
				if !bytes.Equal(body, got) || r.URL.Path != path || r.Method != "POST" || r.Host == "untrusted.example" {
					t.Error("request body, path or host changed incorrectly")
				}
				if r.Header.Get("Authorization") != "Bearer cpa-test-secret" {
					t.Error("upstream key not injected")
				}
				for _, h := range []string{"Cookie", "X-Api-Key", "X-Goog-Api-Key", "Proxy-Authorization", "Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Real-IP"} {
					if r.Header.Get(h) != "" {
						t.Errorf("forwarded sensitive header %s", h)
					}
				}
				if r.Header.Get("Anthropic-Version") != "2023-06-01" || r.Header.Get("Openai-Beta") != "responses=v1" || r.Header.Get("Session_id") != "client-session" {
					t.Error("lost protocol headers")
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Set-Cookie", "private-cookie")
				io.WriteString(w, response)
			}))
			defer up.Close()
			c := newClient(t, up)
			req := httptest.NewRequest("POST", path, bytes.NewReader(body))
			req.Host = "untrusted.example"
			for _, h := range []string{"Authorization", "Cookie", "X-Api-Key", "X-Goog-Api-Key", "Proxy-Authorization", "Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Real-IP"} {
				req.Header.Set(h, "client-private")
			}
			req.Header.Set("Connection", "Authorization, X-Api-Key")
			req.Header.Set("Anthropic-Version", "2023-06-01")
			req.Header.Set("Openai-Beta", "responses=v1")
			req.Header.Set("Session_id", "client-session")
			w := httptest.NewRecorder()
			c.Forward(w, req)
			if w.Code != 200 || w.Body.String() != response || w.Header().Get("Set-Cookie") != "" {
				t.Fatalf("unexpected response status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}

func TestForwardDoesNotRetryOrExposeUpstreamErrors(t *testing.T) {
	for _, status := range []int{302, 400, 401, 403, 429, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Location", "https://private.example")
				w.Header().Set("Retry-After", "7")
				w.WriteHeader(status)
				io.WriteString(w, "cpa-test-secret private")
			}))
			defer up.Close()
			c := newClient(t, up)
			w := httptest.NewRecorder()
			c.Forward(w, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(`{"model":"m"}`)))
			want := status
			if status < 400 || status == 401 || status == 403 {
				want = 502
			}
			if w.Code != want || calls.Load() != 1 || strings.Contains(w.Body.String(), "private") || strings.Contains(w.Body.String(), "cpa-test-secret") || w.Header().Get("Location") != "" {
				t.Fatalf("unsafe error: status %d attempts %d body %s", w.Code, calls.Load(), w.Body.String())
			}
			if status == 429 && w.Header().Get("Retry-After") != "7" {
				t.Fatal("lost retry-after")
			}
		})
	}
}

func TestForwardFlushesSSEBeforeUpstreamCompletes(t *testing.T) {
	release := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		<-release
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer func() { close(release); up.Close() }()
	c := newClient(t, up)
	gateway := httptest.NewServer(http.HandlerFunc(c.Forward))
	defer gateway.Close()
	result := make(chan error, 1)
	go func() {
		resp, err := http.Post(gateway.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{"model":"m","stream":true}`))
		if err != nil {
			result <- err
			return
		}
		defer resp.Body.Close()
		line, err := bufio.NewReader(resp.Body).ReadString('\n')
		if err == nil && line != "data: first\n" {
			err = fmt.Errorf("unexpected first chunk %q", line)
		}
		result <- err
	}()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SSE was buffered until completion")
	}
}

func TestForwardPropagatesCancellationWithoutRetry(t *testing.T) {
	entered := make(chan struct{})
	canceled := make(chan struct{})
	var calls atomic.Int32
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		calls.Add(1)
		close(entered)
		<-r.Context().Done()
		close(canceled)
	}))
	defer up.Close()
	c := newClient(t, up)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(`{"model":"m"}`)).WithContext(ctx)
		c.Forward(httptest.NewRecorder(), req)
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("upstream not entered")
	}
	cancel()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("cancel not propagated")
	}
	<-done
	if calls.Load() != 1 {
		t.Fatal("retried canceled generation")
	}
}

func TestForwardClearsRequestTrailers(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		if len(r.Trailer) != 0 {
			t.Error("forwarded caller trailers")
		}
		io.WriteString(w, `{"ok":true}`)
	}))
	defer up.Close()
	client := newClient(t, up)
	req := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
	req.Trailer = http.Header{"Cookie": []string{"private"}, "Authorization": []string{"private"}}
	req.TransferEncoding = []string{"chunked"}
	req.ContentLength = -1
	recorder := httptest.NewRecorder()
	client.Forward(recorder, req)
	if recorder.Code != 200 {
		t.Fatal("forward failed")
	}
}

func TestForwardDoesNotExposeResponseTrailerCredentials(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Trailer", "X-Api-Key")
		io.WriteString(w, `{"ok":true}`)
		w.Header().Set("X-Api-Key", "cpa-test-secret")
	}))
	defer up.Close()
	c := newClient(t, up)
	gw := httptest.NewServer(http.HandlerFunc(c.Forward))
	defer gw.Close()
	resp, err := http.Post(gw.URL+"/v1/responses", "application/json", strings.NewReader(`{"model":"m"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if len(resp.Trailer) != 0 {
		t.Fatal("upstream credentials escaped in response trailers")
	}
}

func TestForwardScrubsInformationalResponseCredentials(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Api-Key", "cpa-test-secret")
		w.Header().Set("Set-Cookie", "private")
		w.WriteHeader(103)
		w.Header().Del("X-Api-Key")
		w.Header().Del("Set-Cookie")
		io.WriteString(w, `{"ok":true}`)
	}))
	defer up.Close()
	c := newClient(t, up)
	gw := httptest.NewServer(http.HandlerFunc(c.Forward))
	defer gw.Close()
	req, _ := http.NewRequest("POST", gw.URL+"/v1/responses", strings.NewReader(`{"model":"m"}`))
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{Got1xxResponse: func(code int, h textproto.MIMEHeader) error {
		if h.Get("X-Api-Key") != "" || h.Get("Set-Cookie") != "" {
			t.Error("informational response exposed credential headers")
		}
		return nil
	}}))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

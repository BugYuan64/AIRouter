package httpapi

// Explicitly opt-in. Normal test runs never contact a real model provider.
import (
	"airouter/internal/access"
	"airouter/internal/config"
	"airouter/internal/cpa"
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLiveCPA(t *testing.T) {
	base, key, model := os.Getenv("AIROUTER_LIVE_CPA_URL"), os.Getenv("AIROUTER_LIVE_CPA_KEY"), os.Getenv("AIROUTER_LIVE_MODEL")
	if base == "" || key == "" || model == "" {
		t.Skip("live inference disabled; use the explicit SSH smoke verifier")
	}
	upstream, err := cpa.New(base, key, 10*time.Second)
	if err != nil {
		t.Fatal("invalid live CPA setup")
	}
	defer upstream.Close()
	secret := make([]byte, 32)
	if _, err = rand.Read(secret); err != nil {
		t.Fatal("cannot generate test credential")
	}
	gatewayKey := hex.EncodeToString(secret)
	sum := sha256.Sum256([]byte(gatewayKey))
	auth, err := access.New([]config.Client{{ID: "live-smoke", KeySHA256: hex.EncodeToString(sum[:]), AllowedModels: []string{model}}})
	if err != nil {
		t.Fatal("invalid live permissions")
	}
	handler := New(auth, upstream, Options{MaxRequestBytes: 16 << 20, RequestTimeout: 120 * time.Second})
	gateway := httptest.NewServer(handler)
	defer gateway.Close()
	for _, protocol := range []string{"chat", "responses", "messages"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", protocol, stream), func(t *testing.T) {
				path, body := liveRequest(protocol, model, stream)
				ctx, cancel := context.WithTimeout(context.Background(), 125*time.Second)
				defer cancel()
				request, _ := http.NewRequestWithContext(ctx, "POST", gateway.URL+path, bytes.NewReader(body))
				request.Header.Set("Content-Type", "application/json")
				if protocol == "messages" {
					request.Header.Set("X-Api-Key", gatewayKey)
					request.Header.Set("Anthropic-Version", "2023-06-01")
				} else {
					request.Header.Set("Authorization", "Bearer "+gatewayKey)
				}
				started := time.Now()
				response, err := http.DefaultClient.Do(request)
				record := map[string]any{"protocol": protocol, "stream": stream, "requested_model": model, "passed": false}
				defer func() {
					record["elapsed_ms"] = time.Since(started).Milliseconds()
					encoded, _ := json.Marshal(record)
					t.Log("AIRouter_LIVE " + string(encoded))
				}()
				if err != nil {
					record["failure"] = "transport"
					t.Error("live gateway request failed")
					return
				}
				defer response.Body.Close()
				record["status"] = response.StatusCode
				if response.StatusCode != 200 {
					io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
					record["failure"] = "http_status"
					t.Error("live CPA did not return HTTP 200")
					return
				}
				var text, reported string
				if stream {
					if !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
						record["failure"] = "content_type"
						t.Error("expected SSE")
						return
					}
					var completed bool
					text, reported, completed, err = readLiveSSE(response.Body, protocol)
					record["terminal_event"] = completed
					if err != nil || !completed {
						record["failure"] = "incomplete_stream"
						t.Error("live stream did not complete")
						return
					}
				} else {
					raw, e := io.ReadAll(io.LimitReader(response.Body, 1<<20))
					if e != nil {
						record["failure"] = "read"
						t.Error("live response incomplete")
						return
					}
					var payload map[string]any
					if json.Unmarshal(raw, &payload) != nil {
						record["failure"] = "json"
						t.Error("live response was not JSON")
						return
					}
					text, reported = liveText(payload, protocol)
				}
				record["reported_model"] = reported
				record["output_present"] = strings.TrimSpace(text) != ""
				if !strings.Contains(strings.ToUpper(text), "OK") {
					record["failure"] = "missing_output_marker"
					t.Error("live response did not include the requested output marker")
					return
				}
				record["passed"] = true
			})
		}
	}
}

func liveRequest(protocol, model string, stream bool) (string, []byte) {
	prompt := "Reply with only OK."
	body := map[string]any{"model": model, "stream": stream}
	path := "/v1/chat/completions"
	switch protocol {
	case "chat":
		body["messages"] = []any{map[string]any{"role": "user", "content": prompt}}
		body["max_completion_tokens"] = 256
		body["reasoning_effort"] = "low"
	case "responses":
		path = "/v1/responses"
		body["input"] = prompt
		body["store"] = false
		body["max_output_tokens"] = 256
		body["reasoning"] = map[string]any{"effort": "low"}
	case "messages":
		path = "/v1/messages"
		body["messages"] = []any{map[string]any{"role": "user", "content": prompt}}
		body["max_tokens"] = 256
	}
	raw, _ := json.Marshal(body)
	return path, raw
}

func liveText(p map[string]any, protocol string) (string, string) {
	model, _ := p["model"].(string)
	switch protocol {
	case "chat":
		choices, _ := p["choices"].([]any)
		if len(choices) > 0 {
			c, _ := choices[0].(map[string]any)
			m, _ := c["message"].(map[string]any)
			text, _ := m["content"].(string)
			return text, model
		}
	case "messages":
		var text strings.Builder
		parts, _ := p["content"].([]any)
		for _, part := range parts {
			m, _ := part.(map[string]any)
			s, _ := m["text"].(string)
			text.WriteString(s)
		}
		return text.String(), model
	case "responses":
		var text strings.Builder
		output, _ := p["output"].([]any)
		for _, item := range output {
			m, _ := item.(map[string]any)
			parts, _ := m["content"].([]any)
			for _, part := range parts {
				v, _ := part.(map[string]any)
				s, _ := v["text"].(string)
				text.WriteString(s)
			}
		}
		return text.String(), model
	}
	return "", model
}

func readLiveSSE(reader io.Reader, protocol string) (string, string, bool, error) {
	scanner := bufio.NewScanner(io.LimitReader(reader, 1<<20))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var text strings.Builder
	var model string
	completed := false
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			if protocol == "chat" {
				completed = true
			}
			continue
		}
		var event map[string]any
		if json.Unmarshal([]byte(data), &event) != nil {
			continue
		}
		if m, ok := event["model"].(string); ok {
			model = m
		}
		typ, _ := event["type"].(string)
		switch protocol {
		case "chat":
			choices, _ := event["choices"].([]any)
			if len(choices) > 0 {
				c, _ := choices[0].(map[string]any)
				delta, _ := c["delta"].(map[string]any)
				s, _ := delta["content"].(string)
				text.WriteString(s)
			}
		case "responses":
			if typ == "response.output_text.delta" {
				s, _ := event["delta"].(string)
				text.WriteString(s)
			}
			if response, ok := event["response"].(map[string]any); ok {
				if m, ok := response["model"].(string); ok {
					model = m
				}
			}
			if typ == "response.completed" {
				completed = true
			}
		case "messages":
			if typ == "message_start" {
				m, _ := event["message"].(map[string]any)
				model, _ = m["model"].(string)
			}
			if typ == "content_block_delta" {
				d, _ := event["delta"].(map[string]any)
				s, _ := d["text"].(string)
				text.WriteString(s)
			}
			if typ == "message_stop" {
				completed = true
			}
		}
	}
	return text.String(), model, completed, scanner.Err()
}

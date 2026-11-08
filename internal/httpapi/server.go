package httpapi

import (
	"airouter/internal/access"
	"airouter/internal/cpa"
	"airouter/internal/store"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Options struct {
	MaxRequestBytes int64
	RequestTimeout  time.Duration
	// Usage 接收每次生成调用的用量元数据。为 nil 时不记录。
	// 实现方必须保证 Record 非阻塞，否则会拖慢生成路径。
	Usage UsageRecorder
}

// UsageRecorder 是用量记录的抽象，避免 httpapi 依赖具体存储实现。
type UsageRecorder interface {
	Record(event store.UsageEvent)
}

// Authenticator 抽象调用者认证，静态文件与数据库实现都满足。
type Authenticator interface {
	Authenticate(authorization string) (access.Principal, bool)
}

type server struct {
	auth      Authenticator
	cpa       *cpa.Client
	options   Options
	readiness chan struct{}
}

func New(auth Authenticator, client *cpa.Client, options Options) http.Handler {
	if options.MaxRequestBytes <= 0 {
		options.MaxRequestBytes = 16 << 20
	}
	if options.RequestTimeout <= 0 {
		options.RequestTimeout = 10 * time.Minute
	}
	return &server{auth: auth, cpa: client, options: options, readiness: make(chan struct{}, 1)}
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path == "/healthz" || path == "/readyz" {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			w.WriteHeader(405)
			return
		}
		if path == "/healthz" {
			writeJSON(w, 200, map[string]string{"status": "ok"})
			return
		}
		s.ready(w, r)
		return
	}
	p, ok := s.authenticate(r)
	if !ok {
		cpa.WriteError(w, path, 401, "authentication_error")
		return
	}
	if strings.HasPrefix(path, "/v1/responses/") || r.Header.Get("Upgrade") != "" {
		cpa.WriteError(w, path, 501, "unsupported_feature")
		return
	}
	if path == "/v1/models" {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			w.WriteHeader(405)
			return
		}
		models, err := s.cpa.List(r.Context())
		if err != nil {
			status := 502
			code := "upstream_unavailable"
			if errors.Is(err, cpa.ErrTimeout) {
				status = 504
				code = "upstream_timeout"
			}
			cpa.WriteError(w, path, status, code)
			return
		}
		allowed := make([]cpa.Model, 0, len(models))
		for _, m := range models {
			if p.Allows(m.ID) {
				allowed = append(allowed, m)
			}
		}
		writeJSON(w, 200, map[string]any{"object": "list", "data": allowed})
		return
	}
	switch path {
	case "/v1/chat/completions", "/v1/responses", "/v1/messages", "/v1/messages/count_tokens":
	default:
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(405)
		return
	}
	if len(r.Trailer) != 0 {
		cpa.WriteError(w, path, 400, "invalid_request")
		return
	}
	if r.URL.EscapedPath() != path {
		cpa.WriteError(w, path, 400, "invalid_request")
		return
	}
	if _, err := url.ParseQuery(r.URL.RawQuery); err != nil {
		cpa.WriteError(w, path, 400, "invalid_request")
		return
	}
	typ, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	encoding := r.Header.Get("Content-Encoding")
	if err != nil || typ != "application/json" || (encoding != "" && encoding != "identity") {
		cpa.WriteError(w, path, 415, "unsupported_media_type")
		return
	}
	defer r.Body.Close()
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.options.MaxRequestBytes))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			cpa.WriteError(w, path, 413, "request_too_large")
		} else {
			cpa.WriteError(w, path, 400, "invalid_request")
		}
		return
	}
	fields, err := parseObject(raw)
	if err != nil {
		cpa.WriteError(w, path, 400, "invalid_request")
		return
	}
	var model string
	if json.Unmarshal(fields["model"], &model) != nil || model == "" {
		cpa.WriteError(w, path, 400, "invalid_request")
		return
	}
	if !p.Allows(model) {
		cpa.WriteError(w, path, 403, "permission_denied")
		return
	}
	if hasServerResources(fields) {
		cpa.WriteError(w, path, 501, "unsupported_feature")
		return
	}
	if path == "/v1/responses" {
		status := validateResponses(fields)
		if status != 0 {
			code := "unsupported_feature"
			if status == 400 {
				code = "invalid_request"
			}
			cpa.WriteError(w, path, status, code)
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.options.RequestTimeout)
	defer cancel()
	controller := http.NewResponseController(w)
	deadline, _ := ctx.Deadline()
	if err := controller.SetWriteDeadline(deadline); err != nil && !errors.Is(err, http.ErrNotSupported) {
		cpa.WriteError(w, path, 500, "internal_error")
		return
	}
	defer controller.SetWriteDeadline(time.Time{})
	forwarded := r.Clone(ctx)
	forwarded.Body = io.NopCloser(bytes.NewReader(raw))
	forwarded.ContentLength = int64(len(raw))
	forwarded.GetBody = nil
	forwarded.Trailer = nil
	forwarded.TransferEncoding = nil

	// 用量观测：包装 ResponseWriter 只记录状态与时间，不缓冲字节。
	// 包装必须在 SetWriteDeadline 之后建立，但 Unwrap 保证了控制器仍能穿透。
	stream, _ := fields["stream"]
	isStream := bytes.Equal(bytes.TrimSpace(stream), []byte("true"))
	started := time.Now()
	observed := &usageWriter{ResponseWriter: w}
	s.cpa.Forward(observed, forwarded)

	if s.options.Usage != nil {
		status := observed.status
		if !observed.wroteHeader {
			// 未写响应头通常意味着客户端提前断开或转发未产生响应。
			status = 499
		}
		var firstByte *int
		// 首字节时间只对流式有意义：非流式请求的正文一次性到达，
		// 「首字节」等于总耗时，记录它会产生误导性指标。
		if isStream && !observed.firstByte.IsZero() {
			ms := int(observed.firstByte.Sub(started).Milliseconds())
			firstByte = &ms
		}
		s.options.Usage.Record(store.UsageEvent{
			ClientID:    p.ID,
			Model:       model,
			Protocol:    protocolFor(path),
			Status:      status,
			Committed:   observed.committed,
			Stream:      isStream,
			LatencyMS:   int(time.Since(started).Milliseconds()),
			FirstByteMS: firstByte,
			CreatedAt:   started,
			// token 与成本尚未从上游响应解析，保持 nil（未知），不记 0
		})
	}
}

// protocolFor 把请求路径映射为协议枚举，与数据库 CHECK 约束一致。
func protocolFor(path string) store.Protocol {
	switch path {
	case "/v1/chat/completions":
		return store.ProtocolChat
	case "/v1/responses":
		return store.ProtocolResponses
	case "/v1/messages":
		return store.ProtocolMessages
	case "/v1/messages/count_tokens":
		return store.ProtocolCountTokens
	}
	return store.ProtocolChat
}

func (s *server) authenticate(r *http.Request) (access.Principal, bool) {
	authorization := r.Header.Values("Authorization")
	apiKeys := r.Header.Values("X-Api-Key")
	if len(authorization) > 1 || len(apiKeys) > 1 || len(authorization) > 0 && len(apiKeys) > 0 {
		return access.Principal{}, false
	}
	if len(authorization) == 1 {
		return s.auth.Authenticate(authorization[0])
	}
	if len(apiKeys) == 1 && strings.HasPrefix(r.URL.Path, "/v1/messages") {
		return s.auth.Authenticate("Bearer " + apiKeys[0])
	}
	return access.Principal{}, false
}

func (s *server) ready(w http.ResponseWriter, r *http.Request) {
	select {
	case s.readiness <- struct{}{}:
		defer func() { <-s.readiness }()
	default:
		writeJSON(w, 503, map[string]string{"status": "unavailable"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if _, err := s.cpa.List(ctx); err != nil {
		writeJSON(w, 503, map[string]string{"status": "unavailable"})
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ready"})
}

// Parse only the top-level envelope, preserving the original bytes for CPA.
// Reject duplicates so gateway and upstream cannot disagree about model selection.
func parseObject(raw []byte) (map[string]json.RawMessage, error) {
	check := json.NewDecoder(bytes.NewReader(raw))
	check.UseNumber()
	if err := uniqueJSON(check, 0); err != nil {
		return nil, err
	}
	if err := check.Decode(new(any)); err != io.EOF {
		return nil, errors.New("trailing JSON")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("invalid envelope")
	}
	fields := map[string]json.RawMessage{}
	for dec.More() {
		token, err = dec.Token()
		if err != nil {
			return nil, err
		}
		name, ok := token.(string)
		if !ok {
			return nil, errors.New("invalid field")
		}
		if _, exists := fields[name]; exists {
			return nil, errors.New("duplicate field")
		}
		var value json.RawMessage
		if err = dec.Decode(&value); err != nil {
			return nil, err
		}
		fields[name] = value
	}
	if _, err = dec.Token(); err != nil {
		return nil, err
	}
	if err = dec.Decode(new(any)); err != io.EOF {
		return nil, errors.New("trailing JSON")
	}
	return fields, nil
}

func validateResponses(fields map[string]json.RawMessage) int {
	for _, name := range []string{"previous_response_id", "conversation"} {
		if raw, ok := fields[name]; ok && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return 501
		}
	}
	for _, name := range []string{"background", "store"} {
		if raw, ok := fields[name]; ok {
			var enabled bool
			if json.Unmarshal(raw, &enabled) != nil {
				return 400
			}
			if enabled {
				return 501
			}
		}
	}
	var items []map[string]json.RawMessage
	if raw, ok := fields["input"]; ok && len(bytes.TrimSpace(raw)) > 0 && bytes.TrimSpace(raw)[0] == '[' {
		if json.Unmarshal(raw, &items) != nil {
			return 400
		}
		for _, item := range items {
			if stringValue(item["type"]) == "item_reference" || (stringValue(item["type"]) == "" && nonempty(item["id"])) {
				return 501
			}
		}
	}
	return 0
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

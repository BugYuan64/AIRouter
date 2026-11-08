// Package cpa owns the fixed upstream origin and data-plane credentials.
package cpa

import (
	"airouter/internal/config"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	ErrUpstream = errors.New("CPA unavailable or returned an invalid response")
	ErrTimeout  = errors.New("CPA request timed out")
)

type Model struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created *int64 `json:"created,omitempty"`
	OwnedBy string `json:"owned_by,omitempty"`
}

type Client struct {
	directoryURL string
	key          string
	directory    *http.Client
	transport    *http.Transport
	proxy        *httputil.ReverseProxy
}

func New(base, key string, timeout time.Duration) (*Client, error) {
	target, err := config.ParseBaseURL(base)
	if err != nil {
		return nil, err
	}
	if key == "" || strings.ContainsAny(key, " \t\r\n") || timeout <= 0 {
		return nil, errors.New("invalid CPA credential or directory timeout")
	}
	transport := &http.Transport{
		Proxy: nil, DialContext: (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2: true, MaxIdleConns: 100, MaxIdleConnsPerHost: 20, IdleConnTimeout: 90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second, ExpectContinueTimeout: time.Second, DisableCompression: true,
	}
	c := &Client{directoryURL: target.String() + "/v1/models", key: key, transport: transport}
	c.directory = &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	c.proxy = &httputil.ReverseProxy{
		Transport: transport, FlushInterval: -1, ErrorLog: log.New(io.Discard, "", 0),
		Rewrite: func(p *httputil.ProxyRequest) {
			p.SetURL(target)
			// Never forward caller credentials or untrusted proxy provenance to CPA.
			for name := range p.Out.Header {
				lower := strings.ToLower(name)
				if lower == "authorization" || lower == "cookie" || lower == "x-api-key" || lower == "x-goog-api-key" || lower == "proxy-authorization" || lower == "forwarded" || lower == "x-real-ip" || strings.HasPrefix(lower, "x-forwarded-") {
					p.Out.Header.Del(name)
				}
			}
			p.Out.Header.Set("Authorization", "Bearer "+key)
			p.Out.GetBody = nil // Do not opt generation requests into transport replay.
			p.Out.Trailer = nil
			p.Out.TransferEncoding = nil
		},
		ModifyResponse: func(resp *http.Response) error {
			stripResponseHeaders(resp.Header)
			resp.Trailer = nil
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				resp.Body = &withoutTrailers{ReadCloser: resp.Body, response: resp}
				return nil
			}
			status := resp.StatusCode
			code := "upstream_error"
			if status < 400 || status > 599 {
				status = http.StatusBadGateway
			}
			if status == 401 || status == 403 {
				status = http.StatusBadGateway
				code = "upstream_authentication_error"
			}
			resp.Body.Close()
			body := errorBody(resp.Request.URL.Path, status, code)
			retryAfter := resp.Header.Get("Retry-After")
			resp.Header = make(http.Header)
			resp.Header.Set("Content-Type", "application/json")
			resp.Header.Set("Cache-Control", "no-store")
			if retryAfter != "" {
				resp.Header.Set("Retry-After", retryAfter)
			}
			resp.StatusCode = status
			resp.Status = http.StatusText(status)
			resp.Body = io.NopCloser(bytes.NewReader(body))
			resp.ContentLength = int64(len(body))
			resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			status := http.StatusBadGateway
			code := "upstream_unavailable"
			if errors.Is(r.Context().Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
				status = http.StatusGatewayTimeout
				code = "upstream_timeout"
			}
			WriteError(w, r.URL.Path, status, code)
		},
	}
	return c, nil
}

func (c *Client) Close() { c.transport.CloseIdleConnections() }
func (c *Client) Forward(w http.ResponseWriter, r *http.Request) {
	c.proxy.ServeHTTP(&responseHeaders{ResponseWriter: w}, r)
}

type responseHeaders struct{ http.ResponseWriter }

func (w *responseHeaders) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *responseHeaders) WriteHeader(status int) {
	stripResponseHeaders(w.Header())
	w.ResponseWriter.WriteHeader(status)
}

func stripResponseHeaders(headers http.Header) {
	for _, name := range []string{"Set-Cookie", "Location", "Alt-Svc", "Authorization", "X-Api-Key", "X-Goog-Api-Key", "Proxy-Authorization", "Trailer"} {
		headers.Del(name)
	}
}

type withoutTrailers struct {
	io.ReadCloser
	response *http.Response
}

func (b *withoutTrailers) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.response.Trailer = nil
	}
	return n, err
}
func (b *withoutTrailers) Close() error {
	defer func() { b.response.Trailer = nil }()
	return b.ReadCloser.Close()
}

func (c *Client) List(ctx context.Context) ([]Model, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.directoryURL, nil)
	if err != nil {
		return nil, ErrUpstream
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	resp, err := c.directory.Do(req)
	if err != nil {
		var netErr net.Error
		if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.As(err, &netErr) && netErr.Timeout() {
			return nil, ErrTimeout
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil, context.Canceled
		}
		return nil, ErrUpstream
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, ErrUpstream
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil {
		return nil, ErrUpstream
	}
	if len(raw) > 1<<20 {
		return nil, ErrUpstream
	}
	var result struct {
		Data []Model `json:"data"`
	}
	if json.Unmarshal(raw, &result) != nil || result.Data == nil {
		return nil, ErrUpstream
	}
	seen := map[string]bool{}
	for i := range result.Data {
		m := &result.Data[i]
		if m.ID == "" || strings.TrimSpace(m.ID) != m.ID || seen[m.ID] {
			return nil, ErrUpstream
		}
		seen[m.ID] = true
		if m.Object == "" {
			m.Object = "model"
		}
	}
	sort.Slice(result.Data, func(i, j int) bool { return result.Data[i].ID < result.Data[j].ID })
	return result.Data, nil
}

// WriteError uses each client's wire format without copying upstream error text.
func WriteError(w http.ResponseWriter, path string, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(errorBody(path, status, code))
}

func errorBody(path string, status int, code string) []byte {
	typ := "api_error"
	message := "The upstream request could not be completed."
	switch code {
	case "authentication_error":
		typ = "authentication_error"
		message = "Invalid gateway credentials."
	case "permission_denied":
		typ = "permission_error"
		message = "The requested model is not allowed."
	case "invalid_request":
		typ = "invalid_request_error"
		message = "A single valid JSON request with a model is required."
	case "request_too_large":
		typ = "invalid_request_error"
		message = "The request exceeds the configured size limit."
	case "unsupported_feature":
		typ = "invalid_request_error"
		message = "This stateful or transport feature is not supported by this gateway version."
	case "unsupported_media_type":
		typ = "invalid_request_error"
		message = "An uncompressed application/json request is required."
	case "upstream_timeout":
		message = "The upstream request exceeded its time limit."
	}
	detail := map[string]any{"type": typ, "message": message}
	var body any
	if strings.HasPrefix(path, "/v1/messages") {
		body = map[string]any{"type": "error", "error": detail}
	} else {
		detail["code"] = code
		body = map[string]any{"error": detail}
	}
	raw, _ := json.Marshal(body)
	return append(raw, '\n')
}

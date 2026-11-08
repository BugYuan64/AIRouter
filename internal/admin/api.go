package admin

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"airouter/internal/cpa"
	"airouter/internal/store"
)

// Catalog 是管理面读取模型目录所需的最小依赖。
// 不直接依赖 *cpa.Client，便于测试替身。
type Catalog interface {
	List(ctx context.Context) ([]cpa.Model, error)
}

// Options 组装管理 API。
type Options struct {
	// AdminKey 为管理面密钥原文；为空时管理 API 一律 503，
	// 避免未配置鉴权就暴露目录与用量。
	AdminKey string
	Catalog  Catalog
	// Usage 为用量查询来源；为 nil 时 /usage 返回 503，说明未配置数据库。
	Usage store.UsageReader
	// Clients 为账户管理来源；为 nil 时 /keys 返回 503。
	Clients store.ClientStore
	Version string
	// UpstreamTimeout 用于目录探测，默认 5 秒。
	UpstreamTimeout time.Duration
	// CatalogTTL 为目录缓存时长，默认 10 秒，避免每次状态轮询都打上游。
	CatalogTTL time.Duration
}

type apiServer struct {
	opts     Options
	mu       sync.Mutex
	cached   []cpa.Model
	cachedAt time.Time
	cachedOK bool
}

func newAPI(opts Options) http.Handler {
	if opts.UpstreamTimeout <= 0 {
		opts.UpstreamTimeout = 5 * time.Second
	}
	if opts.CatalogTTL <= 0 {
		opts.CatalogTTL = 10 * time.Second
	}
	s := &apiServer{opts: opts}
	mux := http.NewServeMux()
	mux.HandleFunc("/status", s.status)
	// 身份自省：前端据此渲染角色相关界面。
	// 角色来自认证结果，不接受客户端自报。
	mux.HandleFunc("GET /whoami", s.whoami)
	mux.HandleFunc("/models", s.models)
	// 用量与 Key 管理依赖尚未审核通过的持久化方案，明确返回 501，
	// 不用空数组伪装成"还没有数据"。
	mux.HandleFunc("GET /keys", s.listKeys)
	mux.HandleFunc("POST /keys", s.createKey)
	mux.HandleFunc("DELETE /keys/{id}", s.revokeKey)
	mux.HandleFunc("/usage", s.usage)
	mux.HandleFunc("/usage/recent", s.usageRecent)
	return s.withAuth(mux)
}

// session 是已认证的调用者身份，由 withAuth 注入请求上下文。
// 后续 handler 只能从 context 读取身份，绝不能使用请求参数中的 id。
type session struct {
	// Admin 为 true 时可见全部账户；否则只能操作自己的数据。
	Admin bool
	// ClientID 是账户标识；管理员的主密钥登录时为其自身账户（可能为空）。
	ClientID string
}

type sessionKey struct{}

func sessionFrom(ctx context.Context) (session, bool) {
	s, ok := ctx.Value(sessionKey{}).(session)
	return s, ok
}

// withAuth 接受两种凭据：
//   - 管理密钥：管理员身份，可见全部账户；
//   - 客户端网关 Key：只可见自己的数据。
//
// 两者都不匹配时统一返回 401，不区分「密钥不存在」与「角色不符」。
func (s *apiServer) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.opts.AdminKey == "" && s.opts.Clients == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"error": map[string]any{
					"type":    "admin_not_configured",
					"message": "Control plane is not configured.",
				},
			})
			return
		}
		header := r.Header.Get("Authorization")
		parts := strings.Fields(header)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || strings.Contains(parts[1], ",") {
			writeJSON(w, http.StatusUnauthorized, unauthorizedBody())
			return
		}
		token := parts[1]

		// 先比管理员密钥：常量时间比较，避免时序泄露
		if s.opts.AdminKey != "" {
			want := sha256.Sum256([]byte(s.opts.AdminKey))
			got := sha256.Sum256([]byte(token))
			if subtle.ConstantTimeCompare(want[:], got[:]) == 1 {
				next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessionKey{}, session{Admin: true})))
				return
			}
		}

		// 再查客户端 Key
		if s.opts.Clients != nil {
			if client, err := s.opts.Clients.LookupByDigest(r.Context(), store.HashKey(token)); err == nil && client != nil {
				ctx := context.WithValue(r.Context(), sessionKey{}, session{ClientID: client.ID})
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
		}
		writeJSON(w, http.StatusUnauthorized, unauthorizedBody())
	})
}

// requireAdmin 保护仅管理员可用的接口。
func requireAdmin(w http.ResponseWriter, ctx context.Context) (session, bool) {
	s, ok := sessionFrom(ctx)
	if !ok || !s.Admin {
		writeJSON(w, http.StatusForbidden, map[string]any{
			"error": map[string]any{
				"type":    "permission_denied",
				"message": "This operation requires administrator privileges.",
			},
		})
		return session{}, false
	}
	return s, true
}

// visibleClientID 返回本次请求可见的账户范围：
// 管理员返回空字符串（全部），客户端返回自己的 id。
func visibleClientID(s session) string {
	if s.Admin {
		return ""
	}
	return s.ClientID
}

func unauthorizedBody() map[string]any {
	return map[string]any{
		"error": map[string]any{
			"type":    "authentication_error",
			"message": "A valid admin key is required.",
		},
	}
}

// whoami 返回当前凭据的身份与能力。前端不得据此做安全决策，
// 真正的权限校验始终在服务端。
func (s *apiServer) whoami(w http.ResponseWriter, r *http.Request) {
	sess, ok := sessionFrom(r.Context())
	if !ok {
		writeJSON(w, http.StatusUnauthorized, unauthorizedBody())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"role":     map[bool]string{true: "admin", false: "client"}[sess.Admin],
		"clientId": sess.ClientID,
		"capabilities": map[string]bool{
			"viewAllUsage":   sess.Admin,
			"manageKeys":     sess.Admin,
			"createKeys":     sess.Admin,
			"revokeKeys":     sess.Admin,
			"viewAllClients": sess.Admin,
		},
	})
}

// catalog 返回目录，带短期缓存与并发合并，避免轮询打穿上游。
func (s *apiServer) catalog(ctx context.Context) ([]cpa.Model, error, bool) {
	s.mu.Lock()
	if time.Since(s.cachedAt) < s.opts.CatalogTTL {
		models, ok := s.cached, s.cachedOK
		s.mu.Unlock()
		return models, nil, ok
	}
	s.mu.Unlock()

	if s.opts.Catalog == nil {
		return nil, nil, false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	probeCtx, cancel := context.WithTimeout(ctx, s.opts.UpstreamTimeout)
	defer cancel()
	models, err := s.opts.Catalog.List(probeCtx)
	if err != nil {
		// 保留上一次成功的目录用于展示，但明确标记陈旧
		s.mu.Lock()
		stale := s.cached
		ok := s.cachedOK
		s.cachedOK = false
		s.mu.Unlock()
		return stale, err, ok
	}
	sorted := make([]cpa.Model, len(models))
	copy(sorted, models)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	s.mu.Lock()
	s.cached, s.cachedAt, s.cachedOK = sorted, time.Now(), true
	s.mu.Unlock()
	return sorted, nil, true
}

func (s *apiServer) status(w http.ResponseWriter, r *http.Request) {
	if !allowGet(w, r) {
		return
	}
	models, err, _ := s.catalog(r.Context())
	upstream := "ok"
	switch {
	case err != nil:
		upstream = "down"
	case len(models) == 0:
		upstream = "degraded"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version":        s.opts.Version,
		"upstream":       upstream,
		"gatewayRetries": 0,
		"catalogCount":   len(models),
		// 自动路由未启用，策略版本为 null 而不是伪造一个编号
		"activePolicy": nil,
		// 如实反映数据库是否可用，而不是写死值
		"storage": storageState(s.opts),
	})
}

func (s *apiServer) models(w http.ResponseWriter, r *http.Request) {
	if !allowGet(w, r) {
		return
	}
	models, err, ok := s.catalog(r.Context())
	if err != nil && !ok {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": map[string]any{
				"type":    "upstream_unavailable",
				"message": "The model catalog could not be read from CPA.",
			},
		})
		return
	}
	data := make([]map[string]any, 0, len(models))
	for _, m := range models {
		// 能力三态全部为 unknown：目录存在不等于能力已验证。
		// 不按名称推断，避免把"看起来像"当成证据。
		data = append(data, map[string]any{
			"id":            m.ID,
			"ownedBy":       m.OwnedBy,
			"chat":          "unknown",
			"responses":     "unknown",
			"messages":      "unknown",
			"tools":         "unknown",
			"contextWindow": nil,
			"verifiedAt":    nil,
		})
	}
	response := map[string]any{"data": data}
	if err != nil {
		// 上游失败但有陈旧缓存时如实标注
		response["stale"] = true
	}
	writeJSON(w, http.StatusOK, response)
}

// usage 返回指定时间范围的用量汇总。默认最近 30 天。
func (s *apiServer) usage(w http.ResponseWriter, r *http.Request) {
	if !allowGet(w, r) {
		return
	}
	if s.opts.Usage == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": map[string]any{
				"type":    "storage_not_configured",
				"message": "AIROUTER_DATABASE_URL is not set; usage is not recorded.",
			},
		})
		return
	}
	sess, _ := sessionFrom(r.Context())
	from, to := usageRange(r)
	// 范围来自已认证身份，不是请求参数
	rows, err := s.opts.Usage.UsageSummary(r.Context(), visibleClientID(sess), from, to)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": map[string]any{"type": "query_failed", "message": "Usage query failed."},
		})
		return
	}
	var requests, errors int64
	for _, row := range rows {
		requests += row.Requests
		errors += row.Errors
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"from":     from,
		"to":       to,
		"requests": requests,
		"errors":   errors,
		"rows":     rows,
		// token 与成本尚未解析，明确报告为未知而不是 0
		"tokensKnown": false,
		"costKnown":   false,
	})
}

// usageRecent 返回最近明细。
func (s *apiServer) usageRecent(w http.ResponseWriter, r *http.Request) {
	if !allowGet(w, r) {
		return
	}
	if s.opts.Usage == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": map[string]any{
				"type":    "storage_not_configured",
				"message": "AIROUTER_DATABASE_URL is not set; usage is not recorded.",
			},
		})
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	sess, _ := sessionFrom(r.Context())
	rows, err := s.opts.Usage.RecentUsage(r.Context(), visibleClientID(sess), limit)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": map[string]any{"type": "query_failed", "message": "Usage query failed."},
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": rows})
}

// usageRange 解析 from/to 查询参数，默认最近 30 天。
func usageRange(r *http.Request) (time.Time, time.Time) {
	to := time.Now()
	from := to.AddDate(0, 0, -30)
	if v := r.URL.Query().Get("from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			from = t
		}
	}
	if v := r.URL.Query().Get("to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			to = t
		}
	}
	return from, to
}

func (s *apiServer) notImplemented(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusNotImplemented, map[string]any{
		"error": map[string]any{
			"type":    "not_implemented",
			"message": "Pending the storage design review; no data is stored yet.",
		},
	})
}

// storageState 报告持久化层是否已接入。
func storageState(opts Options) string {
	if opts.Usage != nil && opts.Clients != nil {
		return "postgres"
	}
	return "not_configured"
}

func allowGet(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet {
		return true
	}
	w.Header().Set("Allow", "GET")
	w.WriteHeader(http.StatusMethodNotAllowed)
	return false
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, `{"error":{"type":"internal_error"}}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(append(raw, '\n'))
}

// KeyDigest 返回管理密钥的 SHA256 十六进制摘要，供文档与校验示例使用。
func KeyDigest(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// listKeys 列出账户与模型权限。永不返回 Key 摘要或明文。
func (s *apiServer) listKeys(w http.ResponseWriter, r *http.Request) {
	if s.opts.Clients == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": map[string]any{
				"type":    "storage_not_configured",
				"message": "AIROUTER_DATABASE_URL is not set; key management is unavailable.",
			},
		})
		return
	}
	all, err := s.opts.Clients.ListClients(r.Context())
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": map[string]any{"type": "query_failed", "message": "Key query failed."},
		})
		return
	}
	sess, _ := sessionFrom(r.Context())
	// 客户端只能看到自己的账户，管理员看全部
	if !sess.Admin {
		mine := make([]store.Client, 0, 1)
		for _, c := range all {
			if c.ID == sess.ClientID {
				mine = append(mine, c)
				break
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": mine})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": all})
}

// createKey 生成新 Key。明文只在此响应中出现一次，之后无法再取回。
func (s *apiServer) createKey(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r.Context()); !ok {
		return
	}
	if s.opts.Clients == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": map[string]any{
				"type":    "storage_not_configured",
				"message": "AIROUTER_DATABASE_URL is not set; key management is unavailable.",
			},
		})
		return
	}
	var body struct {
		ID     string   `json:"id"`
		Label  string   `json:"label"`
		Models []string `json:"models"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]any{"type": "invalid_request", "message": "A JSON body with id is required."},
		})
		return
	}
	if len(body.Models) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]any{"type": "invalid_request", "message": "At least one allowed model is required."},
		})
		return
	}
	client, key, err := s.opts.Clients.CreateClient(r.Context(), body.ID, body.Label, body.Models)
	switch {
	case errors.Is(err, store.ErrClientExists):
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": map[string]any{"type": "already_exists", "message": "A client with this id already exists."},
		})
		return
	case err != nil:
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": map[string]any{"type": "write_failed", "message": "Could not create the key."},
		})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"data": client,
		// 唯一的明文返回点
		"key":  key,
		"note": "This key is shown once and cannot be retrieved again.",
	})
}

// revokeKey 吊销 Key，保留用量归属。
func (s *apiServer) revokeKey(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireAdmin(w, r.Context()); !ok {
		return
	}
	if s.opts.Clients == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": map[string]any{
				"type":    "storage_not_configured",
				"message": "AIROUTER_DATABASE_URL is not set; key management is unavailable.",
			},
		})
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]any{"type": "invalid_request", "message": "Client id is required."},
		})
		return
	}
	err := s.opts.Clients.RevokeClient(r.Context(), id)
	if errors.Is(err, store.ErrClientNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]any{
			"error": map[string]any{"type": "not_found", "message": "No active client with this id."},
		})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error": map[string]any{"type": "write_failed", "message": "Could not revoke the key."},
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"revoked": id})
}

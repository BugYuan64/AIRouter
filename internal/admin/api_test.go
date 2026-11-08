package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"airouter/internal/cpa"
	"airouter/internal/store"
)

type fakeCatalog struct {
	models []cpa.Model
	err    error
	calls  int
}

func (f *fakeCatalog) List(context.Context) ([]cpa.Model, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.models, nil
}

func call(t *testing.T, handler http.Handler, method, path, key string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, nil)
	if key != "" {
		request.Header.Set("Authorization", "Bearer "+key)
	}
	handler.ServeHTTP(recorder, request)
	return recorder
}

// 未配置管理密钥时不得暴露目录，返回 503 而不是空数据。
func TestAdminAPIRequiresConfiguredKey(t *testing.T) {
	handler := NewHandler(Options{Catalog: &fakeCatalog{}})
	for _, path := range []string{"/admin/api/status", "/admin/api/models"} {
		recorder := call(t, handler, "GET", path, "anything")
		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s: want 503, got %d", path, recorder.Code)
		}
	}
}

func TestAdminAPIRejectsWrongKey(t *testing.T) {
	handler := NewHandler(Options{AdminKey: testAdminKey, Catalog: &fakeCatalog{}})
	for _, key := range []string{"", "wrong", testAdminKey + "x"} {
		recorder := call(t, handler, "GET", "/admin/api/models", key)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("key %q: want 401, got %d", key, recorder.Code)
		}
	}
}

func TestAdminAPIStatusReportsCatalog(t *testing.T) {
	catalog := &fakeCatalog{models: []cpa.Model{{ID: "b-model"}, {ID: "a-model"}}}
	handler := NewHandler(Options{AdminKey: testAdminKey, Catalog: catalog, Version: "test-v"})

	recorder := call(t, handler, "GET", "/admin/api/status", testAdminKey)
	if recorder.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", recorder.Code)
	}
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if payload["upstream"] != "ok" {
		t.Fatalf("want upstream ok, got %v", payload["upstream"])
	}
	if payload["catalogCount"] != float64(2) {
		t.Fatalf("want catalogCount 2, got %v", payload["catalogCount"])
	}
	// 自动路由未启用时策略版本必须为 null，不能伪造编号
	if payload["activePolicy"] != nil {
		t.Fatalf("want nil activePolicy, got %v", payload["activePolicy"])
	}
}

// 目录模型能力一律为 unknown：目录存在不等于能力已验证。
func TestModelsReportUnknownCapabilities(t *testing.T) {
	catalog := &fakeCatalog{models: []cpa.Model{{ID: "gpt-6.1-sol", OwnedBy: "openai"}}}
	handler := NewHandler(Options{AdminKey: testAdminKey, Catalog: catalog})
	recorder := call(t, handler, "GET", "/admin/api/models", testAdminKey)
	if recorder.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", recorder.Code)
	}
	var payload struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(payload.Data) != 1 {
		t.Fatalf("want 1 model, got %d", len(payload.Data))
	}
	model := payload.Data[0]
	for _, field := range []string{"chat", "responses", "messages", "tools"} {
		if model[field] != "unknown" {
			t.Fatalf("%s: want unknown, got %v", field, model[field])
		}
	}
	if model["contextWindow"] != nil || model["verifiedAt"] != nil {
		t.Fatal("unverified capability fields must be null, not fabricated")
	}
}

// 上游失败且无缓存时返回 502，不能返回空目录假装成功。
func TestModelsUpstreamFailureIsError(t *testing.T) {
	catalog := &fakeCatalog{err: errors.New("boom")}
	handler := NewHandler(Options{AdminKey: testAdminKey, Catalog: catalog})
	recorder := call(t, handler, "GET", "/admin/api/models", testAdminKey)
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("want 502, got %d", recorder.Code)
	}
}

// 上游失败但有陈旧缓存时返回 200 并标注 stale，不静默过期。
func TestModelsServeStaleWithFlag(t *testing.T) {
	catalog := &fakeCatalog{models: []cpa.Model{{ID: "m1"}}}
	handler := NewHandler(Options{AdminKey: testAdminKey, Catalog: catalog, CatalogTTL: time.Nanosecond})
	call(t, handler, "GET", "/admin/api/models", testAdminKey)

	catalog.err = errors.New("down")
	time.Sleep(2 * time.Millisecond)
	recorder := call(t, handler, "GET", "/admin/api/models", testAdminKey)
	if recorder.Code != http.StatusOK {
		t.Fatalf("want 200 with stale cache, got %d", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), `"stale":true`) {
		t.Fatalf("stale cache must be flagged: %s", recorder.Body.String())
	}
}

// 目录缓存避免状态轮询打穿上游。
func TestCatalogCacheLimitsUpstreamCalls(t *testing.T) {
	catalog := &fakeCatalog{models: []cpa.Model{{ID: "m1"}}}
	handler := NewHandler(Options{AdminKey: testAdminKey, Catalog: catalog})
	for i := 0; i < 5; i++ {
		call(t, handler, "GET", "/admin/api/status", testAdminKey)
	}
	if catalog.calls != 1 {
		t.Fatalf("want 1 upstream call within TTL, got %d", catalog.calls)
	}
}

// 未配置数据库时用量接口返回 503 说明原因，不能返回空数组假装「还没有数据」。
func TestUsageRequiresStorage(t *testing.T) {
	handler := NewHandler(Options{AdminKey: testAdminKey, Catalog: &fakeCatalog{}})
	for _, path := range []string{"/admin/api/usage", "/admin/api/usage/recent"} {
		recorder := call(t, handler, "GET", path, testAdminKey)
		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s: want 503, got %d", path, recorder.Code)
		}
		if !strings.Contains(recorder.Body.String(), "storage_not_configured") {
			t.Fatalf("%s: must explain missing storage, got %s", path, recorder.Body.String())
		}
	}
}

// 未配置数据库时 Key 接口返回 503 说明原因，不返回空列表假装「没有账户」。
func TestKeysRequiresStorage(t *testing.T) {
	handler := NewHandler(Options{AdminKey: testAdminKey, Catalog: &fakeCatalog{}})
	for _, tc := range []struct{ method, path string }{
		{"GET", "/admin/api/keys"},
		{"POST", "/admin/api/keys"},
		{"DELETE", "/admin/api/keys/x"},
	} {
		recorder := call(t, handler, tc.method, tc.path, testAdminKey)
		if recorder.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s %s: want 503, got %d", tc.method, tc.path, recorder.Code)
		}
	}
}

// 列表接口绝不能返回 Key 明文或摘要。
func TestListKeysNeverExposesSecret(t *testing.T) {
	fake := &fakeClients{list: []store.Client{
		{ID: "alice", KeyPrefix: "sk-ar-abc1", Label: "Alice", Status: "active", Models: []string{"m1"}},
	}}
	handler := NewHandler(Options{AdminKey: testAdminKey, Catalog: &fakeCatalog{}, Clients: fake})
	recorder := call(t, handler, "GET", "/admin/api/keys", testAdminKey)
	if recorder.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", recorder.Code)
	}
	body := recorder.Body.String()
	if strings.Contains(body, "key_sha256") || strings.Contains(body, "sha256") {
		t.Fatalf("response must not expose digests: %s", body)
	}
	if !strings.Contains(body, "sk-ar-abc1") {
		t.Fatalf("prefix should be present for identification: %s", body)
	}
}

// 创建接口返回明文一次，并明确提示不可再次获取。
func TestCreateKeyReturnsPlaintextOnce(t *testing.T) {
	fake := &fakeClients{}
	handler := NewHandler(Options{AdminKey: testAdminKey, Catalog: &fakeCatalog{}, Clients: fake})
	recorder := callJSON(t, handler, "POST", "/admin/api/keys", testAdminKey,
		`{"id":"bob","label":"Bob","models":["m1"]}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["key"] == "" || payload["key"] == nil {
		t.Fatal("plaintext key must be returned once")
	}
	if payload["note"] == nil {
		t.Fatal("must warn that the key cannot be retrieved again")
	}
}

// 创建缺少模型时必须拒绝：无权限的账户不应被创建。
func TestCreateKeyRequiresModels(t *testing.T) {
	fake := &fakeClients{}
	handler := NewHandler(Options{AdminKey: testAdminKey, Catalog: &fakeCatalog{}, Clients: fake})
	recorder := callJSON(t, handler, "POST", "/admin/api/keys", testAdminKey, `{"id":"x"}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", recorder.Code)
	}
}

// 已存在的 id 返回 409，不覆盖既有 Key。
func TestCreateKeyConflict(t *testing.T) {
	fake := &fakeClients{createErr: store.ErrClientExists}
	handler := NewHandler(Options{AdminKey: testAdminKey, Catalog: &fakeCatalog{}, Clients: fake})
	recorder := callJSON(t, handler, "POST", "/admin/api/keys", testAdminKey, `{"id":"dup","models":["m"]}`)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d", recorder.Code)
	}
}

// 吊销未知账户返回 404。
func TestRevokeUnknownKey(t *testing.T) {
	fake := &fakeClients{revokeErr: store.ErrClientNotFound}
	handler := NewHandler(Options{AdminKey: testAdminKey, Catalog: &fakeCatalog{}, Clients: fake})
	recorder := call(t, handler, "DELETE", "/admin/api/keys/nobody", testAdminKey)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", recorder.Code)
	}
}

type fakeClients struct {
	list      []store.Client
	createErr error
	revokeErr error
	// lookupID 非空时，任意 token 都被当作该账户的合法 Key
	lookupID string
}

func (f *fakeClients) ListClients(context.Context) ([]store.Client, error) { return f.list, nil }

func (f *fakeClients) CreateClient(_ context.Context, id, label string, models []string) (store.Client, string, error) {
	if f.createErr != nil {
		return store.Client{}, "", f.createErr
	}
	return store.Client{ID: id, Label: label, Status: "active", Models: models}, "sk-ar-generated", nil
}

func (f *fakeClients) RevokeClient(context.Context, string) error { return f.revokeErr }

func (f *fakeClients) LookupByDigest(context.Context, string) (*store.Client, error) {
	if f.lookupID == "" {
		return nil, nil
	}
	return &store.Client{ID: f.lookupID, Status: "active"}, nil
}

func callJSON(t *testing.T, handler http.Handler, method, path, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(recorder, request)
	return recorder
}

// 用量汇总必须报告 token/成本是否已知，避免把未知显示成 0。
func TestUsageSummaryReportsUnknownTokens(t *testing.T) {
	fake := &fakeUsage{rows: []store.UsageSummaryRow{
		{ClientID: "alice", Protocol: "chat", Requests: 3, Errors: 1, Streams: 2, AvgLatencyMS: 100},
	}}
	handler := NewHandler(Options{AdminKey: testAdminKey, Catalog: &fakeCatalog{}, Usage: fake})
	recorder := call(t, handler, "GET", "/admin/api/usage", testAdminKey)
	if recorder.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if payload["requests"] != float64(3) || payload["errors"] != float64(1) {
		t.Fatalf("aggregate mismatch: %v", payload)
	}
	if payload["tokensKnown"] != false || payload["costKnown"] != false {
		t.Fatal("token/cost must be reported as unknown, not fabricated")
	}
}

type fakeUsage struct {
	rows      []store.UsageSummaryRow
	detail    []store.UsageDetailRow
	err       error
	lastScope string
}

func (f *fakeUsage) UsageSummary(_ context.Context, clientID string, _ time.Time, _ time.Time) ([]store.UsageSummaryRow, error) {
	f.lastScope = clientID
	return f.rows, f.err
}

func (f *fakeUsage) RecentUsage(_ context.Context, clientID string, _ int) ([]store.UsageDetailRow, error) {
	f.lastScope = clientID
	return f.detail, f.err
}

func TestAdminAPIRejectsNonGet(t *testing.T) {
	handler := NewHandler(Options{AdminKey: testAdminKey, Catalog: &fakeCatalog{}})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/admin/api/status", nil)
	request.Header.Set("Authorization", testAdminHeader())
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("want 405, got %d", recorder.Code)
	}
}

// 客户端只能看到自己的用量；范围必须来自认证身份，不能来自请求参数。
func TestClientUsageScopedToOwnAccount(t *testing.T) {
	usage := &fakeUsage{rows: []store.UsageSummaryRow{{ClientID: "alice", Protocol: "chat", Requests: 1}}}
	clients := &fakeClients{lookupID: "alice"}
	handler := NewHandler(Options{AdminKey: testAdminKey, Catalog: &fakeCatalog{}, Usage: usage, Clients: clients})

	// 用客户端 Key 登录后查询用量
	recorder := call(t, handler, "GET", "/admin/api/usage", "alice-key")
	if recorder.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	if usage.lastScope != "alice" {
		t.Fatalf("scope must be alice, got %q", usage.lastScope)
	}
}

// 客户端不能用查询参数扩大范围：即使传 ?client=bob 也只能看到自己。
func TestClientCannotWidenScopeViaQuery(t *testing.T) {
	usage := &fakeUsage{}
	clients := &fakeClients{lookupID: "alice"}
	handler := NewHandler(Options{AdminKey: testAdminKey, Catalog: &fakeCatalog{}, Usage: usage, Clients: clients})

	call(t, handler, "GET", "/admin/api/usage?client=bob", "alice-key")
	if usage.lastScope != "alice" {
		t.Fatalf("query param must not change scope, got %q", usage.lastScope)
	}
}

// 管理员查询不限定账户（空字符串表示全部）。
func TestAdminUsageSeesAllAccounts(t *testing.T) {
	usage := &fakeUsage{}
	handler := NewHandler(Options{AdminKey: testAdminKey, Catalog: &fakeCatalog{}, Usage: usage, Clients: &fakeClients{}})
	call(t, handler, "GET", "/admin/api/usage", testAdminKey)
	if usage.lastScope != "" {
		t.Fatalf("admin scope must be empty (all), got %q", usage.lastScope)
	}
}

// 客户端不得创建或吊销 Key。
func TestClientCannotManageKeys(t *testing.T) {
	clients := &fakeClients{lookupID: "alice"}
	handler := NewHandler(Options{AdminKey: testAdminKey, Catalog: &fakeCatalog{}, Clients: clients})

	for _, tc := range []struct{ method, path string }{
		{"POST", "/admin/api/keys"},
		{"DELETE", "/admin/api/keys/bob"},
	} {
		recorder := callJSON(t, handler, tc.method, tc.path, "alice-key", `{"id":"x","models":["m"]}`)
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("%s %s: client must get 403, got %d", tc.method, tc.path, recorder.Code)
		}
	}
}

// 客户端列表只返回自己那一条。
func TestClientSeesOnlyOwnAccountInKeyList(t *testing.T) {
	clients := &fakeClients{
		lookupID: "alice",
		list: []store.Client{
			{ID: "alice", Status: "active"},
			{ID: "bob", Status: "active"},
		},
	}
	handler := NewHandler(Options{AdminKey: testAdminKey, Catalog: &fakeCatalog{}, Clients: clients})
	recorder := call(t, handler, "GET", "/admin/api/keys", "alice-key")
	if recorder.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", recorder.Code)
	}
	var payload struct {
		Data []store.Client `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Data) != 1 || payload.Data[0].ID != "alice" {
		t.Fatalf("client must see only own account, got %+v", payload.Data)
	}
}

// whoami 必须如实反映角色，供前端渲染；前端不得据此做安全决策。
func TestWhoamiReportsRole(t *testing.T) {
	clients := &fakeClients{lookupID: "alice"}
	handler := NewHandler(Options{AdminKey: testAdminKey, Catalog: &fakeCatalog{}, Clients: clients})

	admin := call(t, handler, "GET", "/admin/api/whoami", testAdminKey)
	var adminBody map[string]any
	if err := json.Unmarshal(admin.Body.Bytes(), &adminBody); err != nil {
		t.Fatal(err)
	}
	if adminBody["role"] != "admin" {
		t.Fatalf("admin key must report role=admin, got %v", adminBody["role"])
	}
	caps, _ := adminBody["capabilities"].(map[string]any)
	if caps["manageKeys"] != true || caps["viewAllUsage"] != true {
		t.Fatalf("admin capabilities wrong: %v", caps)
	}

	client := call(t, handler, "GET", "/admin/api/whoami", "alice-key")
	var clientBody map[string]any
	if err := json.Unmarshal(client.Body.Bytes(), &clientBody); err != nil {
		t.Fatal(err)
	}
	if clientBody["role"] != "client" || clientBody["clientId"] != "alice" {
		t.Fatalf("client identity wrong: %v", clientBody)
	}
	clientCaps, _ := clientBody["capabilities"].(map[string]any)
	if clientCaps["manageKeys"] != false || clientCaps["viewAllUsage"] != false {
		t.Fatalf("client must not have admin capabilities: %v", clientCaps)
	}
}

// 无凭据时 whoami 返回 401。
func TestWhoamiRequiresAuth(t *testing.T) {
	handler := NewHandler(Options{AdminKey: testAdminKey, Catalog: &fakeCatalog{}})
	recorder := call(t, handler, "GET", "/admin/api/whoami", "")
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", recorder.Code)
	}
}

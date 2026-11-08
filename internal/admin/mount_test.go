package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stubDataPlane 记录被调用的路径，便于断言分流是否正确。
func stubDataPlane(called *[]string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*called = append(*called, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
}

// 合并后数据面路径必须交给网关，不能落到控制台的 SPA 回落。
// 这是最关键的一条：错误分流的后果是模型客户端拿到 HTML。
func TestMountRoutesDataPlaneToGateway(t *testing.T) {
	var called []string
	handler := Mount(stubDataPlane(&called), Options{AdminKey: testAdminKey, Catalog: &fakeCatalog{}, Clients: &fakeClients{}})

	paths := []string{
		"/v1/chat/completions",
		"/v1/responses",
		"/v1/messages",
		"/v1/messages/count_tokens",
		"/v1/models",
		"/healthz",
		"/readyz",
	}
	for _, path := range paths {
		called = nil
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest("POST", path, strings.NewReader("{}")))
		if len(called) != 1 || called[0] != path {
			t.Fatalf("%s must reach the data plane, got calls=%v", path, called)
		}
		if ct := recorder.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
			t.Fatalf("%s: data plane response must stay JSON, got %q", path, ct)
		}
	}
}

// 未在数据面注册的 /v1/* 路径也必须走数据面（返回 JSON），
// 不能被 SPA 回落成 HTML。新增端点忘记注册时，客户端会收到明确错误。
func TestMountUnknownV1PathStaysOnDataPlane(t *testing.T) {
	var called []string
	handler := Mount(stubDataPlane(&called), Options{AdminKey: testAdminKey, Catalog: &fakeCatalog{}, Clients: &fakeClients{}})

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest("POST", "/v1/some-future-endpoint", strings.NewReader("{}")))
	if len(called) != 1 {
		t.Fatalf("unknown /v1 path must still reach data plane, got %v", called)
	}
	if strings.Contains(recorder.Body.String(), "<!doctype html") {
		t.Fatal("data plane must never receive SPA HTML fallback")
	}
}

// 控制台路径与管理 API 必须走控制台，不进入数据面。
func TestMountRoutesConsolePaths(t *testing.T) {
	var called []string
	handler := Mount(stubDataPlane(&called), Options{AdminKey: testAdminKey, Catalog: &fakeCatalog{}, Clients: &fakeClients{}})

	// 管理 API：未带 Key 应 401 JSON，且不碰数据面
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest("GET", "/admin/api/status", nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("/admin/api/status want 401, got %d", recorder.Code)
	}
	if len(called) != 0 {
		t.Fatalf("admin API must not touch data plane, got %v", called)
	}

	// 带 Key 应 200
	recorder = httptest.NewRecorder()
	request := httptest.NewRequest("GET", "/admin/api/status", nil)
	request.Header.Set("Authorization", "Bearer "+testAdminKey)
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("/admin/api/status with key want 200, got %d", recorder.Code)
	}

	// 根路径是控制台 SPA 入口
	if built(Assets()) {
		recorder = httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest("GET", "/", nil))
		if !strings.Contains(recorder.Header().Get("Content-Type"), "text/html") {
			t.Fatalf("root must serve console HTML, got %q", recorder.Header().Get("Content-Type"))
		}
		if len(called) != 0 {
			t.Fatalf("root must not touch data plane, got %v", called)
		}
	}
}

// 前端路由路径（/login、/models）应回落 SPA，且不碰数据面。
func TestMountSPAFallbackForClientRoutes(t *testing.T) {
	if !built(Assets()) {
		t.Skip("frontend not built")
	}
	var called []string
	handler := Mount(stubDataPlane(&called), Options{AdminKey: testAdminKey, Catalog: &fakeCatalog{}, Clients: &fakeClients{}})

	for _, path := range []string{"/login", "/models", "/usage"} {
		called = nil
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest("GET", path, nil))
		if !strings.Contains(recorder.Header().Get("Content-Type"), "text/html") {
			t.Fatalf("%s must fall back to SPA, got %q", path, recorder.Header().Get("Content-Type"))
		}
		if len(called) != 0 {
			t.Fatalf("%s must not touch data plane", path)
		}
	}
}

func TestIsDataPlanePath(t *testing.T) {
	yes := []string{"/v1", "/v1/", "/v1/models", "/v1/chat/completions", "/healthz", "/readyz"}
	no := []string{"/", "/admin", "/admin/", "/admin/api/status", "/login", "/assets/x.js", "/v2/models"}
	for _, p := range yes {
		if !IsDataPlanePath(p) {
			t.Errorf("%s should be data plane", p)
		}
	}
	for _, p := range no {
		if IsDataPlanePath(p) {
			t.Errorf("%s should not be data plane", p)
		}
	}
}

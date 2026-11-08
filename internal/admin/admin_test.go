package admin

import (
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testAdminKey = "test-admin-key"

func testAdminHeader() string { return "Bearer " + testAdminKey }

// 管理面与数据面必须物理隔离：管理端口不得回落到 SPA 而把 /v1 请求
// 伪装成 HTTP 200 + HTML。这是连错端口的常见表现，必须明确失败。
func TestAdminRejectsDataPlanePaths(t *testing.T) {
	if !built(Assets()) {
		t.Skip("frontend not built")
	}
	handler := NewHandler(Options{})
	for _, path := range []string{"/v1/chat/completions", "/v1/responses", "/v1/messages", "/v1/models", "/v1"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest("POST", path, strings.NewReader("{}")))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s: want 404, got %d", path, recorder.Code)
		}
		contentType := recorder.Header().Get("Content-Type")
		if !strings.HasPrefix(contentType, "application/json") {
			t.Fatalf("%s: want JSON error, got %q", path, contentType)
		}
		if strings.Contains(recorder.Body.String(), "<!doctype html") {
			t.Fatalf("%s: must not serve HTML", path)
		}
	}
}

// 控制面未实现时明确返回 501 JSON，不能回落成 HTML。
func TestUnimplementedAPIReturnsJSON(t *testing.T) {
	handler := NewHandler(Options{})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest("GET", "/admin/api/status", nil))
	if recorder.Code != http.StatusNotImplemented {
		t.Fatalf("want 501, got %d", recorder.Code)
	}
	if !strings.HasPrefix(recorder.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("want JSON content type, got %q", recorder.Header().Get("Content-Type"))
	}
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if _, ok := payload["error"]; !ok {
		t.Fatalf("want error object, got %v", payload)
	}
}

// 已挂载的 API 前缀下未知路径必须 404 JSON，而不是 SPA 的 index.html，
// 否则前端解析失败却看不出是接口缺失。
func TestUnknownAPIPathDoesNotFallBackToHTML(t *testing.T) {
	handler := NewHandler(Options{AdminKey: "test-admin-key"})
	key := testAdminHeader()

	// 已实现的接口需要鉴权，但路径本身存在
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest("GET", "/admin/api/status", nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status without key want 401, got %d", recorder.Code)
	}

	recorder = httptest.NewRecorder()
	request := httptest.NewRequest("GET", "/admin/api/status", nil)
	request.Header.Set("Authorization", key)
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status with key want 200, got %d", recorder.Code)
	}

	// 未知接口 404 且非 HTML
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest("GET", "/admin/api/nope", nil)
	request.Header.Set("Authorization", key)
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "<!doctype html") {
		t.Fatal("unknown API path must not fall back to index.html")
	}
}

// 静态资源可被读取，未知非 API 路径回落到 SPA 入口。
func TestStaticAssetsAndSPAFallback(t *testing.T) {
	handler := NewHandler(Options{})
	assets := Assets()
	if !built(assets) {
		t.Skip("frontend not built; run cd web && npm run build")
	}

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest("GET", "/", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("root want 200, got %d", recorder.Code)
	}
	if !strings.Contains(strings.ToLower(recorder.Header().Get("Content-Type")), "text/html") {
		t.Fatalf("root want html, got %q", recorder.Header().Get("Content-Type"))
	}

	// history 模式下前端路由是 /models 这类路径，需回落 SPA
	for _, path := range []string{"/models", "/usage", "/login"} {
		recorder = httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest("GET", path, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s: SPA fallback want 200, got %d", path, recorder.Code)
		}
		if !strings.Contains(strings.ToLower(recorder.Header().Get("Content-Type")), "text/html") {
			t.Fatalf("%s: SPA fallback want html, got %q", path, recorder.Header().Get("Content-Type"))
		}
	}
}

// 未构建时给出构建提示页，而不是 500 或空白。
func TestUnbuiltServesInstructionPage(t *testing.T) {
	if built(Assets()) {
		t.Skip("frontend is built")
	}
	recorder := httptest.NewRecorder()
	NewHandler(Options{}).ServeHTTP(recorder, httptest.NewRequest("GET", "/", nil))
	body, _ := io.ReadAll(recorder.Body)
	if !strings.Contains(string(body), "npm run build") {
		t.Fatal("unbuilt response must explain how to build")
	}
}

// 前端改为 history 模式后资源以 /assets 绝对路径引用，
// 根路径必须能取到真实 JS，不能被 SPA 回落成 HTML。
func TestAssetsServedAtRoot(t *testing.T) {
	if !built(Assets()) {
		t.Skip("frontend not built")
	}
	entries, err := fs.ReadDir(Assets(), "assets")
	if err != nil || len(entries) == 0 {
		t.Skip("no built assets")
	}
	var jsName string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".js") {
			jsName = e.Name()
			break
		}
	}
	if jsName == "" {
		t.Skip("no js asset")
	}

	handler := NewHandler(Options{AdminKey: testAdminKey, Catalog: &fakeCatalog{}})
	recorder := call(t, handler, "GET", "/assets/"+jsName, "")
	if !strings.Contains(recorder.Header().Get("Content-Type"), "javascript") {
		t.Fatalf("/assets must serve JS, got %q", recorder.Header().Get("Content-Type"))
	}
}

// /admin 前缀已废弃（history 模式资源用绝对路径），应重定向到根路径，
// 而不是返回一个加载不到资源的 HTML 外壳。
func TestAdminPrefixRedirects(t *testing.T) {
	handler := NewHandler(Options{AdminKey: testAdminKey, Catalog: &fakeCatalog{}})
	for _, path := range []string{"/admin", "/admin/"} {
		recorder := call(t, handler, "GET", path, "")
		if recorder.Code != http.StatusFound {
			t.Fatalf("%s want 302, got %d", path, recorder.Code)
		}
		if loc := recorder.Header().Get("Location"); loc != "/" {
			t.Fatalf("%s want Location /, got %q", path, loc)
		}
	}
}

// /admin/api 必须仍然走 API 处理器，不能被静态资源路由吞掉。
func TestAPIPrefixNotShadowedByAdminStatic(t *testing.T) {
	handler := NewHandler(Options{AdminKey: testAdminKey, Catalog: &fakeCatalog{}})
	recorder := call(t, handler, "GET", "/admin/api/status", testAdminKey)
	if recorder.Code != http.StatusOK {
		t.Fatalf("/admin/api/status want 200, got %d body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("/admin/api must return JSON, got %q", recorder.Header().Get("Content-Type"))
	}

	// 无 Key 时是 401 JSON，不是首页 HTML
	recorder = call(t, handler, "GET", "/admin/api/status", "")
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", recorder.Code)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

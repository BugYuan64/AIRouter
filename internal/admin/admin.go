// Package admin 提供管理面 HTTP 服务，与数据面 /v1/* 物理隔离。
//
// 边界：
//   - 独立端口、独立 http.Server 实例；管理 handler 的 panic 与阻塞不进入数据面。
//   - 管理 Key 与客户端网关 Key 分离，不复用 clients.json。
//   - 前端产物经 embed.FS 提供，静态资源不依赖外部 CDN。
package admin

import (
	"bytes"
	"embed"
	"io/fs"
	"net/http"
	"strings"
	"time"
)

// dist 由 `cd web && npm run build` 生成（Vite outDir 指向本目录）。
// 无产物时 .keep 保证本包仍可编译，运行期返回构建提示而不是 500。
//
//go:embed all:dist
var dist embed.FS

const notBuiltPage = `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8">
<title>AIRouter 控制台未构建</title>
<style>body{font-family:system-ui,sans-serif;background:#fff;color:#070323;
margin:0;display:grid;place-items:center;min-height:100vh}
main{max-width:32rem;padding:2rem}
code{font-family:ui-monospace,monospace;background:#f6f6f8;padding:.15rem .4rem;border-radius:4px}
h1{font-weight:400;font-size:2rem;line-height:1.1;margin:0 0 1rem}</style></head>
<body><main><h1>控制台尚未构建</h1>
<p>前端产物未打包进二进制。在 <code>web/</code> 目录执行：</p>
<p><code>npm install &amp;&amp; npm run build</code></p>
<p>然后重新构建 Go 二进制。</p></main></body></html>`

// Assets 返回打包好的前端资源根目录。
func Assets() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return nil
	}
	return sub
}

// built 报告前端产物是否可用（以 index.html 存在为准）。
func built(assets fs.FS) bool {
	if assets == nil {
		return false
	}
	_, err := fs.Stat(assets, "index.html")
	return err == nil
}

// NewHandler 组装控制台路由：管理 API + SPA 入口。
//
// 前端使用 history 模式，资源以 /assets 绝对路径引用，因此只需根路径。
// /admin 与 /admin/ 重定向到根路径，兼容旧链接。
func NewHandler(opts Options) http.Handler {
	return Controls(opts)
}

// Controls 返回控制台的路由，供独立使用或合并到单端口。
//
// 包含根路径的 SPA 入口：前端产物以 /assets/* 引用静态资源，
// 必须由根处理器提供。调用方需在此之前分流数据面路径，
// 否则 /v1/* 会被 SPA 回落吞掉。
func Controls(opts Options) http.Handler {
	assets := Assets()
	mux := http.NewServeMux()

	if opts.AdminKey != "" || opts.Catalog != nil {
		mux.Handle("/admin/api/", http.StripPrefix("/admin/api", newAPI(opts)))
	} else {
		mux.HandleFunc("/admin/api/", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusNotImplemented)
			_, _ = w.Write([]byte(`{"error":{"type":"not_implemented","message":"control plane is not configured"}}`))
		})
	}

	// /admin 前缀已废弃：history 模式下资源用绝对路径，
	// 保留前缀会返回加载不到资源的外壳。重定向到根路径。
	redirect := func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/", http.StatusFound)
	}
	mux.HandleFunc("/admin", redirect)
	mux.HandleFunc("/admin/", redirect)
	mux.Handle("/", spaHandler(assets))
	return mux
}

// spaHandler 提供 embed 静态资源；非资源路径回落到 index.html 以支持前端路由。
func spaHandler(assets fs.FS) http.Handler {
	if !built(assets) {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(notBuiltPage))
		})
	}
	index, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		index = []byte(notBuiltPage)
	}
	fileServer := http.FileServer(http.FS(assets))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// API 前缀未匹配到处理器时返回 404 JSON，不回落成 HTML，
		// 否则客户端会解析失败却看不出是接口缺失。
		if strings.HasPrefix(r.URL.Path, "/admin/api/") {
			http.NotFound(w, r)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		// 数据面路径在管理端口必须明确 404：SPA 回落会让连错端口的模型客户端
		// 收到 HTTP 200 + HTML，把端口错误伪装成成功响应。
		if isDataPlanePath(r.URL.Path) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"type":"wrong_endpoint","message":"this port serves the admin console, not the model API"}}`))
			return
		}
		if name != "" {
			if info, statErr := fs.Stat(assets, name); statErr == nil && !info.IsDir() {
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(index))
	})
}

// isDataPlanePath 报告路径是否属于模型数据面（/v1/*）。
func isDataPlanePath(path string) bool {
	return path == "/v1" || strings.HasPrefix(path, "/v1/")
}

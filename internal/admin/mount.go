package admin

import (
	"net/http"
	"strings"
)

// Mount 把数据面与控制台合并到一个 http.Handler 上，供单端口部署使用。
//
// 路由优先级由 ServeMux 的最长匹配决定，但数据面与控制台都需要接管
// 相当宽的路径区间，因此这里显式分流，避免规则互相遮挡：
//
//	数据面：/v1/*、/healthz、/readyz
//	控制台：/admin/api/*、/admin/*、其余全部（SPA 入口）
//
// 关键约束：数据面的响应绝不能被 SPA 回落吞掉。若调用方请求
// /v1/some-future-endpoint 而该路径未在数据面注册，必须返回 JSON 错误，
// 而不是 index.html —— 否则模型客户端会解析失败却看不出原因。
func Mount(dataPlane http.Handler, opts Options) http.Handler {
	controls := Controls(opts)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if IsDataPlanePath(path) {
			dataPlane.ServeHTTP(w, r)
			return
		}
		controls.ServeHTTP(w, r)
	})
}

// IsDataPlanePath 报告路径是否属于模型数据面。
//
// 新增数据面端点时必须同步这里与 Caddy 的路径判断。
func IsDataPlanePath(path string) bool {
	switch {
	case path == "/v1" || strings.HasPrefix(path, "/v1/"):
		return true
	case path == "/healthz" || path == "/readyz":
		return true
	}
	return false
}

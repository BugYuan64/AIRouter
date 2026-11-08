package config

import (
	"os"
	"testing"
)

// 容器化部署必须能用 Docker 网络别名访问 CPA（如 http://cpa:8317）。
// 这是部署前置条件：放行范围过窄会让进程直接起不来。
func TestParseBaseURLAllowsPrivateNetworks(t *testing.T) {
	allowed := []string{
		"https://cpa.example.com",  // 公网 HTTPS
		"https://8.8.8.8",          // 公网 HTTPS 用 IP 也可
		"http://127.0.0.1:8317",    // 回环（本机/SSH 隧道）
		"http://localhost:8317",    // 回环主机名
		"http://[::1]:8317",        // IPv6 回环
		"http://cpa:8317",          // Docker 单标签别名
		"http://cliproxyapi:8317",  // Docker 服务名
		"http://10.0.0.5:8317",     // RFC1918 10/8
		"http://172.20.0.2:8317",   // RFC1918 172.16/12（当前 bridge 网段）
		"http://192.168.1.10:8317", // RFC1918 192.168/16
		"https://cpa.local:8443",   // 私有主机名 + HTTPS
	}
	for _, raw := range allowed {
		if _, err := ParseBaseURL(raw); err != nil {
			t.Errorf("ParseBaseURL(%q) rejected, want allowed: %v", raw, err)
		}
	}
}

// 公网明文 HTTP 仍必须拒绝：CPA_API_KEY 会以 Bearer 明文传输。
func TestParseBaseURLRejectsPublicPlainHTTP(t *testing.T) {
	rejected := []string{
		"http://8.8.8.8:8317",
		"http://example.com",
		"http://api.openai.com:80",
		"http://1.1.1.1",
	}
	for _, raw := range rejected {
		if _, err := ParseBaseURL(raw); err == nil {
			t.Errorf("ParseBaseURL(%q) accepted, want rejected", raw)
		}
	}
}

// 其他非法形态仍应拒绝，放行内网不能顺带放宽这些约束。
func TestParseBaseURLStillRejectsMalformedOrigins(t *testing.T) {
	rejected := []string{
		"https://user:pass@cpa.example.com", // 含用户信息
		"https://cpa.example.com/v1",        // 含路径
		"https://cpa.example.com?a=1",       // 含查询
		"https://cpa.example.com#frag",      // 含片段
		"ftp://cpa.example.com",             // 非 HTTP(S)
		"cpa.example.com",                   // 缺 scheme
		"http://cpa:0",                      // 端口越界
		"http://cpa:70000",                  // 端口越界
		"http://",                           // 缺主机
	}
	for _, raw := range rejected {
		if _, err := ParseBaseURL(raw); err == nil {
			t.Errorf("ParseBaseURL(%q) accepted, want rejected", raw)
		}
	}
}

// 配置数据库后权限文件可省略：认证走数据库。
// 两者都缺失才应是配置错误。
func TestLoadRequiresSomeCredentialSource(t *testing.T) {
	base := map[string]string{
		"CPA_BASE_URL": "http://cpa:8317",
		"CPA_API_KEY":  "cpa-key",
	}
	getenv := func(k string) string { return base[k] }
	readFile := func(string) ([]byte, error) { return nil, os.ErrNotExist }

	if _, err := Load(getenv, readFile); err == nil {
		t.Fatal("missing both clients file and database must fail")
	}

	base["AIROUTER_DATABASE_URL"] = "postgres://u:p@db:5432/x"
	cfg, err := Load(getenv, readFile)
	if err != nil {
		t.Fatalf("database-only config must load: %v", err)
	}
	if cfg.DatabaseURL == "" || len(cfg.Clients) != 0 {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

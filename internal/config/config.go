package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	ID            string   `json:"id"`
	KeySHA256     string   `json:"key_sha256"`
	AllowedModels []string `json:"allowed_models"`
	// IsAdmin 标记管理员自身账户；只影响用量归属展示，不授予管理面权限。
	IsAdmin bool `json:"is_admin,omitempty"`
}

type Config struct {
	ListenAddr      string
	AdminKey        string
	CPABaseURL      string
	CPAAPIKey       string
	DatabaseURL     string
	UpstreamTimeout time.Duration
	RequestTimeout  time.Duration
	MaxRequestBytes int64
	Clients         []Client
}

func Load(getenv func(string) string, readFile func(string) ([]byte, error)) (Config, error) {
	c := Config{ListenAddr: "127.0.0.1:8080", CPABaseURL: getenv("CPA_BASE_URL"), CPAAPIKey: getenv("CPA_API_KEY"), UpstreamTimeout: 10 * time.Second, RequestTimeout: 10 * time.Minute, MaxRequestBytes: 16 << 20}
	if v := getenv("AIROUTER_LISTEN_ADDR"); v != "" {
		c.ListenAddr = v
	}
	host, port, err := net.SplitHostPort(c.ListenAddr)
	_ = host
	if err != nil {
		return Config{}, errors.New("AIROUTER_LISTEN_ADDR must be host:port")
	}
	// 管理密钥与调用者网关密钥分离；这里只要求格式合法。
	if v := getenv("AIROUTER_ADMIN_KEY"); v != "" {
		if strings.ContainsAny(v, " \t\r\n") {
			return Config{}, errors.New("AIROUTER_ADMIN_KEY must be a nonempty token without whitespace")
		}
		c.AdminKey = v
	}
	// 数据库可选：未配置时不记录用量，生成路径不受影响。
	if v := getenv("AIROUTER_DATABASE_URL"); v != "" {
		if !strings.HasPrefix(v, "postgres://") && !strings.HasPrefix(v, "postgresql://") {
			return Config{}, errors.New("AIROUTER_DATABASE_URL must be a postgres:// DSN")
		}
		c.DatabaseURL = v
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 0 || n > 65535 {
		return Config{}, errors.New("AIROUTER_LISTEN_ADDR has invalid port")
	}
	if _, err = ParseBaseURL(c.CPABaseURL); err != nil {
		return Config{}, err
	}
	if c.CPAAPIKey == "" || strings.ContainsAny(c.CPAAPIKey, " \t\r\n") {
		return Config{}, errors.New("CPA_API_KEY must be a nonempty token")
	}
	for _, item := range []struct {
		name   string
		target *time.Duration
	}{{"AIROUTER_UPSTREAM_TIMEOUT", &c.UpstreamTimeout}, {"AIROUTER_REQUEST_TIMEOUT", &c.RequestTimeout}} {
		if v := getenv(item.name); v != "" {
			d, e := time.ParseDuration(v)
			if e != nil || d <= 0 {
				return Config{}, errors.New(item.name + " must be a positive duration")
			}
			*item.target = d
		}
	}
	if v := getenv("AIROUTER_MAX_REQUEST_BYTES"); v != "" {
		n, e := strconv.ParseInt(v, 10, 64)
		if e != nil || n <= 0 {
			return Config{}, errors.New("AIROUTER_MAX_REQUEST_BYTES must be positive")
		}
		c.MaxRequestBytes = n
	}
	path := getenv("AIROUTER_CLIENTS_FILE")
	if path == "" {
		// 配置了数据库时认证走数据库，权限文件不再是必需的。
		// 两者都缺失才是配置错误：没有任何可用的调用者来源。
		if c.DatabaseURL == "" {
			return Config{}, errors.New("AIROUTER_CLIENTS_FILE or AIROUTER_DATABASE_URL is required")
		}
		return c, nil
	}
	raw, err := readFile(path)
	if err != nil {
		return Config{}, errors.New("cannot read AIROUTER_CLIENTS_FILE")
	}
	if len(raw) > 1<<20 {
		return Config{}, errors.New("client configuration exceeds size limit")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&c.Clients); err != nil {
		return Config{}, errors.New("invalid client configuration JSON")
	}
	if err = dec.Decode(new(any)); err != io.EOF {
		return Config{}, errors.New("client configuration must contain one JSON value")
	}
	if err = ValidateClients(c.Clients, c.CPAAPIKey); err != nil {
		return Config{}, err
	}
	return c, nil
}

// ParseBaseURL only accepts an origin, never a request-controlled path or query.
//
// HTTP 明文仅允许两类目标，因为 CPA_API_KEY 会以 Bearer 明文发送：
//   - 回环地址（本机或 SSH 隧道）
//   - 私有内网：RFC1918 网段，或不含点号的单标签主机名（Docker 网络别名）
//
// 公网目标必须使用 HTTPS。
func ParseBaseURL(raw string) (*url.URL, error) {
	bad := errors.New("CPA_BASE_URL must be an HTTPS origin, a loopback HTTP origin, or a private-network HTTP origin")
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || (u.Path != "" && u.Path != "/") {
		return nil, bad
	}
	if u.Scheme != "https" {
		if u.Scheme != "http" || !isTrustedHTTPHost(u.Hostname()) {
			return nil, bad
		}
	}
	if p := u.Port(); p != "" {
		n, e := strconv.Atoi(p)
		if e != nil || n < 1 || n > 65535 {
			return nil, bad
		}
	}
	u.Path = ""
	u.RawPath = ""
	return u, nil
}

// isTrustedHTTPHost 判断明文 HTTP 目标是否可接受。
// 只放行回环、RFC1918 私有网段，以及不含点号的单标签主机名
// （Docker 网络别名，如 cpa、cliproxyapi）。
// 单标签主机名在公网 DNS 中无法解析，因此不会把内部凭据发送到公网。
func isTrustedHTTPHost(host string) bool {
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
	}
	// 单标签主机名：不含点，且非空。例如 cpa、cliproxyapi、airouter-db。
	return !strings.Contains(host, ".")
}

func ValidateClients(clients []Client, upstreamKey string) error {
	if len(clients) == 0 {
		return errors.New("at least one client is required")
	}
	ids := map[string]bool{}
	digests := map[string]bool{}
	upstream := sha256.Sum256([]byte(upstreamKey))
	for _, c := range clients {
		if c.ID == "" || strings.TrimSpace(c.ID) != c.ID || ids[c.ID] {
			return errors.New("client IDs must be nonempty and unique")
		}
		ids[c.ID] = true
		raw, err := hex.DecodeString(c.KeySHA256)
		digest := strings.ToLower(c.KeySHA256)
		if err != nil || len(raw) != sha256.Size || digests[digest] {
			return errors.New("client key digests must be unique SHA256 hex values")
		}
		digests[digest] = true
		if upstreamKey != "" && bytes.Equal(raw, upstream[:]) {
			return errors.New("gateway and CPA credentials must be different")
		}
		seen := map[string]bool{}
		for _, model := range c.AllowedModels {
			if model == "" || strings.TrimSpace(model) != model || strings.ContainsAny(model, "\r\n\t") || seen[model] {
				return errors.New("model permissions must be nonempty unique IDs")
			}
			seen[model] = true
		}
	}
	return nil
}

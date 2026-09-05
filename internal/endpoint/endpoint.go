package endpoint

import (
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"github.com/langbot-app/langbot-cli/internal/result"
)

func Normalize(value string) (string, error) {
	invalid := func() (string, error) {
		return "", result.New("input", "endpoint 必须是有效的 HTTP(S) 服务根地址，不含用户信息、查询或片段")
	}
	if value == "" || strings.IndexFunc(value, unicode.IsControl) >= 0 || strings.ContainsAny(value, "\\#") {
		return invalid()
	}
	u, err := url.Parse(value)
	if err != nil || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery {
		return invalid()
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || strings.Contains(u.Host, " ") {
		return invalid()
	}
	host, port := strings.ToLower(u.Hostname()), u.Port()
	if strings.HasSuffix(u.Host, ":") {
		return invalid()
	}
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return invalid()
		}
		port = strconv.Itoa(n)
		if (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443") {
			port = ""
		}
	}
	if strings.Contains(host, ":") && net.ParseIP(host) == nil {
		return invalid()
	}
	u.Host = host
	if port != "" {
		u.Host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		u.Host = "[" + host + "]"
	}
	// 拒绝代理可能再次解码的分隔符，保持配置比较与实际请求目标一致。
	escaped := strings.ToLower(u.EscapedPath())
	if strings.Contains(escaped, "%2f") || strings.ContainsAny(u.Path, "\\%") || strings.IndexFunc(u.Path, unicode.IsControl) >= 0 {
		return invalid()
	}
	for _, segment := range strings.Split(u.Path, "/") {
		if segment == "." || segment == ".." {
			return invalid()
		}
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u.String(), nil
}

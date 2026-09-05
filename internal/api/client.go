package api

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/langbot-app/langbot-cli/internal/endpoint"
	"github.com/langbot-app/langbot-cli/internal/result"
)

const (
	infoPath          = "/api/v1/system/info"
	defaultTimeout    = 30 * time.Second
	maxResponseBytes  = 1 << 20
	maxServerCodeSize = 128
	maxRequestIDSize  = 128
)

// Target 是一次请求使用的不可变连接快照。
type Target struct {
	Endpoint string        `json:"endpoint" yaml:"endpoint"`
	APIKey   string        `json:"-" yaml:"-"`
	Timeout  time.Duration `json:"timeout" yaml:"timeout"`
}

// Client 只封装 HTTP 传输策略，不保存 context 或凭据状态。
type Client struct {
	Transport http.RoundTripper
}

// Info 是 /system/info 中 CLI 当前需要的稳定字段。
type Info struct {
	Version string `json:"version" yaml:"version"`
	Edition string `json:"edition" yaml:"edition"`
}

type envelope struct {
	Code      json.RawMessage `json:"code"`
	Msg       string          `json:"msg"`
	Data      json.RawMessage `json:"data"`
	RequestID string          `json:"request_id"`
}

type response struct {
	StatusCode int
	Header     http.Header
	Body       envelope
	Data       map[string]any
}

// Info 请求已确认的公开诊断接口。该接口返回成功不代表 API Key 有效。
func (c Client) Info(ctx context.Context, target Target) (Info, error) {
	resp, err := c.fetch(ctx, target, http.MethodGet, infoPath)
	if err != nil {
		return Info{}, err
	}
	data := projectInfo(resp.Data, target.APIKey)
	if safeString(data["version"]) == "" {
		return Info{}, protocolError("服务返回的诊断数据缺少有效版本", resp.StatusCode, responseRequestID(resp.Header, resp.Body, target.APIKey))
	}

	return Info{
		Version: safeString(data["version"]),
		Edition: safeString(data["edition"]),
	}, nil
}

// Raw 返回 /system/info 的安全投影。接口有意不接受任意路径，避免把 Raw 变成
// 绕过操作定义的通用 HTTP 入口。
func (c Client) Raw(ctx context.Context, target Target, method, path string) (any, error) {
	if method != http.MethodGet || path != infoPath {
		return nil, result.New("incompatible", "只允许读取已确认的 system/info 接口")
	}
	resp, err := c.fetch(ctx, target, method, path)
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"code": 0,
		"msg":  "ok",
		"data": projectInfo(resp.Data, target.APIKey),
	}, nil
}

func (c Client) fetch(ctx context.Context, target Target, method, path string) (response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if method != http.MethodGet || path != infoPath {
		return response{}, result.New("incompatible", "只允许读取已确认的 system/info 接口")
	}
	base, err := endpoint.Normalize(target.Endpoint)
	if err != nil {
		return response{}, result.New("input", "服务地址无效")
	}
	requestURL := strings.TrimRight(base, "/") + infoPath

	request, err := http.NewRequestWithContext(ctx, method, requestURL, nil)
	if err != nil {
		return response{}, result.New("input", "无法构造 HTTP 请求")
	}
	if target.APIKey != "" {
		request.Header.Set("X-API-Key", target.APIKey)
	}

	transport := c.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	httpClient := &http.Client{
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	timeout := target.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request = request.WithContext(callCtx)

	resp, err := httpClient.Do(request)
	if err != nil {
		return response{}, transportError(callCtx, err)
	}
	defer resp.Body.Close()

	body, tooLarge, readErr := readLimited(resp.Body)
	if readErr != nil {
		err := transportError(callCtx, readErr)
		err.HTTPStatus = resp.StatusCode
		err.RequestID = responseRequestID(resp.Header, envelope{}, target.APIKey)
		return response{}, err
	}
	if tooLarge {
		err := protocolError("服务响应过大", resp.StatusCode, responseRequestID(resp.Header, envelope{}, target.APIKey))
		return response{}, err
	}
	parsed, parseErr := parseEnvelope(body)
	requestID := responseRequestID(resp.Header, parsed, target.APIKey)
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return response{}, httpError(resp.StatusCode, parsed.Code, requestID, target.APIKey)
	}
	if parseErr != nil {
		return response{}, protocolError("服务返回的响应格式无效", resp.StatusCode, requestID)
	}
	if !isSuccessCode(parsed.Code) {
		return response{}, businessError(parsed.Code, requestID, resp.StatusCode, target.APIKey)
	}
	data, ok := parseDataObject(parsed.Data)
	if !ok {
		return response{}, protocolError("服务返回的诊断数据格式无效", resp.StatusCode, requestID)
	}
	if !validInfoData(data, target.APIKey) {
		return response{}, protocolError("服务返回的诊断数据缺少有效版本", resp.StatusCode, requestID)
	}
	return response{
		StatusCode: resp.StatusCode,
		Header:     resp.Header,
		Body:       parsed,
		Data:       data,
	}, nil
}

func readLimited(body io.Reader) ([]byte, bool, error) {
	limited := io.LimitReader(body, maxResponseBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, false, err
	}
	if len(data) > maxResponseBytes {
		return nil, true, nil
	}
	return data, false, nil
}

func parseEnvelope(body []byte) (envelope, error) {
	var value map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil || value == nil {
		return envelope{}, errors.New("response is not a JSON object")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return envelope{}, errors.New("response has trailing JSON")
	}
	var parsed envelope
	parsed.Code = value["code"]
	parsed.Data = value["data"]
	if raw := value["msg"]; len(raw) != 0 {
		_ = json.Unmarshal(raw, &parsed.Msg)
	}
	if raw := value["request_id"]; len(raw) != 0 {
		_ = json.Unmarshal(raw, &parsed.RequestID)
	}
	if len(parsed.Code) == 0 {
		return envelope{}, errors.New("response has no code")
	}
	return parsed, nil
}

func parseDataObject(raw json.RawMessage) (map[string]any, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, false
	}
	var data map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&data); err != nil || data == nil {
		return nil, false
	}
	return data, true
}

func isSuccessCode(raw json.RawMessage) bool {
	var number json.Number
	if err := json.Unmarshal(raw, &number); err == nil && number.String() == "0" {
		return true
	}
	var text string
	return json.Unmarshal(raw, &text) == nil && text == "0"
}

func httpError(status int, code json.RawMessage, requestID, secret string) *result.Error {
	kind := "server"
	switch status {
	case http.StatusUnauthorized:
		kind = "auth"
	case http.StatusForbidden:
		kind = "permission"
	case http.StatusNotFound:
		kind = "not_found"
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		kind = "incompatible"
	case http.StatusRequestTimeout, http.StatusTooManyRequests:
		kind = "network"
	}
	err := result.New(kind, httpStatusMessage(status))
	err.HTTPStatus = status
	err.ServerCode = safeServerCode(code, secret)
	err.RequestID = requestID
	return err
}

func businessError(code json.RawMessage, requestID string, status int, secret string) *result.Error {
	err := result.New("server", "服务拒绝了诊断请求")
	err.HTTPStatus = status
	err.ServerCode = safeServerCode(code, secret)
	err.RequestID = requestID
	return err
}

func protocolError(message string, status int, requestID string) *result.Error {
	err := result.New("incompatible", message)
	err.HTTPStatus = status
	err.RequestID = requestID
	return err
}

func transportMessage(ctx context.Context, err error) string {
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return "请求已取消"
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return "请求超时"
	}
	return "无法连接服务"
}

func transportError(ctx context.Context, err error) *result.Error {
	if isTLSError(err) {
		failure := result.New("network", "TLS 证书验证失败")
		failure.Type = "tls_error"
		return failure
	}
	return result.New("network", transportMessage(ctx, err))
}

func isTLSError(err error) bool {
	var verificationError *tls.CertificateVerificationError
	if errors.As(err, &verificationError) {
		return true
	}
	var unknownAuthority x509.UnknownAuthorityError
	if errors.As(err, &unknownAuthority) {
		return true
	}
	var hostnameError x509.HostnameError
	if errors.As(err, &hostnameError) {
		return true
	}
	var certificateInvalid x509.CertificateInvalidError
	return errors.As(err, &certificateInvalid)
}

func responseRequestID(header http.Header, body envelope, secret string) string {
	if id := safeRequestID(header.Get("X-Request-Id"), secret); id != "" {
		return id
	}
	return safeRequestID(body.RequestID, secret)
}

func safeServerCode(raw json.RawMessage, secret string) any {
	if len(raw) == 0 {
		return nil
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err == nil {
		if value, err := strconv.ParseInt(number.String(), 10, 64); err == nil {
			if containsSecret(strconv.FormatInt(value, 10), secret) {
				return nil
			}
			return value
		}
	}
	var text string
	if json.Unmarshal(raw, &text) != nil || len(text) == 0 || len(text) > maxServerCodeSize || containsSecret(text, secret) {
		return nil
	}
	for _, r := range text {
		if !(r == '_' || r == '-' || r == '.' || r == ':' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return nil
		}
	}
	return text
}

func safeRequestID(value, secret string) string {
	if len(value) == 0 || len(value) > maxRequestIDSize || containsSecret(value, secret) {
		return ""
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return ""
		}
	}
	return value
}

func containsSecret(value, secret string) bool {
	return secret != "" && strings.Contains(value, secret)
}

func projectInfo(data map[string]any, secret string) map[string]any {
	projected := make(map[string]any, 7)
	for _, key := range []string{
		"version",
		"edition",
		"debug",
		"enable_marketplace",
		"disable_models_service",
		"mcp_stdio_enabled",
		"wizard_status",
	} {
		value, ok := data[key]
		if !ok {
			continue
		}
		switch key {
		case "version", "edition", "wizard_status":
			text, ok := value.(string)
			if ok && !containsSecret(text, secret) {
				projected[key] = value
			}
		default:
			if _, ok := value.(bool); ok {
				projected[key] = value
			}
		}
	}
	return projected
}

func validInfoData(data map[string]any, secret string) bool {
	version, ok := data["version"].(string)
	return ok && version != "" && !containsSecret(version, secret)
}

func safeString(value any) string {
	text, _ := value.(string)
	return text
}

func httpStatusMessage(status int) string {
	if status >= 300 && status < 400 {
		return "服务返回了重定向，CLI 不会自动跟随"
	}
	return fmt.Sprintf("服务返回 HTTP %d", status)
}

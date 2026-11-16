package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/langbot-app/langbot-cli/internal/clientidentity"
	"github.com/langbot-app/langbot-cli/internal/endpoint"
	"github.com/langbot-app/langbot-cli/internal/result"
)

const operationsPath = "/api/v1/operations"

func ValidateOperationID(identifier string) error {
	if identifier == "" || strings.IndexFunc(identifier, func(character rune) bool {
		return !(character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '_' || character == '.')
	}) >= 0 || strings.Contains(identifier, "..") {
		return result.New("input", "操作 ID 必须由小写字母、数字、下划线和点组成")
	}
	return nil
}

func (c Client) applicationRequest(ctx context.Context, target Target, method, path string, body io.Reader, contentType string) (*http.Response, context.CancelFunc, error) {
	base, err := endpoint.Normalize(target.Endpoint)
	if err != nil {
		return nil, nil, result.New("input", "服务地址无效")
	}
	timeout := target.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	request, err := http.NewRequestWithContext(callCtx, method, strings.TrimRight(base, "/")+path, body)
	if err != nil {
		cancel()
		return nil, nil, result.New("input", "无法构造操作请求")
	}
	if target.UserToken != "" {
		request.Header.Set("Authorization", "Bearer "+target.UserToken)
	} else if target.APIKey != "" {
		request.Header.Set("X-API-Key", target.secret())
	}
	request.Header.Set("Content-Type", contentType)
	clientidentity.Apply(request)
	client := &http.Client{Transport: c.Transport, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		failure := transportError(callCtx, err)
		cancel()
		return nil, nil, failure
	}
	return response, cancel, nil
}

func applicationBody(body map[string]any) (io.Reader, error) {
	encoded, err := json.Marshal(bodyOrEmpty(body))
	if err != nil || len(encoded) > maxRequestBytes {
		return nil, result.New("input", "操作请求体无效或过大")
	}
	return bytes.NewReader(encoded), nil
}

func applicationResponse(response *http.Response, target Target, bare bool) (any, error) {
	data, tooLarge, err := readLimited(response.Body)
	if err != nil || tooLarge {
		return nil, result.New("network", "读取操作响应失败或响应过大")
	}
	parsed, parseErr := parseEnvelope(data)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if target.UserToken != "" && (response.StatusCode == 401 || response.StatusCode == 403) {
			return nil, result.RequiresMemberSession()
		}
		return nil, httpError(response.StatusCode, parsed.Code, responseRequestID(response.Header, parsed, target.secret()), target.secret())
	}
	if !bare {
		if parseErr != nil {
			return nil, result.New("incompatible", "操作响应格式无效")
		}
		if !isSuccessCode(parsed.Code) {
			return nil, businessError(parsed.Code, responseRequestID(response.Header, parsed, target.secret()), response.StatusCode, target.secret())
		}
		data = parsed.Data
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, result.New("incompatible", "操作响应不是有效 JSON")
	}
	return redactConnectionSecret(value, target.secret()), nil
}

func (c Client) OperationList(ctx context.Context, target Target, domain, search string) (any, error) {
	query := url.Values{}
	if domain != "" {
		query.Set("domain", domain)
	}
	if search != "" {
		query.Set("q", search)
	}
	return c.applicationJSON(ctx, target, http.MethodGet, operationsPath+"?"+query.Encode(), nil, true)
}

func (c Client) OperationDescribe(ctx context.Context, target Target, identifier string) (map[string]any, error) {
	if err := ValidateOperationID(identifier); err != nil {
		return nil, err
	}
	value, err := c.applicationJSON(ctx, target, http.MethodGet, operationsPath+"/"+identifier, nil, true)
	if err != nil {
		return nil, err
	}
	description, ok := value.(map[string]any)
	if !ok || description["id"] != identifier {
		return nil, result.New("incompatible", "操作描述格式无效")
	}
	return description, nil
}

func (c Client) OperationCall(ctx context.Context, target Target, identifier string, body map[string]any) (any, error) {
	if err := ValidateOperationID(identifier); err != nil {
		return nil, err
	}
	return c.applicationJSON(ctx, target, http.MethodPost, operationsPath+"/"+identifier, body, false)
}

func (c Client) applicationJSON(ctx context.Context, target Target, method, path string, body map[string]any, bare bool) (any, error) {
	var reader io.Reader
	if method != http.MethodGet {
		var err error
		reader, err = applicationBody(body)
		if err != nil {
			return nil, err
		}
	}
	response, cancel, err := c.applicationRequest(ctx, target, method, path, reader, "application/json")
	if err != nil {
		return nil, err
	}
	defer cancel()
	defer response.Body.Close()
	return applicationResponse(response, target, bare)
}

func (c Client) OperationStream(ctx context.Context, target Target, identifier string, body map[string]any, emit func(any) error) error {
	if err := ValidateOperationID(identifier); err != nil {
		return err
	}
	reader, err := applicationBody(body)
	if err != nil {
		return err
	}
	response, cancel, err := c.applicationRequest(ctx, target, http.MethodPost, operationsPath+"/"+identifier+"/stream", reader, "application/json")
	if err != nil {
		return err
	}
	defer cancel()
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, err = applicationResponse(response, target, false)
		return err
	}
	if !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		return result.New("incompatible", "操作未返回 SSE 流")
	}
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 4096), maxResponseBytes)
	event, data := "", ""
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			if len(data)+len(line) > maxResponseBytes {
				return result.New("incompatible", "SSE 事件过大")
			}
			data += strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " ") + "\n"
			continue
		}
		if line != "" {
			continue
		}
		if event == "error" {
			failure := result.New("server", "流式操作失败；请检查操作日志")
			var detail struct {
				Code json.RawMessage `json:"code"`
			}
			if json.Unmarshal([]byte(data), &detail) == nil {
				failure.ServerCode = safeServerCode(detail.Code, target.secret())
			}
			return failure
		}
		if event == "done" {
			return nil
		}
		if data != "" {
			var value any
			decoder := json.NewDecoder(strings.NewReader(data))
			decoder.UseNumber()
			if decoder.Decode(&value) != nil {
				return result.New("incompatible", "SSE 事件不是有效 JSON")
			}
			if err := emit(redactConnectionSecret(value, target.secret())); err != nil {
				return err
			}
		}
		event, data = "", ""
	}
	if scanner.Err() != nil {
		return transportError(ctx, scanner.Err())
	}
	return result.New("network", "SSE 流在 done 事件前断开；请勿自动重试")
}

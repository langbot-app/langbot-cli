package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/langbot-app/langbot-cli/internal/clientidentity"
	"github.com/langbot-app/langbot-cli/internal/endpoint"
	"github.com/langbot-app/langbot-cli/internal/i18n"
	"github.com/langbot-app/langbot-cli/internal/result"
)

const operationsPath = "/api/v1/operations"

// MaxArtifactBytes matches Core's authenticated Artifact read limit.
const MaxArtifactBytes int64 = 10 << 20

func ValidateOperationID(identifier string) error {
	if identifier == "" || strings.IndexFunc(identifier, func(character rune) bool {
		return !(character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '_' || character == '.')
	}) >= 0 || strings.Contains(identifier, "..") {
		return result.New("input", i18n.T("Operation ID must contain only lowercase letters, digits, underscores and dots"))
	}
	return nil
}

func (c Client) applicationRequest(ctx context.Context, target Target, method, path string, body io.Reader, contentType string) (*http.Response, context.CancelFunc, error) {
	base, err := endpoint.Normalize(target.Endpoint)
	if err != nil {
		return nil, nil, result.New("input", i18n.T("Invalid service URL"))
	}
	timeout := target.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	request, err := http.NewRequestWithContext(callCtx, method, strings.TrimRight(base, "/")+path, body)
	if err != nil {
		cancel()
		return nil, nil, result.New("input", i18n.T("Unable to construct operation request"))
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
		return nil, result.New("input", i18n.T("Operation request body is invalid or too large"))
	}
	return bytes.NewReader(encoded), nil
}

func applicationResponse(response *http.Response, target Target, bare bool) (any, error) {
	data, tooLarge, err := readLimited(response.Body)
	if err != nil || tooLarge {
		return nil, result.New("network", i18n.T("Failed to read operation response, or response too large"))
	}
	parsed, parseErr := parseEnvelope(data)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		if target.UserToken != "" && (response.StatusCode == 401 || response.StatusCode == 403) {
			failure := result.RequiresMemberSession()
			failure.HTTPStatus = response.StatusCode
			failure.RequestID = responseRequestID(response.Header, parsed, target.secret())
			return nil, failure
		}
		return nil, httpError(response.StatusCode, parsed.Code, responseRequestID(response.Header, parsed, target.secret()), target.secret())
	}
	if !bare {
		if parseErr != nil {
			return nil, result.New("incompatible", i18n.T("Invalid operation response format"))
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
		return nil, result.New("incompatible", i18n.T("Operation response is not valid JSON"))
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
		return nil, result.New("incompatible", i18n.T("Invalid operation description format"))
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
		return result.New("incompatible", i18n.T("Operation did not return an SSE stream"))
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
				return result.New("incompatible", i18n.T("SSE event is too large"))
			}
			data += strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " ") + "\n"
			continue
		}
		if line != "" {
			continue
		}
		if event == "error" {
			failure := result.New("server", i18n.T("Streaming operation failed; inspect operation logs"))
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
				return result.New("incompatible", i18n.T("SSE event is not valid JSON"))
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
	return result.New("network", i18n.T("SSE stream disconnected before done; do not retry automatically"))
}

// ArtifactDownload streams authorized binary content without JSON or text conversion.
func (c Client) ArtifactDownload(ctx context.Context, target Target, identifier string, limit int64, destination io.Writer) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	path, err := resourcePath("/api/v1/artifacts", identifier)
	if err != nil {
		return 0, err
	}
	if limit < 1 || limit > MaxArtifactBytes || destination == nil {
		return 0, result.New("input", i18n.T("Invalid Artifact download limit or destination"))
	}
	response, cancel, err := c.applicationRequest(ctx, target, http.MethodGet, path+"/content?max_bytes="+strconv.FormatInt(limit, 10), nil, "")
	if err != nil {
		return 0, err
	}
	defer cancel()
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, err := applicationResponse(response, target, false)
		return 0, err
	}
	written, copyErr := io.Copy(destination, io.LimitReader(response.Body, limit+1))
	if copyErr != nil {
		callContext := ctx
		if response.Request != nil {
			callContext = response.Request.Context()
		}
		failure := transportError(callContext, copyErr)
		failure.HTTPStatus = response.StatusCode
		failure.RequestID = safeRequestID(response.Header.Get("X-Request-Id"), target.secret())
		return written, failure
	}
	if written > limit {
		failure := result.New("incompatible", i18n.T("Artifact content exceeds the download limit"))
		failure.Type, failure.HTTPStatus = "artifact_too_large", response.StatusCode
		failure.RequestID = safeRequestID(response.Header.Get("X-Request-Id"), target.secret())
		return written, failure
	}
	return written, nil
}

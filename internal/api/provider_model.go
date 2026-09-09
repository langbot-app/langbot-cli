package api

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/langbot-app/langbot-cli/internal/result"
)

const (
	ModelTypeLLM       = "llm"
	ModelTypeEmbedding = "embedding"
	ModelTypeRerank    = "rerank"
)

// ValidateModelType 将模型请求限制在服务端支持的三类模型资源内。
func ValidateModelType(modelType string) error {
	switch modelType {
	case ModelTypeLLM, ModelTypeEmbedding, ModelTypeRerank:
		return nil
	default:
		return result.New("input", "model type 必须是 llm、embedding 或 rerank")
	}
}

func modelBasePath(modelType string) (string, error) {
	if err := ValidateModelType(modelType); err != nil {
		return "", err
	}
	switch modelType {
	case ModelTypeLLM:
		return llmModelsPath, nil
	case ModelTypeEmbedding:
		return embeddingModelsPath, nil
	default:
		return rerankModelsPath, nil
	}
}

func (c Client) Providers(ctx context.Context, target Target) ([]map[string]any, error) {
	resp, err := c.fetch(ctx, target, http.MethodGet, providersPath+"?include_secret=false")
	if err != nil {
		return nil, err
	}
	raw, ok := resp.Data["providers"].([]any)
	if !ok {
		return nil, protocolError("服务返回的 Provider 列表格式无效", resp.StatusCode, responseRequestID(resp.Header, resp.Body, target.APIKey))
	}
	items := make([]map[string]any, 0, len(raw))
	for _, value := range raw {
		item, ok := value.(map[string]any)
		if !ok || !validResourceUUID(item, target.APIKey) {
			return nil, protocolError("服务返回的 Provider 格式无效", resp.StatusCode, responseRequestID(resp.Header, resp.Body, target.APIKey))
		}
		items = append(items, projectProvider(item, target.APIKey))
	}
	return items, nil
}

func (c Client) Provider(ctx context.Context, target Target, uuid string) (map[string]any, error) {
	path, err := resourcePath(providersPath, uuid)
	if err != nil {
		return nil, err
	}
	resp, err := c.fetch(ctx, target, http.MethodGet, path+"?include_secret=false")
	if err != nil {
		return nil, err
	}
	item, ok := resp.Data["provider"].(map[string]any)
	if !ok || !validResourceUUID(item, target.APIKey) {
		return nil, protocolError("服务返回的 Provider 格式无效", resp.StatusCode, responseRequestID(resp.Header, resp.Body, target.APIKey))
	}
	projected := projectProvider(item, target.APIKey)
	if projected["uuid"] != uuid {
		return nil, protocolError("服务返回的 Provider UUID 与请求不一致", resp.StatusCode, responseRequestID(resp.Header, resp.Body, target.APIKey))
	}
	return projected, nil
}

func (c Client) ProviderCreate(ctx context.Context, target Target, body map[string]any) (WriteResult, error) {
	return c.createResource(ctx, target, http.MethodPost, providersPath, body)
}

func (c Client) ProviderUpdate(ctx context.Context, target Target, uuid string, body map[string]any) error {
	_, err := c.updateResource(ctx, target, http.MethodPut, providersPath, uuid, body)
	return err
}

func (c Client) ProviderDelete(ctx context.Context, target Target, uuid string) error {
	_, err := c.updateResource(ctx, target, http.MethodDelete, providersPath, uuid, nil)
	return err
}

func (c Client) ProviderScanModels(ctx context.Context, target Target, uuid, modelType string) (map[string]any, error) {
	path, err := resourcePath(providersPath, uuid)
	if err != nil {
		return nil, err
	}
	query := url.Values{}
	if strings.TrimSpace(modelType) != "" {
		query.Set("type", modelType)
	}
	path += "/scan-models"
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	resp, err := c.fetch(ctx, target, http.MethodGet, path)
	if err != nil {
		return nil, err
	}
	raw, ok := resp.Data["models"].([]any)
	if !ok {
		return nil, protocolError("服务返回的扫描模型列表格式无效", resp.StatusCode, responseRequestID(resp.Header, resp.Body, target.APIKey))
	}
	models := make([]map[string]any, 0, len(raw))
	for _, value := range raw {
		model, ok := value.(map[string]any)
		if !ok {
			return nil, protocolError("服务返回的扫描模型格式无效", resp.StatusCode, responseRequestID(resp.Header, resp.Body, target.APIKey))
		}
		models = append(models, projectSafeFields(model, []string{
			"id", "name", "type", "abilities", "display_name", "description", "context_length", "owned_by",
			"input_modalities", "output_modalities", "already_added",
		}, target.APIKey))
	}
	return map[string]any{"models": models}, nil
}

func (c Client) Models(ctx context.Context, target Target, modelType, providerUUID string) ([]map[string]any, error) {
	base, err := modelBasePath(modelType)
	if err != nil {
		return nil, err
	}
	query := url.Values{}
	query.Set("include_secret", "false")
	if strings.TrimSpace(providerUUID) != "" {
		query.Set("provider_uuid", providerUUID)
	}
	resp, err := c.fetch(ctx, target, http.MethodGet, base+"?"+query.Encode())
	if err != nil {
		return nil, err
	}
	raw, ok := resp.Data["models"].([]any)
	if !ok {
		return nil, protocolError("服务返回的 Model 列表格式无效", resp.StatusCode, responseRequestID(resp.Header, resp.Body, target.APIKey))
	}
	items := make([]map[string]any, 0, len(raw))
	for _, value := range raw {
		item, ok := value.(map[string]any)
		if !ok || !validResourceUUID(item, target.APIKey) {
			return nil, protocolError("服务返回的 Model 格式无效", resp.StatusCode, responseRequestID(resp.Header, resp.Body, target.APIKey))
		}
		items = append(items, projectModel(item, target.APIKey))
	}
	return items, nil
}

func (c Client) Model(ctx context.Context, target Target, modelType, uuid string) (map[string]any, error) {
	base, err := modelBasePath(modelType)
	if err != nil {
		return nil, err
	}
	path, err := resourcePath(base, uuid)
	if err != nil {
		return nil, err
	}
	resp, err := c.fetch(ctx, target, http.MethodGet, path+"?include_secret=false")
	if err != nil {
		return nil, err
	}
	item, ok := resp.Data["model"].(map[string]any)
	if !ok || !validResourceUUID(item, target.APIKey) {
		return nil, protocolError("服务返回的 Model 格式无效", resp.StatusCode, responseRequestID(resp.Header, resp.Body, target.APIKey))
	}
	projected := projectModel(item, target.APIKey)
	if projected["uuid"] != uuid {
		return nil, protocolError("服务返回的 Model UUID 与请求不一致", resp.StatusCode, responseRequestID(resp.Header, resp.Body, target.APIKey))
	}
	return projected, nil
}

func (c Client) ModelCreate(ctx context.Context, target Target, modelType string, body map[string]any) (WriteResult, error) {
	base, err := modelBasePath(modelType)
	if err != nil {
		return WriteResult{}, err
	}
	return c.createResource(ctx, target, http.MethodPost, base, body)
}

func (c Client) ModelUpdate(ctx context.Context, target Target, modelType, uuid string, body map[string]any) error {
	base, err := modelBasePath(modelType)
	if err != nil {
		return err
	}
	_, err = c.updateResource(ctx, target, http.MethodPut, base, uuid, body)
	return err
}

func (c Client) ModelDelete(ctx context.Context, target Target, modelType, uuid string) error {
	base, err := modelBasePath(modelType)
	if err != nil {
		return err
	}
	_, err = c.updateResource(ctx, target, http.MethodDelete, base, uuid, nil)
	return err
}

func (c Client) ModelTest(ctx context.Context, target Target, modelType, uuid string, body map[string]any) (any, error) {
	base, err := modelBasePath(modelType)
	if err != nil {
		return nil, err
	}
	path, err := resourcePath(base, uuid)
	if err != nil {
		return nil, err
	}
	resp, err := c.write(ctx, target, http.MethodPost, path+"/test", bodyOrEmpty(body))
	if err != nil {
		return nil, err
	}
	return redactSafeValue(resp.Data, target.APIKey), nil
}

func projectProvider(value map[string]any, secret string) map[string]any {
	projected := projectSafeFields(value, []string{
		"uuid", "name", "requester", "base_url", "api_keys", "created_at", "updated_at",
		"llm_count", "embedding_count", "rerank_count",
	}, secret)
	if baseURL, ok := projected["base_url"].(string); ok {
		projected["base_url"] = redactProviderURL(baseURL, secret)
	}
	return projected
}

func projectModel(value map[string]any, secret string) map[string]any {
	return projectSafeFields(value, []string{
		"uuid", "name", "provider_uuid", "abilities", "context_length", "reasoning_config",
		"reasoning_capabilities", "extra_args", "prefered_ranking", "created_at", "updated_at", "provider",
	}, secret)
}

func projectSafeFields(value map[string]any, fields []string, secret string) map[string]any {
	projected := make(map[string]any, len(fields))
	for _, field := range fields {
		item, ok := value[field]
		if ok {
			projected[field] = redactSafeValueForField(item, field, secret)
		}
	}
	return projected
}

func redactSafeValue(value any, secret string) any {
	return redactSafeValueForField(value, "", secret)
}

func redactSafeValueForField(value any, field, secret string) any {
	if isSensitiveField(field) {
		return maskSensitiveValue(value)
	}
	switch typed := value.(type) {
	case map[string]any:
		projected := make(map[string]any, len(typed))
		for key, item := range typed {
			projected[key] = redactSafeValueForField(item, key, secret)
		}
		return projected
	case []any:
		items := make([]any, len(typed))
		for index, item := range typed {
			items[index] = redactSafeValueForField(item, "", secret)
		}
		return items
	case string:
		if secret != "" {
			return strings.ReplaceAll(typed, secret, "***")
		}
	}
	return value
}

func isSensitiveField(field string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(field, "-", "_"), " ", "_"))
	for _, token := range []string{"api_key", "apikey", "token", "password", "secret", "authorization", "credential", "private_key"} {
		if strings.Contains(normalized, token) {
			return true
		}
	}
	return false
}

func maskSensitiveValue(value any) any {
	switch typed := value.(type) {
	case []any:
		items := make([]any, len(typed))
		for index := range typed {
			items[index] = "***"
		}
		return items
	case map[string]any:
		return "***"
	default:
		return "***"
	}
}

func redactProviderURL(value, secret string) string {
	parsed, err := url.Parse(value)
	if err != nil {
		return strings.ReplaceAll(value, secret, "***")
	}
	if parsed.User != nil {
		parsed.User = url.User("***")
	}
	query := parsed.Query()
	for key, values := range query {
		if isSensitiveField(key) {
			for index := range values {
				values[index] = "***"
			}
			query[key] = values
			continue
		}
		for index, item := range values {
			query[key][index] = strings.ReplaceAll(item, secret, "***")
		}
	}
	parsed.RawQuery = query.Encode()
	redacted := strings.ReplaceAll(parsed.String(), secret, "***")
	return strings.ReplaceAll(redacted, "%2A%2A%2A", "***")
}

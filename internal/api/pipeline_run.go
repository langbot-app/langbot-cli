package api

import (
	"context"
	"github.com/langbot-app/langbot-cli/internal/result"
	"net/http"
	"net/url"
)

func (c Client) PipelineRun(ctx context.Context, target Target, id, message string) (map[string]any, error) {
	if err := ValidateResourceID(id); err != nil {
		return nil, err
	}
	resp, err := c.write(ctx, target, http.MethodPost, pipelinesPath+"/"+url.PathEscape(id)+"/run", map[string]any{"message": message})
	if err != nil {
		return nil, err
	}
	data, ok := redactConnectionSecret(resp.Data, target.APIKey).(map[string]any)
	if !ok {
		return nil, result.New("incompatible", "试运行返回格式无效，执行状态未知；请勿自动重试")
	}
	return data, nil
}

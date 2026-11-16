package api

import (
	"context"
	"github.com/langbot-app/langbot-cli/internal/i18n"
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
	data, ok := redactConnectionSecret(resp.Data, target.secret()).(map[string]any)
	if !ok {
		return nil, result.New("incompatible", i18n.T("Invalid test response format; execution state unknown, do not retry automatically"))
	}
	return data, nil
}

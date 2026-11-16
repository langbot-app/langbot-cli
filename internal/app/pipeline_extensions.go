package app

import (
	"context"
	"github.com/langbot-app/langbot-cli/internal/api"
	"github.com/langbot-app/langbot-cli/internal/i18n"
	"github.com/langbot-app/langbot-cli/internal/result"
	"net/http"
	"net/url"
	"strings"
)

func (s *Service) PipelineExtensionsUpdate(ctx context.Context, id string, body map[string]any, dryRun bool, options CheckOptions) (Result, error) {
	if err := api.ValidateResourceID(id); err != nil {
		return Result{}, err
	}
	fields := []string{"bound_plugins", "bound_mcp_servers", "bound_skills", "bound_mcp_resources", "enable_all_plugins", "enable_all_mcp_servers", "enable_all_skills", "mcp_resource_agent_read_enabled"}
	if err := validateBodyFields(body, fields...); err != nil {
		return Result{}, err
	}
	for _, field := range fields {
		value, exists := body[field]
		if !exists {
			return Result{}, result.New("input", i18n.T("Extension bindings replace all values; required field: ")+field)
		}
		if strings.HasPrefix(field, "bound_") {
			items, ok := value.([]any)
			if !ok {
				return Result{}, result.New("input", field+i18n.T(" must be an array"))
			}
			for _, item := range items {
				if field == "bound_plugins" || field == "bound_mcp_resources" {
					if _, ok := item.(map[string]any); !ok {
						return Result{}, result.New("input", field+i18n.T(" must be an array of objects"))
					}
				} else if name, ok := item.(string); !ok || strings.TrimSpace(name) == "" {
					return Result{}, result.New("input", field+i18n.T(" must be a non-empty array of strings"))
				}
			}
		} else if _, ok := value.(bool); !ok {
			return Result{}, result.New("input", field+i18n.T(" must be a boolean"))
		}
	}
	preflight, err := s.writePreflight(ctx, "pipeline.extensions.update", "resource.manage", options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	if dryRun {
		return writePlan(preflight, "pipeline.extensions.update", id), nil
	}
	client, target := s.writeClient(preflight)
	if err := client.PipelineExtensionsUpdate(ctx, target, id, body); err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	op, _ := api.ResolveOperation(http.MethodGet, "/api/v1/pipelines/"+url.PathEscape(id)+"/extensions")
	data, err := client.APIRequest(ctx, target, op, nil)
	value := Result{Data: data, Meta: preflight.Meta()}
	if err != nil {
		return value, readbackError(err)
	}
	actual, ok := data.(map[string]any)
	if !ok || !observableFieldsEqual(body, actual, fields...) {
		return value, verificationError(i18n.T("Extension binding readback does not match the request"))
	}
	return value, nil
}

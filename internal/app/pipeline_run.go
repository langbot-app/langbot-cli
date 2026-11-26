package app

import (
	"context"
	"github.com/langbot-app/langbot-cli/internal/api"
	"github.com/langbot-app/langbot-cli/internal/i18n"
	"github.com/langbot-app/langbot-cli/internal/result"
	"strings"
)

func (s *Service) PipelineRun(ctx context.Context, id, message string, dryRun bool, options CheckOptions) (Result, error) {
	if err := api.ValidateResourceID(id); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(message) == "" {
		return Result{}, result.New("input", i18n.T("message must not be empty"))
	}
	preflight, err := s.writePreflight(ctx, "pipeline.run", "runtime.operate", options)
	if err != nil {
		return Result{Meta: preflight.Meta()}, err
	}
	if dryRun {
		return writePlan(preflight, "pipeline.run", id), nil
	}
	client, target := s.writeClient(preflight)
	data, err := client.PipelineRun(ctx, target, id, message)
	if err != nil {
		failure := result.AsError(err)
		if failure.Type == "result_unknown" || failure.Kind == "network" || failure.Kind == "incompatible" {
			return Result{Data: map[string]any{"status": "unknown", "pipeline_uuid": id}, Meta: preflight.Meta()}, err
		}
		return Result{Meta: preflight.Meta()}, err
	}
	value := Result{Data: data, Meta: preflight.Meta()}
	switch data["status"] {
	case "completed":
		return value, nil
	case "unknown":
		return value, result.New("network", i18n.T("Test timed out; server execution state unknown, do not retry automatically"))
	case "failed":
		return value, result.New("server", i18n.T("Chat execution failed; inspect the returned error and run history"))
	default:
		return value, result.New("incompatible", i18n.T("Test response lacks a valid status; execution state unknown, do not retry automatically"))
	}
}

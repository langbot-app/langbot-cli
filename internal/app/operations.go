package app

import (
	"context"
	"strings"

	"github.com/langbot-app/langbot-cli/internal/api"
	"github.com/langbot-app/langbot-cli/internal/i18n"
	"github.com/langbot-app/langbot-cli/internal/result"
)

type OperationSession struct {
	Client    api.Client
	Target    api.Target
	preflight WritePreflight
}

func (s *Service) OpenOperations(ctx context.Context, options CheckOptions, stream bool) (*OperationSession, error) {
	conn, identity, err := s.connectionWithIdentity(ctx, options)
	if err != nil {
		return nil, err
	}
	preflight := WritePreflight{Connection: conn, Identity: identity}
	if conn.UserToken != "" {
		return &OperationSession{Client: api.Client{Transport: s.deps.Transport}, Target: readTarget(preflight), preflight: preflight}, nil
	}
	capabilities, err := s.capabilities(ctx, conn)
	if err != nil {
		return nil, err
	}
	if !capabilities.Operations["operation.invoke"] || stream && !capabilities.Operations["operation.stream"] {
		return nil, result.New("incompatible", i18n.T("Requires LangBot Core with operation.invoke / operation.stream (Cloud or next Core version); existing commands remain available"))
	}
	preflight.Capabilities = capabilities
	return &OperationSession{Client: api.Client{Transport: s.deps.Transport}, Target: readTarget(preflight), preflight: preflight}, nil
}

func (session *OperationSession) Result(value any) Result {
	return Result{Data: value, Meta: session.preflight.Meta()}
}

func OperationWrites(description map[string]any) bool {
	risk, _ := description["risk"].(string)
	effect, _ := description["side_effect"].(bool)
	return effect || risk == "write" || risk == "high" || risk == "dangerous"
}

func (session *OperationSession) Authorize(description map[string]any) error {
	if permission, _ := description["permission"].(string); permission != "" && !hasPermission(session.preflight.Identity, permission) {
		return result.New("permission", i18n.T("Current API Key lacks ")+permission+i18n.T(" permission"))
	}
	if !OperationWrites(description) {
		return nil
	}
	conn := session.preflight.Connection
	if conn.Temporary || strings.TrimSpace(conn.ExpectedWorkspaceUUID) == "" {
		return result.New("precondition", i18n.T("Writes require a saved, Workspace-bound context"))
	}
	return workspaceBindingError(conn, session.preflight.Identity)
}

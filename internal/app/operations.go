package app

import (
	"context"
	"strings"

	"github.com/langbot-app/langbot-cli/internal/api"
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
	capabilities, err := s.capabilities(ctx, conn)
	if err != nil {
		return nil, err
	}
	if !capabilities.Operations["operation.invoke"] || stream && !capabilities.Operations["operation.stream"] {
		return nil, result.New("incompatible", "需要支持 operation.invoke / operation.stream 的 LangBot Core（Cloud 或下一个 Core 版本）；现有命令仍可使用")
	}
	preflight := WritePreflight{Connection: conn, Identity: identity, Capabilities: capabilities}
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
		return result.New("permission", "当前 API Key 缺少 "+permission+" 权限")
	}
	if !OperationWrites(description) {
		return nil
	}
	conn := session.preflight.Connection
	if conn.Temporary || strings.TrimSpace(conn.ExpectedWorkspaceUUID) == "" {
		return result.New("precondition", "写操作要求使用已绑定 Workspace 的保存 context")
	}
	return workspaceBindingError(conn, session.preflight.Identity)
}

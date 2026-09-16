package app

import (
	"context"
	"errors"
	"time"

	"github.com/langbot-app/langbot-cli/internal/deploy"
)

// LocalTargetOptions 固定一次本机部署操作的目录和超时。
type LocalTargetOptions struct {
	Dir        string
	Timeout    string
	TimeoutSet bool
}

func (s *Service) Status(ctx context.Context, options LocalTargetOptions) (Result, error) {
	diagnostics, err := s.localDiagnostics(options.Dir)
	if err != nil {
		return Result{}, err
	}
	timeout, err := operationTimeout(options.Timeout, options.TimeoutSet, 30*time.Second)
	if err != nil {
		return Result{}, err
	}
	operationCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	status, statusErr := diagnostics.Status(operationCtx)
	return Result{Data: status}, mapLocalError(statusErr)
}

func (s *Service) Doctor(ctx context.Context, options LocalTargetOptions) (Result, error) {
	diagnostics, err := s.localDiagnostics(options.Dir)
	if err != nil {
		return Result{}, err
	}
	timeout, err := operationTimeout(options.Timeout, options.TimeoutSet, 30*time.Second)
	if err != nil {
		return Result{}, err
	}
	operationCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	checks, doctorErr := diagnostics.Doctor(operationCtx)
	data := map[string]any{"dir": diagnostics.Dir, "checks": checks}
	return Result{Data: data}, mapLocalError(doctorErr)
}

func localRecordError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, deploy.ErrNotInstalled):
		return deploy.Problem{Kind: "precondition", Msg: "本地部署尚未安装"}
	case errors.Is(err, deploy.ErrNotManaged):
		return deploy.Problem{Kind: "precondition", Msg: "目标目录不是 lbctl 管理的部署"}
	case errors.Is(err, deploy.ErrCorrupt):
		return deploy.Problem{Kind: "precondition", Msg: "本地部署记录损坏"}
	default:
		return deploy.Problem{Kind: "precondition", Msg: "无法读取本地部署记录"}
	}
}

package app

import (
	"context"
	"net/url"
	"path/filepath"
	"strconv"
	"time"

	"github.com/langbot-app/langbot-cli/internal/deploy"
)

type LocalInstance struct {
	Name      string `json:"name"`
	Dir       string `json:"dir"`
	Port      int    `json:"port"`
	Status    string `json:"status"`
	Autostart *bool  `json:"autostart"`
}

func (s *Service) ListInstances(ctx context.Context, timeout string) (Result, error) {
	duration, err := operationTimeout(timeout, timeout != "", 5*time.Second)
	if err != nil {
		return Result{}, err
	}
	entries, err := s.localDeployments()
	if err != nil {
		return Result{}, err
	}
	instances := make([]LocalInstance, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return Result{Data: map[string]any{"instances": instances}}, err
		}
		item := LocalInstance{Dir: entry.Dir, Name: filepath.Base(entry.Dir), Status: "unknown"}
		record, err := (deploy.Store{Dir: entry.Dir}).Read()
		if err == nil {
			if record.Name != "" {
				item.Name = record.Name
			}
			item.Port = record.Port
			if item.Port == 0 {
				if endpoint, err := url.Parse(record.Endpoint); err == nil {
					item.Port, _ = strconv.Atoi(endpoint.Port())
				}
			}
			probeCtx, cancel := context.WithTimeout(ctx, duration)
			if record.Driver == deploy.DriverNative {
				item.Status = "stopped"
				if deploy.NativeRunning(entry.Dir) {
					item.Status = "running"
				}
			} else if diagnostics, err := s.localDiagnostics(entry.Dir); err == nil {
				if state, err := diagnostics.Status(probeCtx); err == nil {
					item.Status = state.Status
				}
			}
			if startup, err := deploy.AutostartStatus(probeCtx, record); err == nil {
				item.Autostart = new(startup.Enabled)
			}
			cancel()
		}
		instances = append(instances, item)
	}
	return Result{Data: map[string]any{"instances": instances}}, nil
}

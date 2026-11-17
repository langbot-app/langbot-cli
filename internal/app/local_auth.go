package app

import (
	"context"
	"fmt"
	"time"

	"github.com/langbot-app/langbot-cli/internal/config"
	"github.com/langbot-app/langbot-cli/internal/deploy"
	"github.com/langbot-app/langbot-cli/internal/i18n"
	"github.com/langbot-app/langbot-cli/internal/progress"
	"github.com/langbot-app/langbot-cli/internal/result"
)

func (s *Service) configureLocalAuth(ctx context.Context, dir string, record deploy.Record, running bool) (bool, error) {
	if !deploy.SupportsLocalAuth(record.CoreVersion) {
		progress.Info(ctx, []string{i18n.T("Automatic API authentication requires Core v4.10.3 or newer; skipped")})
		return false, nil
	}
	changed, err := deploy.EnsureLocalAuth(ctx, dir, record, s.localRunner())
	if err != nil {
		return false, result.New("precondition", err.Error())
	}
	if changed && running {
		progress.Info(ctx, []string{fmt.Sprintf(i18n.T("Local API key configured; restart to activate: %s"), "lbctl restart --dir "+nativeShellQuote(dir))})
	}
	err = s.WithContext(ctx).update(func(file *config.File) error {
		name := record.DeploymentID
		selected, exists := file.Contexts[name]
		if exists && !sameLocalEndpoint(selected.Endpoint, record.Endpoint) {
			return result.New("precondition", i18n.T("Local context endpoint differs from deployment"))
		}
		selected.Endpoint = record.Endpoint
		if selected.Credential == nil || selected.Credential.Type == "local_deployment" {
			selected.Credential = &config.Credential{Type: "local_deployment", Name: deploy.LocalKeyPath(dir)}
		}
		file.Contexts[name] = selected
		if file.CurrentContext == "" {
			file.CurrentContext = name
		}
		return nil
	})
	return changed, err
}

func (s *Service) bindLocalAuth(ctx context.Context, record deploy.Record) error {
	file, err := s.load()
	if err != nil {
		return err
	}
	selected, exists := file.Contexts[record.DeploymentID]
	if !exists || selected.Credential == nil || selected.Credential.Type != "local_deployment" {
		return nil
	}
	lookup := func(string) (string, bool) { return "", false }
	connection, err := config.Resolve(file, config.Options{Context: record.DeploymentID, ContextSet: true}, lookup)
	if err != nil {
		return err
	}
	connection, err = connection.WithCredential(config.Options{}, lookup, nil)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	identity, err := s.identity(ctx, connection)
	if err != nil {
		return err
	}
	if err := workspaceBindingError(connection, identity); err != nil {
		return err
	}
	return s.WithContext(ctx).update(func(file *config.File) error {
		current, exists := file.Contexts[record.DeploymentID]
		if !exists || current.Endpoint != selected.Endpoint || current.Credential == nil || *current.Credential != *selected.Credential || current.ExpectedWorkspaceUUID != selected.ExpectedWorkspaceUUID {
			return result.New("precondition", i18n.T("Local context changed during authentication"))
		}
		current.ExpectedWorkspaceUUID = identity.WorkspaceUUID
		file.Contexts[record.DeploymentID] = current
		return nil
	})
}

func (s *Service) ShowAPIKey(ctx context.Context, options CheckOptions) (string, error) {
	connection, err := s.connection(ctx, config.Options{Context: options.Context, ContextSet: options.ContextSet,
		Endpoint: options.Endpoint, EndpointSet: options.EndpointSet, Timeout: options.Timeout, TimeoutSet: options.TimeoutSet, APIKeyStdin: options.APIKeyStdin})
	if err != nil {
		return "", err
	}
	if connection.APIKey == "" {
		return "", result.New("auth", i18n.T("No API key configured"))
	}
	return connection.APIKey, nil
}

func (s *Service) automaticCredentialDeployment(options CheckOptions) string {
	if options.EndpointSet || options.APIKeyStdin {
		return ""
	}
	file, err := s.load()
	if err != nil {
		return ""
	}
	connection, err := config.Resolve(file, config.Options{Context: options.Context, ContextSet: options.ContextSet}, s.deps.LookupEnv)
	if err != nil || connection.Temporary || connection.CredentialSource != "local_deployment" {
		return ""
	}
	selected := file.Contexts[connection.Context]
	directories, err := s.deploymentDirs()
	if err != nil {
		return ""
	}
	for _, dir := range directories {
		record, err := (deploy.Store{Dir: dir}).Read()
		if err == nil && record.DeploymentID == connection.Context && selected.Credential.Name == deploy.LocalKeyPath(dir) && sameLocalEndpoint(record.Endpoint, connection.Endpoint) {
			return dir
		}
	}
	return ""
}

func (s *Service) repairDockerInstall(ctx context.Context, dir string, record deploy.Record) (Result, error) {
	err := deploy.WithMutationLock(ctx, dir, func() error {
		if err := deploy.CheckUpgradeInterrupted(dir); err != nil {
			return err
		}
		diagnostics := deploy.Diagnostics{Dir: dir, Store: deploy.Store{Dir: dir}, Runner: s.localRunner(), HTTP: s.deps.LocalHTTP}
		before, err := diagnostics.Status(ctx)
		if err != nil || before.Status == "unknown" {
			return result.New("precondition", i18n.T("Unable to confirm local deployment status; run doctor first"))
		}
		changed, err := s.configureLocalAuth(ctx, dir, record, false)
		if err != nil {
			return err
		}
		args := []string{"up", "-d"}
		if changed && before.Status == "running" {
			args = []string{"restart"}
		}
		if _, err := s.localRunner().Compose(ctx, record, args...); err != nil {
			return result.New("server", i18n.T("Unable to repair local deployment"))
		}
		if err := s.waitForLocalHTTP(ctx, record); err != nil {
			return err
		}
		return s.bindLocalAuth(ctx, record)
	})
	data := map[string]any{"action": "install", "record": record, "changed": err == nil, "url": record.Endpoint, "instance_dir": dir, "local_auth": deploy.SupportsLocalAuth(record.CoreVersion)}
	data["local_context"] = record.DeploymentID
	if err == nil {
		data["status"] = map[string]any{"status": "running"}
		installAutostartData(data, record, dir)
	}
	return Result{Data: data}, normalizeLocalMutationError(err)
}

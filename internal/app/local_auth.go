package app

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/langbot-app/langbot-cli/internal/config"
	"github.com/langbot-app/langbot-cli/internal/deploy"
	"github.com/langbot-app/langbot-cli/internal/i18n"
	"github.com/langbot-app/langbot-cli/internal/progress"
	"github.com/langbot-app/langbot-cli/internal/result"
)

type localAuthAttempt struct {
	service *Service
	dir     string
	failed  bool
	bound   bool
}

func (auth *localAuthAttempt) configure(ctx context.Context, record deploy.Record) bool {
	changed, err := auth.service.configureLocalAuth(ctx, auth.dir, record)
	auth.failed = auth.failed || err != nil
	return changed
}

func (auth *localAuthAttempt) bind(ctx context.Context, record deploy.Record) {
	if !auth.failed && deploy.SupportsLocalAuth(record.CoreVersion) {
		var err error
		auth.bound, err = auth.service.bindLocalAuth(ctx, record)
		auth.failed = err != nil
	}
}

func (auth *localAuthAttempt) result(value Result) Result {
	if data, ok := value.Data.(map[string]any); ok {
		if _, exists := data["local_auth"]; exists {
			data["local_auth"] = auth.bound && !auth.failed
		}
	}
	if auth.failed {
		data, ok := value.Data.(map[string]any)
		if !ok {
			data = map[string]any{}
			value.Data = data
		}
		data["local_auth"] = false
		data["local_auth_error"] = fmt.Sprintf(i18n.T("Warning: CLI could not connect automatically; check the local API configuration, then run %s"), "lbctl restart --dir "+nativeShellQuote(auth.dir))
	}
	return value
}

func (s *Service) configureLocalAuth(ctx context.Context, dir string, record deploy.Record) (bool, error) {
	if !deploy.SupportsLocalAuth(record.CoreVersion) {
		progress.Info(ctx, []string{i18n.T("Automatic API authentication requires Core v4.10.11 or newer; skipped")})
		return false, nil
	}
	changed, err := deploy.EnsureLocalAuth(ctx, dir, record, s.localRunner())
	if err != nil {
		return false, result.New("precondition", err.Error())
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
		selectCurrent := file.CurrentContext == ""
		previous := file.Contexts[file.CurrentContext]
		if previous.Credential != nil && previous.Credential.Type == "local_deployment" && sameLocalEndpoint(previous.Endpoint, record.Endpoint) {
			retired, err := (deploy.Store{Dir: filepath.Dir(previous.Credential.Name)}).Read()
			selectCurrent = err == nil && retired.Uninstalled && retired.DeploymentID == file.CurrentContext
		}
		if selectCurrent {
			file.CurrentContext = name
		}
		return nil
	})
	return changed, err
}

func (s *Service) bindLocalAuth(ctx context.Context, record deploy.Record) (bool, error) {
	file, err := s.load()
	if err != nil {
		return false, err
	}
	selected, exists := file.Contexts[record.DeploymentID]
	if !exists || selected.Credential == nil || selected.Credential.Type != "local_deployment" {
		return false, nil
	}
	lookup := func(string) (string, bool) { return "", false }
	connection, err := config.Resolve(file, config.Options{Context: record.DeploymentID, ContextSet: true}, lookup)
	if err != nil {
		return false, err
	}
	connection, err = connection.WithCredential(config.Options{}, lookup, nil)
	if err != nil {
		return false, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	identity, err := s.identity(ctx, connection)
	if err != nil {
		return false, err
	}
	if err := workspaceBindingError(connection, identity); err != nil {
		return false, err
	}
	err = s.WithContext(ctx).update(func(file *config.File) error {
		current, exists := file.Contexts[record.DeploymentID]
		if !exists || current.Endpoint != selected.Endpoint || current.Credential == nil || *current.Credential != *selected.Credential || current.ExpectedWorkspaceUUID != selected.ExpectedWorkspaceUUID {
			return result.New("precondition", i18n.T("Local context changed during authentication"))
		}
		current.ExpectedWorkspaceUUID = identity.WorkspaceUUID
		file.Contexts[record.DeploymentID] = current
		return nil
	})
	return err == nil, err
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
	auth := localAuthAttempt{service: s, dir: dir}
	err := deploy.WithMutationLock(ctx, dir, func() error {
		if err := deploy.CheckUpgradeInterrupted(dir); err != nil {
			return err
		}
		diagnostics := deploy.Diagnostics{Dir: dir, Store: deploy.Store{Dir: dir}, Runner: s.localRunner(), HTTP: s.deps.LocalHTTP}
		before, err := diagnostics.Status(ctx)
		if err != nil || before.Status == "unknown" {
			return result.New("precondition", i18n.T("Unable to confirm local deployment status; run doctor first"))
		}
		changed := auth.configure(ctx, record)
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
		return nil
	})
	if err == nil {
		auth.bind(ctx, record)
	}
	data := map[string]any{"action": "install", "record": record, "changed": err == nil, "url": record.Endpoint, "instance_dir": dir, "local_auth": deploy.SupportsLocalAuth(record.CoreVersion)}
	data["local_context"] = record.DeploymentID
	if err == nil {
		data["status"] = map[string]any{"status": "running"}
		installAutostartData(data, record, dir)
		addInstallBoxResult(ctx, data, record)
	}
	return auth.result(Result{Data: data}), normalizeLocalMutationError(err)
}

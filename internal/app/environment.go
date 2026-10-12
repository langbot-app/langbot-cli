package app

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"time"

	"github.com/langbot-app/langbot-cli/internal/api"
	"github.com/langbot-app/langbot-cli/internal/config"
	"github.com/langbot-app/langbot-cli/internal/deploy"
	endpointutil "github.com/langbot-app/langbot-cli/internal/endpoint"
	"github.com/langbot-app/langbot-cli/internal/result"
)

type ActiveEnvironment struct {
	Name         string                  `json:"name" yaml:"name"`
	Type         string                  `json:"type" yaml:"type"`
	Discovery    string                  `json:"discovery" yaml:"discovery"`
	Connection   EnvironmentConnection   `json:"connection" yaml:"connection"`
	Service      EnvironmentService      `json:"service" yaml:"service"`
	Identity     EnvironmentIdentity     `json:"identity" yaml:"identity"`
	Capabilities EnvironmentCapabilities `json:"capabilities" yaml:"capabilities"`
	Runtime      EnvironmentRuntime      `json:"runtime" yaml:"runtime"`
}

type EnvironmentConnection struct {
	Endpoint       string `json:"endpoint" yaml:"endpoint"`
	Reachable      bool   `json:"reachable" yaml:"reachable"`
	Authentication string `json:"authentication" yaml:"authentication"`
}

type EnvironmentService struct {
	Version string `json:"version,omitempty" yaml:"version,omitempty"`
	Edition string `json:"edition,omitempty" yaml:"edition,omitempty"`
}

type EnvironmentIdentity struct {
	Status        string   `json:"status" yaml:"status"`
	InstanceUUID  string   `json:"instance_uuid,omitempty" yaml:"instance_uuid,omitempty"`
	WorkspaceUUID string   `json:"workspace_uuid,omitempty" yaml:"workspace_uuid,omitempty"`
	APIKeyID      string   `json:"api_key_id,omitempty" yaml:"api_key_id,omitempty"`
	Permissions   []string `json:"permissions,omitempty" yaml:"permissions,omitempty"`
}

type EnvironmentCapabilities struct {
	Status        string            `json:"status" yaml:"status"`
	SchemaVersion int               `json:"schema_version,omitempty" yaml:"schema_version,omitempty"`
	Operations    map[string]string `json:"operations,omitempty" yaml:"operations,omitempty"`
}

type EnvironmentRuntime struct {
	Location     string               `json:"location" yaml:"location"`
	HostPlatform string               `json:"host_platform,omitempty" yaml:"host_platform,omitempty"`
	Driver       string               `json:"driver" yaml:"driver"`
	Management   string               `json:"management" yaml:"management"`
	Binding      string               `json:"binding" yaml:"binding"`
	Status       string               `json:"status,omitempty" yaml:"status,omitempty"`
	DeploymentID string               `json:"deployment_id,omitempty" yaml:"deployment_id,omitempty"`
	Dir          string               `json:"dir,omitempty" yaml:"dir,omitempty"`
	Details      *deploy.StatusResult `json:"details,omitempty" yaml:"details,omitempty"`
}

type environmentResolution struct {
	Environment ActiveEnvironment
	Connection  config.Connection
	Diagnostics *deploy.Diagnostics
	Record      deploy.Record
	LocalMatch  bool
	NoTarget    bool
}

func (s *Service) Status(ctx context.Context, options CheckOptions) (Result, error) {
	resolved, err := s.discoverActiveEnvironment(ctx, options, true)
	if err != nil {
		return Result{Data: resolved.Environment, Meta: connectionMeta(resolved.Connection)}, err
	}
	return Result{Data: resolved.Environment, Meta: connectionMeta(resolved.Connection)}, nil
}

// Doctor 根据当前 Active Environment 选择适用的只读检查。
func (s *Service) Doctor(ctx context.Context, options CheckOptions) (Result, error) {
	resolved, discoverErr := s.discoverActiveEnvironment(ctx, options, false)
	if resolved.NoTarget {
		return s.localPreflight(ctx, resolved, options)
	}
	checks := environmentChecks(resolved.Environment)
	data := map[string]any{"environment": resolved.Environment, "checks": checks}
	var localErr error
	if !resolved.LocalMatch || resolved.Environment.Runtime.Driver != deploy.DriverDockerCompose {
		return Result{Data: data, Meta: connectionMeta(resolved.Connection)}, discoverErr
	}
	localCtx, cancel := context.WithTimeout(ctx, resolved.Connection.Timeout)
	defer cancel()
	localChecks, localErr := resolved.Diagnostics.Doctor(localCtx)
	checks = append(checks, localChecks...)
	data["checks"] = checks
	if discoverErr != nil {
		return Result{Data: data, Meta: connectionMeta(resolved.Connection)}, discoverErr
	}
	if localErr != nil {
		return Result{Data: data, Meta: connectionMeta(resolved.Connection)}, mapLocalError(localErr)
	}
	return Result{Data: data, Meta: connectionMeta(resolved.Connection)}, nil
}

func (s *Service) discoverActiveEnvironment(ctx context.Context, options CheckOptions, inspectRuntime bool) (environmentResolution, error) {
	resolved := environmentResolution{Environment: newActiveEnvironment()}
	file, err := s.load()
	if err != nil {
		return resolved, err
	}
	conn, err := s.resolveDiscovery(ctx, file, config.Options{
		Context: options.Context, Endpoint: options.Endpoint, Timeout: options.Timeout,
		ContextSet: options.ContextSet, EndpointSet: options.EndpointSet,
		TimeoutSet: options.TimeoutSet, APIKeyStdin: options.APIKeyStdin,
	})
	fallbackName := ""
	if err != nil && s.canUseLocalFallback(file, options) {
		if diagnostics, localErr := s.localDiagnostics(""); localErr == nil {
			if record, recordErr := diagnostics.Store.Read(); recordErr == nil {
				fallback := options
				fallback.Endpoint = record.Endpoint
				fallback.EndpointSet = true
				conn, err = s.resolveDiscovery(ctx, file, config.Options{
					Endpoint: fallback.Endpoint, Timeout: fallback.Timeout,
					EndpointSet: true, TimeoutSet: fallback.TimeoutSet, APIKeyStdin: fallback.APIKeyStdin,
				})
				fallbackName = strings.TrimSpace(record.DeploymentID)
			}
		}
	}
	resolved.Connection = conn
	resolved.Environment.Name = conn.Context
	if resolved.Environment.Name == "" && fallbackName != "" {
		resolved.Environment.Name = fallbackName
	}
	if resolved.Environment.Name == "" && conn.Endpoint != "" {
		resolved.Environment.Name = "temporary"
	}
	resolved.Environment.Connection.Endpoint = conn.Endpoint
	if err != nil {
		if conn.Endpoint == "" && s.canUseLocalFallback(file, options) {
			resolved.NoTarget = true
			resolved.Environment.Name = "unconfigured"
			resolved.Environment.Discovery = "unconfigured"
		}
		if conn.Endpoint != "" {
			return s.completeRuntime(ctx, resolved, api.Context{}, false, inspectRuntime, err)
		}
		return resolved, err
	}

	callCtx, cancel := context.WithTimeout(ctx, conn.Timeout)
	defer cancel()
	info, infoErr := s.info(callCtx, conn)
	if infoErr != nil {
		resolved.Environment.Connection.Reachable = hasHTTPResponse(infoErr)
		resolved.Environment.Discovery = "failed"
		failure := result.AsError(infoErr)
		if failure.Kind == "auth" || failure.Kind == "permission" {
			resolved.Environment.Connection.Authentication = "failed"
			resolved.Environment.Identity.Status = "failed"
		}
		return s.completeRuntime(callCtx, resolved, api.Context{}, false, inspectRuntime, infoErr)
	}
	resolved.Environment.Connection.Reachable = true
	resolved.Environment.Service = EnvironmentService{Version: info.Version, Edition: info.Edition}
	resolved.Environment.Type = environmentType(info.Edition)

	if conn.CredentialSource == "" {
		resolved.Environment.Discovery = "partial"
		return s.completeRuntime(callCtx, resolved, api.Context{}, false, inspectRuntime, nil)
	}

	identity, identityErr := s.identity(callCtx, conn)
	if identityErr != nil {
		if optionalDiscoveryUnavailable(identityErr) {
			resolved.Environment.Identity.Status = "unknown"
			resolved.Environment.Connection.Authentication = "unknown"
			resolved.Environment.Discovery = "partial"
		} else {
			resolved.Environment.Identity.Status = "failed"
			resolved.Environment.Connection.Authentication = "failed"
			resolved.Environment.Discovery = "failed"
			return s.completeRuntime(callCtx, resolved, api.Context{}, false, inspectRuntime, identityErr)
		}
	} else {
		resolved.Environment.Connection.Authentication = "verified"
		resolved.Environment.Identity = EnvironmentIdentity{
			Status: "verified", InstanceUUID: identity.InstanceUUID, WorkspaceUUID: identity.WorkspaceUUID,
			APIKeyID: identity.APIKeyID, Permissions: identity.Permissions,
		}
		if bindingErr := workspaceBindingError(conn, identity); bindingErr != nil {
			resolved.Environment.Identity.Status = "mismatch"
			resolved.Environment.Discovery = "failed"
			return s.completeRuntime(callCtx, resolved, identity, true, inspectRuntime, bindingErr)
		}
	}

	capabilities, capabilityErr := s.capabilities(callCtx, conn)
	if capabilityErr != nil {
		if optionalDiscoveryUnavailable(capabilityErr) {
			resolved.Environment.Capabilities.Status = "unknown"
			resolved.Environment.Discovery = "partial"
		} else {
			resolved.Environment.Capabilities.Status = "failed"
			resolved.Environment.Discovery = "failed"
			return s.completeRuntime(callCtx, resolved, identity, identityErr == nil, inspectRuntime, capabilityErr)
		}
	} else {
		resolved.Environment.Capabilities = EnvironmentCapabilities{
			Status: "verified", SchemaVersion: capabilities.SchemaVersion, Operations: capabilityStatuses(capabilities),
		}
	}
	if resolved.Environment.Discovery != "partial" {
		resolved.Environment.Discovery = "complete"
	}
	return s.completeRuntime(callCtx, resolved, identity, identityErr == nil, inspectRuntime, nil)
}

func (s *Service) resolveDiscovery(ctx context.Context, file config.File, options config.Options) (config.Connection, error) {
	conn, err := config.Resolve(file, options, s.deps.LookupEnv)
	if err != nil {
		return conn, err
	}
	if conn.Temporary && conn.CredentialSource == "" {
		conn.Context = ""
		return conn, nil
	}
	return s.withCredential(ctx, conn, options)
}

func (s *Service) localPreflight(ctx context.Context, resolved environmentResolution, options CheckOptions) (Result, error) {
	timeout, err := localOperationTimeout(options, s.deps.LookupEnv)
	if err != nil {
		return Result{Data: resolved.Environment}, err
	}
	diagnostics, err := s.localDiagnostics("")
	if err != nil {
		return Result{Data: resolved.Environment}, err
	}
	resolved.Environment.Runtime.Location = "local"
	resolved.Environment.Runtime.HostPlatform = runtime.GOOS
	resolved.Environment.Runtime.Binding = "missing"
	checks := environmentChecks(resolved.Environment)
	localCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	localChecks, localErr := diagnostics.Doctor(localCtx)
	checks = append(checks, localChecks...)
	data := map[string]any{"environment": resolved.Environment, "checks": checks}
	if localErr != nil {
		return Result{Data: data}, mapLocalError(localErr)
	}
	return Result{Data: data}, nil
}

func localOperationTimeout(options CheckOptions, lookup func(string) (string, bool)) (time.Duration, error) {
	value := options.Timeout
	present := options.TimeoutSet
	if !present && lookup != nil {
		value, present = lookup("LANGBOT_TIMEOUT")
	}
	if !present {
		return 30 * time.Second, nil
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, result.New("input", "timeout 不能为空")
	}
	timeout, err := time.ParseDuration(value)
	if err != nil || timeout <= 0 {
		return 0, result.New("input", "timeout 无效")
	}
	return timeout, nil
}

func (s *Service) canUseLocalFallback(file config.File, options CheckOptions) bool {
	if file.CurrentContext != "" || options.ContextSet || options.EndpointSet {
		return false
	}
	for _, name := range []string{"LANGBOT_CONTEXT", "LANGBOT_ENDPOINT"} {
		if _, present := s.deps.LookupEnv(name); present {
			return false
		}
	}
	return true
}

func (s *Service) completeRuntime(ctx context.Context, resolved environmentResolution, identity api.Context, identityVerified, inspect bool, priorErr error) (environmentResolution, error) {
	runtimeInfo, runtimeErr := s.resolveRuntime(ctx, resolved.Environment, identity, identityVerified, inspect, &resolved)
	resolved.Environment.Runtime = runtimeInfo
	if priorErr != nil {
		if inspect && resolved.LocalMatch && runtimeErr == nil && runtimeInfo.Status == "stopped" && result.AsError(priorErr).Kind == "network" {
			resolved.Environment.Discovery = "partial"
			return resolved, nil
		}
		return resolved, priorErr
	}
	return resolved, runtimeErr
}

func (s *Service) resolveRuntime(ctx context.Context, environment ActiveEnvironment, identity api.Context, identityVerified, inspect bool, resolved *environmentResolution) (EnvironmentRuntime, error) {
	runtimeInfo := EnvironmentRuntime{Location: "unknown", Driver: "unbound", Management: "unavailable", Binding: "none"}
	diagnostics, err := s.localDiagnostics("")
	if err != nil {
		runtimeInfo.Binding = "unavailable"
		return fallbackRuntime(environment, runtimeInfo), nil
	}
	resolved.Diagnostics = &diagnostics
	record, recordErr := diagnostics.Store.Read()
	if recordErr != nil {
		switch {
		case errors.Is(recordErr, deploy.ErrNotInstalled), errors.Is(recordErr, deploy.ErrNotManaged):
			runtimeInfo.Binding = "missing"
		case errors.Is(recordErr, deploy.ErrCorrupt):
			runtimeInfo.Binding = "corrupt"
		default:
			runtimeInfo.Binding = "unreadable"
		}
		return fallbackRuntime(environment, runtimeInfo), nil
	}
	resolved.Record = record
	if !deploymentMatches(record, environment.Connection.Endpoint, identity, identityVerified) {
		runtimeInfo.Binding = "mismatch"
		return fallbackRuntime(environment, runtimeInfo), nil
	}
	resolved.LocalMatch = true
	runtimeInfo.Location = "local"
	runtimeInfo.HostPlatform = runtime.GOOS
	runtimeInfo.Driver = record.Driver
	runtimeInfo.Binding = "verified"
	runtimeInfo.DeploymentID = record.DeploymentID
	runtimeInfo.Dir = diagnostics.Dir
	if record.Driver != deploy.DriverDockerCompose {
		runtimeInfo.Management = "unavailable"
		return runtimeInfo, nil
	}
	runtimeInfo.Management = "local_managed"
	if !inspect {
		return runtimeInfo, nil
	}
	status, statusErr := diagnostics.Status(ctx)
	runtimeInfo.Status = status.Status
	runtimeInfo.Details = &status
	if statusErr != nil {
		runtimeInfo.Status = "unknown"
		return runtimeInfo, mapLocalError(statusErr)
	}
	return runtimeInfo, nil
}

func fallbackRuntime(environment ActiveEnvironment, info EnvironmentRuntime) EnvironmentRuntime {
	switch environment.Type {
	case "cloud":
		info.Location = "cloud"
		info.Driver = "cloud"
		info.Management = "cloud_managed"
	case "self_hosted":
		info.Management = "unbound"
	}
	return info
}

func newActiveEnvironment() ActiveEnvironment {
	return ActiveEnvironment{
		Type: "unknown", Discovery: "unknown",
		Connection:   EnvironmentConnection{Authentication: "not_checked"},
		Identity:     EnvironmentIdentity{Status: "unknown"},
		Capabilities: EnvironmentCapabilities{Status: "unknown"},
		Runtime:      EnvironmentRuntime{Location: "unknown", Driver: "unbound", Management: "unavailable", Binding: "none"},
	}
}

func environmentType(edition string) string {
	switch strings.ToLower(strings.TrimSpace(edition)) {
	case "cloud":
		return "cloud"
	case "community":
		return "self_hosted"
	default:
		return "unknown"
	}
}

func optionalDiscoveryUnavailable(err error) bool {
	if err == nil {
		return false
	}
	typed := result.AsError(err)
	return typed.Kind == "not_found" || typed.HTTPStatus == 404
}

func deploymentMatches(record deploy.Record, endpoint string, identity api.Context, identityVerified bool) bool {
	if identityVerified && record.InstanceUUID != "" {
		return record.InstanceUUID == identity.InstanceUUID
	}
	recordEndpoint, recordErr := endpointutil.Normalize(record.Endpoint)
	activeEndpoint, activeErr := endpointutil.Normalize(endpoint)
	return recordErr == nil && activeErr == nil && recordEndpoint == activeEndpoint
}

func environmentChecks(environment ActiveEnvironment) []deploy.Check {
	checks := []deploy.Check{{Name: "connection", Status: "error", Reason: "服务不可达"}}
	if environment.Discovery == "unconfigured" {
		checks[0] = deploy.Check{Name: "connection", Status: "warn", Reason: "未配置服务目标"}
	} else if environment.Connection.Reachable {
		checks[0] = deploy.Check{Name: "connection", Status: "ok", Reason: "服务可达", Detail: environment.Connection.Endpoint}
	}
	serviceStatus := "ok"
	serviceReason := "服务类型已识别"
	if environment.Type == "unknown" {
		serviceStatus, serviceReason = "warn", "服务类型未知"
	}
	checks = append(checks, deploy.Check{Name: "service_discovery", Status: serviceStatus, Reason: serviceReason, Detail: environment.Service})
	checks = append(checks, stateCheck("identity", environment.Identity.Status, "API Key 身份已确认", "API Key 身份未知"))
	checks = append(checks, stateCheck("capabilities", environment.Capabilities.Status, "服务能力已确认", "服务能力未知"))
	checks = append(checks, deploy.Check{Name: "runtime_binding", Status: runtimeCheckStatus(environment.Runtime), Reason: runtimeCheckReason(environment.Runtime), Detail: environment.Runtime})
	return checks
}

func stateCheck(name, state, okReason, unknownReason string) deploy.Check {
	switch state {
	case "verified":
		return deploy.Check{Name: name, Status: "ok", Reason: okReason}
	case "failed", "mismatch":
		return deploy.Check{Name: name, Status: "error", Reason: "检查失败"}
	default:
		return deploy.Check{Name: name, Status: "warn", Reason: unknownReason}
	}
}

func runtimeCheckStatus(info EnvironmentRuntime) string {
	if info.Management == "local_managed" || info.Management == "cloud_managed" {
		return "ok"
	}
	return "warn"
}

func runtimeCheckReason(info EnvironmentRuntime) string {
	switch info.Management {
	case "local_managed":
		return "本机生命周期绑定已确认"
	case "cloud_managed":
		return "生命周期由 LangBot Cloud 管理"
	case "unbound":
		return "未找到匹配的本机生命周期绑定"
	default:
		return "当前环境不提供本机生命周期管理"
	}
}

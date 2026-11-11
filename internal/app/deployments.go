package app

import (
	"errors"

	"github.com/langbot-app/langbot-cli/internal/api"
	"github.com/langbot-app/langbot-cli/internal/deploy"
	"github.com/langbot-app/langbot-cli/internal/result"
)

type DeploymentEntry struct {
	ID       string `json:"deployment_id,omitempty" yaml:"deployment_id,omitempty"`
	Dir      string `json:"dir" yaml:"dir"`
	Endpoint string `json:"endpoint,omitempty" yaml:"endpoint,omitempty"`
	Project  string `json:"project,omitempty" yaml:"project,omitempty"`
	Binding  string `json:"binding" yaml:"binding"`
}

func (s *Service) deploymentDirs() ([]string, error) {
	if s.deps.LocalDir != "" {
		dir, err := deploy.ResolveDir(s.deps.LocalDir)
		return []string{dir}, err
	}
	return (deploy.Locator{Path: s.deps.LocalLocatorPath}).Directories()
}

// localDeployments is local and read-only; an offline or broken deployment must
// not prevent the remaining entries from being listed.
func (s *Service) localDeployments() ([]DeploymentEntry, error) {
	dirs, err := s.deploymentDirs()
	if err != nil {
		return nil, result.New("precondition", "本机部署定位记录不可用")
	}
	entries := []DeploymentEntry{}
	registered, err := (deploy.Locator{Path: s.deps.LocalLocatorPath}).RegisteredDirectories()
	if err != nil {
		return nil, result.New("precondition", "本机部署定位记录不可用")
	}
	for _, dir := range dirs {
		record, readErr := (deploy.Store{Dir: dir}).Read()
		if len(registered) == 0 && (errors.Is(readErr, deploy.ErrNotInstalled) || errors.Is(readErr, deploy.ErrNotManaged)) {
			continue
		}
		entry := DeploymentEntry{Dir: dir, Binding: "registered"}
		if readErr != nil {
			entry.Binding = "unavailable"
		} else {
			entry.ID, entry.Endpoint, entry.Project = record.DeploymentID, record.Endpoint, record.ComposeProject
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// The registry lock is held by install/adopt. Multiple records must not own
// the same endpoint, runtime identity, or Compose project.
func (s *Service) checkDeploymentRegistration(dir string, candidate deploy.Record) error {
	dirs, err := (deploy.Locator{Path: s.deps.LocalLocatorPath}).Directories()
	if err != nil {
		return result.New("precondition", "本机部署定位记录不可用")
	}
	if s.deps.LocalDir != "" {
		dirs = append(dirs, s.deps.LocalDir)
	}
	for _, existing := range dirs {
		if existing == dir {
			continue
		}
		record, err := (deploy.Store{Dir: existing}).Read()
		if errors.Is(err, deploy.ErrNotInstalled) || errors.Is(err, deploy.ErrNotManaged) {
			continue
		}
		if err != nil {
			return result.New("precondition", "已有本机部署记录不可用，请先诊断")
		}
		if sameLocalEndpoint(record.Endpoint, candidate.Endpoint) ||
			(record.ComposeProject != "" && record.ComposeProject == candidate.ComposeProject) ||
			(record.InstanceUUID != "" && record.InstanceUUID == candidate.InstanceUUID) {
			return result.New("precondition", "该 endpoint、实例或 Compose project 已由其他目录登记")
		}
	}
	return nil
}

func (s *Service) matchingLocalDiagnostics(endpoint string, identity api.Context, verified bool) (deploy.Diagnostics, error) {
	diagnostics, err := s.localDiagnostics("")
	if err != nil {
		return diagnostics, err
	}
	dirs, err := s.deploymentDirs()
	if err != nil {
		return diagnostics, err
	}
	matched := false
	for _, dir := range dirs {
		record, err := (deploy.Store{Dir: dir}).Read()
		if err != nil || !deploymentMatches(record, endpoint, identity, verified) {
			continue
		}
		if matched {
			return deploy.Diagnostics{}, result.New("precondition", "多个本机部署匹配当前环境，拒绝自动选择")
		}
		diagnostics, err = s.localDiagnostics(dir)
		if err != nil {
			return diagnostics, err
		}
		matched = true
	}
	return diagnostics, nil
}

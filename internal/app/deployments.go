package app

import (
	"errors"
	"fmt"
	"strings"

	"github.com/langbot-app/langbot-cli/internal/api"
	"github.com/langbot-app/langbot-cli/internal/deploy"
	"github.com/langbot-app/langbot-cli/internal/i18n"
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
		return nil, result.New("precondition", i18n.T("Local deployment locator record unavailable"))
	}
	entries := []DeploymentEntry{}
	registered, err := (deploy.Locator{Path: s.deps.LocalLocatorPath}).RegisteredDirectories()
	if err != nil {
		return nil, result.New("precondition", i18n.T("Local deployment locator record unavailable"))
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
		return result.New("precondition", i18n.T("Local deployment locator record unavailable"))
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
			return result.New("precondition", i18n.T("Existing local deployment record is unavailable; diagnose first"))
		}
		if record.Uninstalled {
			continue
		}
		if sameLocalEndpoint(record.Endpoint, candidate.Endpoint) ||
			(record.ComposeProject != "" && record.ComposeProject == candidate.ComposeProject) ||
			(record.InstanceUUID != "" && record.InstanceUUID == candidate.InstanceUUID) {
			command := "lbctl start --dir " + nativeShellQuote(existing)
			if record.Driver == deploy.DriverNative {
				recovery := map[string]any{}
				nativeInstallRecovery(recovery, existing, record, true)
				command = recovery["retry_command"].(string)
			}
			details := []string{fmt.Sprintf(i18n.T("Directory: %s"), existing)}
			for _, field := range [][2]string{{i18n.T("Instance: %s"), record.Name}, {i18n.T("Endpoint: %s"), record.Endpoint}, {i18n.T("Compose project: %s"), record.ComposeProject}, {i18n.T("Instance UUID: %s"), record.InstanceUUID}} {
				if field[1] != "" {
					details = append(details, fmt.Sprintf(i18n.T(field[0]), field[1]))
				}
			}
			cleanup := "lbctl uninstall --dir " + nativeShellQuote(existing) + " --yes"
			return result.New("precondition", fmt.Sprintf(i18n.T("Registration conflict: %s. Continue using the existing instance: %s; or uninstall it (data preserved) and reinstall: %s"), strings.Join(details, ", "), command, cleanup))
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
			return deploy.Diagnostics{}, result.New("precondition", i18n.T("Multiple local deployments match this environment; refusing automatic selection"))
		}
		diagnostics, err = s.localDiagnostics(dir)
		if err != nil {
			return diagnostics, err
		}
		matched = true
	}
	return diagnostics, nil
}

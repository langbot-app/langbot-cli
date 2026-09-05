package config

import "time"

// Credential 仅保存凭据来源，不保存凭据值。
type Credential struct {
	Type string `yaml:"type" json:"type"`
	Name string `yaml:"name" json:"name"`
}

type Context struct {
	Endpoint              string      `yaml:"endpoint" json:"endpoint"`
	Credential            *Credential `yaml:"credential,omitempty" json:"credential,omitempty"`
	Timeout               string      `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	ExpectedWorkspaceUUID string      `yaml:"expected_workspace_uuid,omitempty" json:"expected_workspace_uuid,omitempty"`
}

type File struct {
	Version        int                `yaml:"version" json:"version"`
	CurrentContext string             `yaml:"current_context,omitempty" json:"current_context,omitempty"`
	Contexts       map[string]Context `yaml:"contexts" json:"contexts"`
}

type Store struct {
	Path string
}

type Options struct {
	Context     string
	Endpoint    string
	Timeout     string
	ContextSet  bool
	EndpointSet bool
	TimeoutSet  bool
	APIKeyStdin bool
}

type Connection struct {
	Context               string        `json:"context" yaml:"context"`
	Endpoint              string        `json:"endpoint" yaml:"endpoint"`
	CredentialSource      string        `json:"credential_source" yaml:"credential_source"`
	ExpectedWorkspaceUUID string        `json:"expected_workspace_uuid,omitempty" yaml:"expected_workspace_uuid,omitempty"`
	Temporary             bool          `json:"temporary" yaml:"temporary"`
	Timeout               time.Duration `json:"-" yaml:"-"`
	APIKey                string        `json:"-" yaml:"-"`

	credentialRef              string
	credentialType             string
	requiresExplicitCredential bool
}

const (
	currentVersion  = 1
	defaultTimeout  = 30 * time.Second
	maxConfigBytes  = 1 << 20
	maxSecretBytes  = 64 << 10
	lockRetryPeriod = 25 * time.Millisecond
)

func emptyFile() File {
	return File{Version: currentVersion, Contexts: make(map[string]Context)}
}

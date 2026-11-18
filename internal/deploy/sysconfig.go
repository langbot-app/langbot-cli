package deploy

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/langbot-app/langbot-cli/internal/i18n"
	"github.com/langbot-app/langbot-cli/internal/redact"
	"github.com/langbot-app/langbot-cli/internal/telemetry"
	"go.yaml.in/yaml/v3"
)

type SystemConfig struct{ Document yaml.Node }

func ParseSystemConfig(data []byte) (*SystemConfig, error) {
	config := &SystemConfig{}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&config.Document); err != nil || len(config.Document.Content) != 1 || config.Document.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New(i18n.T("Invalid system configuration YAML"))
	}
	var extra yaml.Node
	var decoded any
	if decoder.Decode(&extra) != io.EOF || config.Document.Decode(&decoded) != nil {
		return nil, errors.New(i18n.T("Invalid system configuration YAML"))
	}
	return config, nil
}

func (config *SystemConfig) Node(key string) *yaml.Node {
	node := config.Document.Content[0]
	if key == "" {
		return node
	}
	for _, part := range strings.Split(key, ".") {
		node = mappingValue(node, part)
	}
	return node
}

func (config *SystemConfig) Bytes() ([]byte, error) { return yaml.Marshal(&config.Document) }

func secretConfigKey(key string) bool {
	key = strings.ToLower(key)
	return redact.IsSecretField(key) || strings.Contains(key, "key") || strings.Contains(key, "token") || strings.Contains(key, "secret") || strings.Contains(key, "password")
}

func configValue(node *yaml.Node, key string, secrets bool) any {
	if node == nil {
		return nil
	}
	if !secrets && secretConfigKey(key) {
		return redact.Mask
	}
	if node.Kind == yaml.AliasNode {
		return configValue(node.Alias, key, secrets)
	}
	if node.Kind == yaml.MappingNode {
		value := map[string]any{}
		for index := 0; index < len(node.Content); index += 2 {
			name := node.Content[index].Value
			value[name] = configValue(node.Content[index+1], name, secrets)
		}
		return value
	}
	if node.Kind == yaml.SequenceNode {
		value := []any{}
		for _, child := range node.Content {
			value = append(value, configValue(child, key, secrets))
		}
		return value
	}
	var value any
	_ = node.Decode(&value)
	return value
}

func (config *SystemConfig) Value(key string, secrets bool) any {
	return configValue(config.Node(key), key, secrets)
}

func KnownSystemKey(key string) bool {
	switch key {
	case "box.enabled", "box.backend", "box.runtime.endpoint", "api.port":
		return true
	}
	return false
}

func (config *SystemConfig) Set(key, value string) error {
	parts := strings.Split(key, ".")
	for _, part := range parts {
		if part == "" {
			return errors.New(i18n.T("Invalid system configuration key"))
		}
	}
	previous := config.Node(key)
	tag := ""
	if previous != nil {
		tag = previous.Tag
	}
	switch key {
	case "box.enabled":
		tag = "!!bool"
	case "api.port":
		tag = "!!int"
	case "box.backend", "box.runtime.endpoint":
		tag = "!!str"
	}
	node := &yaml.Node{}
	if tag == "!!str" {
		node.Kind, node.Tag, node.Value = yaml.ScalarNode, tag, value
	} else {
		var document yaml.Node
		if err := yaml.Unmarshal([]byte(value), &document); err != nil || len(document.Content) != 1 {
			return errors.New(i18n.T("Invalid system configuration value"))
		}
		node = document.Content[0]
		if tag != "" && tag != "!!null" && node.Tag != tag {
			return errors.New(i18n.T("System configuration value has the wrong type"))
		}
	}
	switch key {
	case "box.backend":
		if !ValidBoxBackend(value, false) {
			return errors.New(i18n.T("Box backend must be local, docker, nsjail, e2b or host"))
		}
	case "box.runtime.endpoint":
		if value != "" {
			parsed, err := url.Parse(value)
			if err != nil || parsed.Scheme == "" || parsed.Host == "" {
				return errors.New(i18n.T("Box endpoint must be a URL or empty"))
			}
		}
	case "api.port":
		var port int
		if node.Decode(&port) != nil || port < 1 || port > 65535 {
			return errors.New(i18n.T("port must be 1-65535"))
		}
	}
	parent := config.Document.Content[0]
	for _, part := range parts[:len(parts)-1] {
		child := mappingValue(parent, part)
		if child == nil {
			child = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			parent.Content = append(parent.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: part}, child)
		}
		if child.Kind != yaml.MappingNode {
			return errors.New(i18n.T("System configuration parent is not a mapping"))
		}
		parent = child
	}
	if previous != nil {
		if previous.Kind == yaml.ScalarNode && node.Kind == previous.Kind && previous.Tag == node.Tag && previous.Value == node.Value {
			return nil
		}
		node.HeadComment, node.LineComment, node.FootComment = previous.HeadComment, previous.LineComment, previous.FootComment
		*previous = *node
	} else {
		parent.Content = append(parent.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: parts[len(parts)-1]}, node)
	}
	return nil
}

func (config *SystemConfig) Unset(key string) error {
	parts := strings.Split(key, ".")
	for _, part := range parts {
		if part == "" {
			return errors.New(i18n.T("Invalid system configuration key"))
		}
	}
	parent := config.Document.Content[0]
	for _, part := range parts[:len(parts)-1] {
		parent = mappingValue(parent, part)
	}
	if parent == nil || parent.Kind != yaml.MappingNode {
		return nil
	}
	for index := 0; index < len(parent.Content); index += 2 {
		if parent.Content[index].Value == parts[len(parts)-1] {
			parent.Content = append(parent.Content[:index], parent.Content[index+2:]...)
			break
		}
	}
	return nil
}

func ReadSystemConfig(ctx context.Context, dir string, record Record, runner Runner) ([]byte, bool, error) {
	path := filepath.Join(record.DataDir, "config.yaml")
	data, err := os.ReadFile(path)
	missing := os.IsNotExist(err)
	if err == nil {
		return data, false, nil
	}
	if record.Driver == DriverNative {
		if !missing {
			return nil, false, err
		}
		command := exec.CommandContext(ctx, NativePython(dir, record.CoreVersion), "-c", readAuthConfig, path)
		hideNativeConsole(command)
		data, err = command.Output()
	} else {
		var output CommandResult
		output, err = authContainer(ctx, runner, record, "", readAuthConfig, "/data/config.yaml")
		data = []byte(output.Stdout)
	}
	return data, missing, err
}

const writeSystemConfig = `import os, shutil, tempfile
path = '/data/config.yaml'
if os.path.exists(path):
    shutil.copyfile(path, path + '.bak')
    os.chmod(path + '.bak', 0o600)
fd, temporary = tempfile.mkstemp(dir='/data')
try:
    with os.fdopen(fd, 'wb') as target, open('/auth/config.yaml', 'rb') as source:
        shutil.copyfileobj(source, target)
        target.flush()
        os.fsync(target.fileno())
    os.replace(temporary, path)
finally:
    if os.path.exists(temporary): os.unlink(temporary)
`

func WriteSystemConfig(ctx context.Context, dir string, record Record, runner Runner, data []byte) error {
	if _, err := ParseSystemConfig(data); err != nil {
		return err
	}
	path := filepath.Join(record.DataDir, "config.yaml")
	write := func() error {
		previous, err := os.ReadFile(path)
		if err == nil {
			if err := writeAtomic(path+".bak", previous, 0o600); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		return writeAtomic(path, data, 0o600)
	}
	err := write()
	if err == nil || record.Driver != DriverDockerCompose {
		return err
	}
	temporary, err := os.MkdirTemp(dir, ".sysconfig-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temporary)
	if err := writeAtomic(filepath.Join(temporary, "config.yaml"), data, 0o600); err != nil {
		return err
	}
	_, err = authContainer(ctx, runner, record, temporary, writeSystemConfig)
	return err
}

func SystemConfigOverrides(ctx context.Context, record Record, runner Runner, key string) ([]string, error) {
	name := strings.ToUpper(strings.ReplaceAll(key, ".", "__"))
	var names []string
	if record.Driver == DriverNative {
		for _, entry := range NativeEnvironment(record) {
			variable, _, _ := strings.Cut(entry, "=")
			if key == "" && strings.Contains(variable, "__") || strings.EqualFold(variable, name) {
				names = append(names, variable)
			}
		}
		return names, nil
	}
	output, err := runner.Compose(telemetry.PrivateCommand(ctx), record, "config", "--format", "json")
	if err != nil {
		return nil, errors.New(i18n.T("Unable to inspect Compose environment overrides"))
	}
	var compose struct {
		Services map[string]struct {
			Environment map[string]any `yaml:"environment"`
		} `yaml:"services"`
	}
	if yaml.Unmarshal([]byte(output.Stdout), &compose) != nil {
		return nil, errors.New(i18n.T("Unable to inspect Compose environment overrides"))
	}
	for variable, value := range compose.Services["langbot"].Environment {
		if value != nil && (key == "" && strings.Contains(variable, "__") || strings.EqualFold(variable, name)) {
			names = append(names, variable)
		}
	}
	return names, nil
}

const WindowsHostBoxMinVersion = ""

func WindowsHostBoxSupported(platform, version string) bool {
	if platform != "windows" {
		return true
	}
	if WindowsHostBoxMinVersion == "" {
		return false
	}
	comparison, err := CompareReleaseVersions(version, WindowsHostBoxMinVersion)
	return err == nil && comparison >= 0
}

func ValidBoxBackend(value string, off bool) bool {
	switch value {
	case "local", "docker", "nsjail", "e2b", "host":
		return true
	case "off":
		return off
	}
	return false
}

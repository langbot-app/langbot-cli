package deploy

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/langbot-app/langbot-cli/internal/i18n"
	"github.com/langbot-app/langbot-cli/internal/telemetry"
	"go.yaml.in/yaml/v3"
)

const GlobalAPIKeyMinVersion = "v4.10.3"

func SupportsLocalAuth(version string) bool {
	comparison, err := CompareReleaseVersions(version, GlobalAPIKeyMinVersion)
	return err == nil && comparison >= 0
}

func LocalKeyPath(dir string) string { return filepath.Join(dir, ".api-key") }

const readAuthConfig = `import os, sys, importlib.resources
path = sys.argv[1]
if os.path.exists(path):
    with open(path, encoding='utf-8') as source:
        sys.stdout.write(source.read())
else:
    sys.stdout.write(importlib.resources.files('langbot.templates').joinpath('config.yaml').read_text(encoding='utf-8'))
`

const writeAuthConfig = `import os, shutil
with open('/data/config.yaml', 'wb') as target, open('/auth/config.yaml', 'rb') as source:
    os.chmod('/data/config.yaml', 0o600)
    shutil.copyfileobj(source, target)
`

func EnsureLocalAuth(ctx context.Context, dir string, record Record, runner Runner) (bool, error) {
	if !SupportsLocalAuth(record.CoreVersion) {
		return false, nil
	}
	failure := func() (bool, error) {
		return false, errors.New(i18n.T("Unable to configure local API authentication"))
	}
	path := filepath.Join(record.DataDir, "config.yaml")
	data, err := os.ReadFile(path)
	newConfig := os.IsNotExist(err)
	if err != nil {
		if record.Driver == DriverNative {
			if !newConfig {
				return failure()
			}
			command := exec.CommandContext(ctx, NativePython(dir, record.CoreVersion), "-c", readAuthConfig, path)
			hideNativeConsole(command)
			data, err = command.Output()
		} else {
			var output CommandResult
			output, err = authContainer(ctx, runner, record, "", readAuthConfig, "/data/config.yaml")
			data = []byte(output.Stdout)
		}
		if err != nil {
			return failure()
		}
	}
	updated, key, changed, err := configureAPIKey(data, newConfig, record.Endpoint)
	if err != nil {
		return failure()
	}
	if changed || newConfig {
		if err := writeAtomic(path, updated, 0o600); err != nil {
			if record.Driver != DriverDockerCompose {
				return failure()
			}
			temporary, err := os.MkdirTemp(dir, ".auth-")
			if err != nil {
				return failure()
			}
			defer os.RemoveAll(temporary)
			if err := writeAtomic(filepath.Join(temporary, "config.yaml"), updated, 0o600); err != nil {
				return failure()
			}
			if _, err := authContainer(ctx, runner, record, temporary, writeAuthConfig); err != nil {
				return failure()
			}
		}
	}
	if err := writeAtomic(LocalKeyPath(dir), []byte(key), 0o600); err != nil {
		return failure()
	}
	return changed, nil
}

func authContainer(ctx context.Context, runner Runner, record Record, source, script string, arguments ...string) (CommandResult, error) {
	image, err := dockerBackupImage(record)
	if err != nil {
		return CommandResult{}, err
	}
	mode := ",readonly"
	if source != "" {
		mode = ""
	}
	args := []string{"run", "--rm", "--network", "none", "--user", "0:0", "--mount", "type=bind,src=" + record.DataDir + ",dst=/data" + mode}
	if source != "" {
		args = append(args, "--mount", "type=bind,src="+source+",dst=/auth,readonly")
	}
	args = append(args, "--entrypoint", "/app/.venv/bin/python", image, "-c", script)
	return runner.Docker(telemetry.PrivateCommand(ctx), append(args, arguments...)...)
}

func configureAPIKey(data []byte, newConfig bool, endpoint string) ([]byte, string, bool, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, "", false, errors.New("invalid configuration")
	}
	root := document.Content[0]
	api := mappingValue(root, "api")
	if api == nil {
		api = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "api"}, api)
	}
	if api.Kind != yaml.MappingNode {
		return nil, "", false, errors.New("invalid api configuration")
	}
	key := mappingValue(api, "global_api_key")
	if key != nil && key.Tag != "!!null" && (key.Kind != yaml.ScalarNode || key.Tag != "!!str") {
		return nil, "", false, errors.New("invalid key type")
	}
	if key != nil && key.Tag == "!!str" && key.Value != "" {
		return data, key.Value, false, nil
	}
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return nil, "", false, err
	}
	if key == nil {
		key = &yaml.Node{}
		api.Content = append(api.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: "global_api_key"}, key)
	}
	*key = yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "lbg_" + base64.RawURLEncoding.EncodeToString(random)}
	if newConfig {
		if webhook := mappingValue(api, "webhook_prefix"); webhook != nil {
			webhook.Value = endpoint
		}
	}
	encoded, err := yaml.Marshal(&document)
	return encoded, key.Value, true, err
}

package deploy

import (
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/langbot-app/langbot-cli/internal/i18n"
	"go.yaml.in/yaml/v3"
)

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

func ComposeSourceImage(content []byte) (string, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(content, &document); err != nil || len(document.Content) != 1 {
		return "", errors.New(i18n.T("Unable to parse Compose assets"))
	}
	image := mappingValue(mappingValue(mappingValue(document.Content[0], "services"), "langbot"), "image")
	if image == nil || image.Kind != yaml.ScalarNode {
		return "", errors.New(i18n.T("Missing LangBot image declaration"))
	}
	return image.Value, nil
}

// Replace only literal image tokens, preserving comments and all other bytes.
func replaceComposeImages(content []byte, target string) ([]byte, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(content, &document); err != nil || len(document.Content) != 1 {
		return nil, errors.New(i18n.T("Unable to parse Compose assets"))
	}
	root := document.Content[0]
	if mappingValue(root, "include") != nil {
		return nil, errors.New(i18n.T("Upgrade does not support external Compose files"))
	}
	services := mappingValue(root, "services")
	lines := strings.SplitAfter(string(content), "\n")
	type edit struct {
		start, end int
		value      string
	}
	var edits []edit
	for _, name := range []string{"langbot", "langbot_plugin_runtime", "langbot_box"} {
		service := mappingValue(services, name)
		if service == nil && name == "langbot_box" {
			continue
		}
		image := mappingValue(service, "image")
		if image == nil || image.Kind != yaml.ScalarNode || image.Tag != "!!str" || image.Anchor != "" ||
			mappingValue(service, "build") != nil || mappingValue(service, "extends") != nil {
			return nil, fmt.Errorf(i18n.T("Service %s must directly declare an official image, not build, extends or image aliases"), name)
		}
		if !strings.HasPrefix(image.Value, DefaultImageRepository+":") || strings.ContainsAny(image.Value, "$\r\n ") {
			return nil, fmt.Errorf(i18n.T("Service %s is not an upgradable official LangBot image"), name)
		}
		original, replacement := image.Value, target
		switch image.Style {
		case yaml.SingleQuotedStyle:
			original, replacement = "'"+original+"'", "'"+target+"'"
		case yaml.DoubleQuotedStyle:
			original, replacement = strconv.Quote(original), strconv.Quote(target)
		case 0:
		default:
			return nil, errors.New(i18n.T("Upgrade does not support multiline image declarations"))
		}
		if image.Line < 1 || image.Line > len(lines) || image.Column < 1 {
			return nil, errors.New(i18n.T("Unable to locate Compose image declaration"))
		}
		line := []rune(lines[image.Line-1])
		if image.Column-1 > len(line) {
			return nil, errors.New(i18n.T("Unable to locate Compose image declaration"))
		}
		start := len(string(line[:image.Column-1]))
		for _, previous := range lines[:image.Line-1] {
			start += len(previous)
		}
		if !strings.HasPrefix(string(content[start:]), original) {
			return nil, errors.New(i18n.T("Compose image declaration uses unsupported escaping"))
		}
		edits = append(edits, edit{start, start + len(original), replacement})
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
	updated := string(content)
	for _, change := range edits {
		updated = updated[:change.start] + change.value + updated[change.end:]
	}
	return []byte(updated), nil
}

// Validate resolved mounts before stopping an adopted deployment. The recorded
// data tree must cover every writable bind used by the LangBot services.
func ValidateAdoptedUpgradeConfig(record Record, content string) error {
	var config struct {
		Services map[string]struct {
			Volumes []struct {
				Type, Source, Target string
				ReadOnly             bool `json:"read_only"`
			}
			EnvFile []json.RawMessage `json:"env_file"`
		} `json:"services"`
	}
	if err := json.Unmarshal([]byte(content), &config); err != nil {
		return errors.New(i18n.T("Unable to verify adopted Compose mounts"))
	}
	dataRoot := upgradeMountPath(record.DataDir)
	for _, name := range []string{"langbot", "langbot_plugin_runtime", "langbot_box"} {
		service, exists := config.Services[name]
		if !exists {
			if name != "langbot_box" || record.Profile == "all" {
				return fmt.Errorf(i18n.T("Compose is missing service %s"), name)
			}
			continue
		}
		if len(service.EnvFile) != 0 {
			return errors.New(i18n.T("Upgrade does not support service-level env_file; use the Compose project .env"))
		}
		coreData := false
		for _, volume := range service.Volumes {
			source := upgradeMountPath(volume.Source)
			if name == "langbot" && volume.Target == "/app/data" {
				coreData = volume.Type == "bind" && source == dataRoot && !volume.ReadOnly
			}
			if volume.ReadOnly || (name == "langbot_box" && volume.Source == "/var/run/docker.sock" && volume.Target == "/var/run/docker.sock") {
				continue
			}
			if volume.Type != "bind" || (source != dataRoot && !strings.HasPrefix(source, dataRoot+"/")) {
				return fmt.Errorf(i18n.T("Service %s has writable mounts outside the fully backed-up data directory"), name)
			}
		}
		if name == "langbot" && !coreData {
			return errors.New(i18n.T("LangBot /app/data mount does not match the deployment record data directory"))
		}
	}
	return nil
}

func upgradeMountPath(path string) string {
	path = strings.TrimRight(composeWorkingPath(path, runtime.GOOS), "/")
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	return path
}

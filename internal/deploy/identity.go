package deploy

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/langbot-app/langbot-cli/internal/clientidentity"
	"github.com/langbot-app/langbot-cli/internal/i18n"
	"go.yaml.in/yaml/v3"
)

const deployComposeEnvironment = "      LANGBOT_DEPLOY_SOURCE: ${LANGBOT_DEPLOY_SOURCE}\n      LANGBOT_DEPLOY_TOOL_VERSION: ${LANGBOT_DEPLOY_TOOL_VERSION}\n"

func (assets *InstallAssets) SetDeployIdentity(version string) {
	assets.Environment = deployEnvironment(assets.Environment, version)
}

func deployEnvironment(content []byte, version string) []byte {
	lines := strings.Split(string(content), "\n")
	for _, entry := range []struct{ key, value string }{
		{"LANGBOT_DEPLOY_SOURCE", "lbctl"},
		{"LANGBOT_DEPLOY_TOOL_VERSION", clientidentity.Version(version)},
	} {
		found := false
		for index, line := range lines {
			key, _, exists := strings.Cut(strings.TrimPrefix(strings.TrimSpace(line), "export "), "=")
			if exists && strings.TrimSpace(key) == entry.key {
				lines[index] = entry.key + "=" + entry.value
				found = true
			}
		}
		if !found {
			if lines[len(lines)-1] == "" {
				lines[len(lines)-1] = entry.key + "=" + entry.value
				lines = append(lines, "")
			} else {
				lines = append(lines, entry.key+"="+entry.value, "")
			}
		}
	}
	return []byte(strings.Join(lines, "\n"))
}

func WriteDeployIdentity(record Record, version string) error {
	path := record.EnvFile
	if path == "" {
		path = filepath.Join(filepath.Dir(record.ComposeFile), ".env")
	}
	content, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return writeAtomic(path, deployEnvironment(content, version), 0o600)
}

func addDeployEnvironment(content []byte) ([]byte, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(content, &document); err != nil || len(document.Content) != 1 {
		return nil, errors.New(i18n.T("Unable to parse Compose assets"))
	}
	service := mappingValue(mappingValue(document.Content[0], "services"), "langbot")
	if service == nil || service.Kind != yaml.MappingNode {
		return nil, errors.New(i18n.T("Missing LangBot service declaration"))
	}
	environment := mappingValue(service, "environment")
	lines := strings.SplitAfter(string(content), "\n")
	offset := func(node *yaml.Node) int {
		position := 0
		for _, line := range lines[:node.Line-1] {
			position += len(line)
		}
		return position + len(string([]rune(lines[node.Line-1])[:node.Column-1]))
	}
	type edit struct {
		start, end int
		value      string
	}
	var edits []edit
	var missing []string
	for _, key := range []string{"LANGBOT_DEPLOY_SOURCE", "LANGBOT_DEPLOY_TOOL_VERSION"} {
		value := "${" + key + "}"
		var existing *yaml.Node
		if environment != nil && environment.Kind == yaml.SequenceNode {
			for _, item := range environment.Content {
				if item.Value == key || strings.HasPrefix(item.Value, key+"=") {
					existing = item
					break
				}
			}
			value = key + "=" + value
		} else {
			existing = mappingValue(environment, key)
		}
		if existing == nil {
			if environment != nil && environment.Kind == yaml.SequenceNode {
				missing = append(missing, strconv.Quote(value))
			} else {
				missing = append(missing, key+": "+strconv.Quote(value))
			}
			continue
		}
		if existing.Kind != yaml.ScalarNode {
			return nil, errors.New(i18n.T("Deployment source environment variables must be scalars"))
		}
		original := existing.Value
		switch existing.Style {
		case yaml.SingleQuotedStyle:
			original = "'" + strings.ReplaceAll(original, "'", "''") + "'"
		case yaml.DoubleQuotedStyle:
			original = strconv.Quote(original)
		case 0:
		default:
			return nil, errors.New(i18n.T("Deployment source environment variables do not support multiline declarations"))
		}
		start := offset(existing)
		if !strings.HasPrefix(string(content[start:]), original) {
			return nil, errors.New(i18n.T("Unable to locate deployment source environment variables"))
		}
		if existing.Value != value {
			edits = append(edits, edit{start, start + len(original), strconv.Quote(value)})
		}
	}
	if len(missing) != 0 {
		if environment == nil {
			if service.Style&yaml.FlowStyle != 0 {
				start := offset(service) + 1
				edits = append(edits, edit{start, start, "environment: {" + strings.Join(missing, ", ") + "}, "})
			} else {
				start := offset(service)
				indent := strings.Repeat(" ", service.Column-1)
				edits = append(edits, edit{start, start, "environment: {" + strings.Join(missing, ", ") + "}\n" + indent})
			}
		} else if environment.Kind != yaml.MappingNode && environment.Kind != yaml.SequenceNode {
			return nil, errors.New(i18n.T("Compose environment must be a mapping or list"))
		} else if environment.Style&yaml.FlowStyle != 0 {
			start := offset(environment) + 1
			text := strings.Join(missing, ", ")
			if len(environment.Content) != 0 {
				text += ", "
			}
			edits = append(edits, edit{start, start, text})
		} else {
			start := offset(environment)
			indent := strings.Repeat(" ", environment.Column-1)
			prefix := ""
			if environment.Kind == yaml.SequenceNode {
				prefix = "- "
			}
			text := prefix + strings.Join(missing, "\n"+indent+prefix) + "\n" + indent
			edits = append(edits, edit{start, start, text})
		}
	}
	sort.SliceStable(edits, func(first, second int) bool { return edits[first].start > edits[second].start })
	updated := string(content)
	for _, change := range edits {
		updated = updated[:change.start] + change.value + updated[change.end:]
	}
	return []byte(updated), nil
}

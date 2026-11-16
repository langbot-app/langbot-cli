package requestbody

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/langbot-app/langbot-cli/internal/i18n"
	"github.com/langbot-app/langbot-cli/internal/result"
	"go.yaml.in/yaml/v3"
)

const maxBytes = 1 << 20

// ValidateSources 确保 API Key 和请求体不会同时读取 stdin。
func ValidateSources(name string, apiKeyStdin bool) error {
	if strings.TrimSpace(name) == "" {
		return result.New("input", i18n.T("A request body file is required"))
	}
	if name == "-" && apiKeyStdin {
		return result.New("input", i18n.T("Request body stdin cannot be combined with --api-key-stdin"))
	}
	return nil
}

// Load 从文件或 stdin 读取并解析一个 JSON/YAML object 请求体。
func Load(ctx context.Context, name string, stdin io.Reader) (map[string]any, error) {
	data, err := LoadText(ctx, name, stdin)
	if err != nil {
		return nil, err
	}
	return Parse(name, []byte(data))
}

// LoadText 从文件或 stdin 读取受大小限制的文本。
func LoadText(ctx context.Context, name string, stdin io.Reader) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var reader io.Reader
	var file *os.File
	if name == "-" {
		reader = stdin
	} else {
		opened, err := os.Open(name)
		if err != nil {
			return "", result.New("input", i18n.T("Unable to read request body file"))
		}
		file = opened
		defer file.Close()
		reader = file
	}
	if reader == nil {
		return "", result.New("input", i18n.T("Unable to read request body"))
	}
	data, err := read(ctx, reader)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// Parse 解析 JSON 或 YAML。扩展名未知时根据首个非空字节选择格式。
func Parse(name string, data []byte) (map[string]any, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, result.New("input", i18n.T("Request body is empty"))
	}
	format := strings.ToLower(name)
	switch {
	case strings.HasSuffix(format, ".json"):
		return parseJSON(data)
	case strings.HasSuffix(format, ".yaml"), strings.HasSuffix(format, ".yml"):
		return parseYAML(data)
	default:
		trimmed := bytes.TrimSpace(data)
		if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') {
			return parseJSON(data)
		}
		return parseYAML(data)
	}
}

func read(ctx context.Context, reader io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(contextReader{ctx: ctx, reader: reader}, maxBytes+1))
	if ctx.Err() != nil {
		return nil, result.New("network", i18n.T("Request cancelled"))
	}
	if err != nil {
		return nil, result.New("input", i18n.T("Failed to read request body"))
	}
	if len(data) > maxBytes {
		return nil, result.New("input", i18n.T("Request body is too large"))
	}
	return data, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(buffer []byte) (int, error) {
	select {
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	default:
		return r.reader.Read(buffer)
	}
}

func parseJSON(data []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := decodeJSONValue(decoder)
	if err != nil {
		return nil, result.New("input", i18n.T("Invalid request body JSON"))
	}
	if token, err := decoder.Token(); !errors.Is(err, io.EOF) || token != nil {
		return nil, result.New("input", i18n.T("Request body contains extra content"))
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, result.New("input", i18n.T("Request body root must be a JSON object"))
	}
	return object, nil
}

func decodeJSONValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch delimiter := token.(type) {
	case json.Delim:
		switch delimiter {
		case '{':
			object := map[string]any{}
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return nil, err
				}
				key, ok := keyToken.(string)
				if !ok {
					return nil, errors.New(i18n.T("object key is not a string"))
				}
				if _, exists := object[key]; exists {
					return nil, errors.New(i18n.T("duplicate object key"))
				}
				value, err := decodeJSONValue(decoder)
				if err != nil {
					return nil, err
				}
				object[key] = value
			}
			_, err = decoder.Token()
			return object, err
		case '[':
			values := []any{}
			for decoder.More() {
				value, err := decodeJSONValue(decoder)
				if err != nil {
					return nil, err
				}
				values = append(values, value)
			}
			_, err = decoder.Token()
			return values, err
		default:
			return nil, errors.New(i18n.T("unexpected delimiter"))
		}
	default:
		return token, nil
	}
}

func parseYAML(data []byte) (map[string]any, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil || len(document.Content) != 1 {
		return nil, result.New("input", i18n.T("Invalid request body YAML"))
	}
	root := document.Content[0]
	if err := validateYAMLNode(root); err != nil {
		return nil, result.New("input", err.Error())
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, result.New("input", i18n.T("Request body contains extra YAML documents"))
	}
	if root.Kind != yaml.MappingNode {
		return nil, result.New("input", i18n.T("Request body root must be a JSON object"))
	}
	var object map[string]any
	if err := yaml.Unmarshal(data, &object); err != nil {
		return nil, result.New("input", i18n.T("Invalid request body YAML"))
	}
	if object == nil {
		return nil, result.New("input", i18n.T("Request body root must be a JSON object"))
	}
	return object, nil
}

func validateYAMLNode(node *yaml.Node) error {
	if node.Kind == yaml.AliasNode {
		return errors.New(i18n.T("YAML request bodies do not support aliases"))
	}
	if node.Tag != "" && !knownYAMLTag(node) {
		return errors.New(i18n.T("YAML request body contains an unknown tag"))
	}
	switch node.Kind {
	case yaml.MappingNode:
		seen := make(map[string]struct{}, len(node.Content)/2)
		for index := 0; index < len(node.Content); index += 2 {
			key := node.Content[index]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
				return errors.New(i18n.T("YAML request body keys must be strings"))
			}
			if _, exists := seen[key.Value]; exists {
				return errors.New(i18n.T("Request body contains duplicate keys"))
			}
			seen[key.Value] = struct{}{}
			if err := validateYAMLNode(node.Content[index+1]); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		for _, child := range node.Content {
			if err := validateYAMLNode(child); err != nil {
				return err
			}
		}
	}
	return nil
}

func knownYAMLTag(node *yaml.Node) bool {
	if node.Tag == "!" {
		return true
	}
	switch node.Kind {
	case yaml.MappingNode:
		return node.Tag == "!!map"
	case yaml.SequenceNode:
		return node.Tag == "!!seq"
	case yaml.ScalarNode:
		switch node.Tag {
		case "!!str", "!!int", "!!float", "!!bool", "!!null", "!!timestamp", "!!binary":
			return true
		default:
			return false
		}
	default:
		return false
	}
}

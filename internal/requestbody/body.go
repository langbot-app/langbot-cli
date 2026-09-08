package requestbody

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/langbot-app/langbot-cli/internal/result"
	"go.yaml.in/yaml/v3"
)

const maxBytes = 1 << 20

// ValidateSources 确保 API Key 和请求体不会同时读取 stdin。
func ValidateSources(name string, apiKeyStdin bool) error {
	if strings.TrimSpace(name) == "" {
		return result.New("input", "必须指定请求体文件")
	}
	if name == "-" && apiKeyStdin {
		return result.New("input", "请求体 stdin 不能与 --api-key-stdin 同时使用")
	}
	return nil
}

// Load 从文件或 stdin 读取并解析一个 JSON/YAML object 请求体。
func Load(ctx context.Context, name string, stdin io.Reader) (map[string]any, error) {
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
			return nil, result.New("input", "无法读取请求体文件")
		}
		file = opened
		defer file.Close()
		reader = file
	}
	if reader == nil {
		return nil, result.New("input", "无法读取请求体")
	}
	data, err := read(ctx, reader)
	if err != nil {
		return nil, err
	}
	return Parse(name, data)
}

// Parse 解析 JSON 或 YAML。扩展名未知时根据首个非空字节选择格式。
func Parse(name string, data []byte) (map[string]any, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, result.New("input", "请求体为空")
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
		return nil, result.New("network", "请求已取消")
	}
	if err != nil {
		return nil, result.New("input", "读取请求体失败")
	}
	if len(data) > maxBytes {
		return nil, result.New("input", "请求体过大")
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
		return nil, result.New("input", "请求体 JSON 格式无效")
	}
	if token, err := decoder.Token(); !errors.Is(err, io.EOF) || token != nil {
		return nil, result.New("input", "请求体包含多余内容")
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, result.New("input", "请求体根节点必须是 JSON object")
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
					return nil, errors.New("object key is not a string")
				}
				if _, exists := object[key]; exists {
					return nil, errors.New("duplicate object key")
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
			return nil, errors.New("unexpected delimiter")
		}
	default:
		return token, nil
	}
}

func parseYAML(data []byte) (map[string]any, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil || len(document.Content) != 1 {
		return nil, result.New("input", "请求体 YAML 格式无效")
	}
	root := document.Content[0]
	if err := validateYAMLNode(root); err != nil {
		return nil, result.New("input", err.Error())
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, result.New("input", "请求体包含多余 YAML 文档")
	}
	if root.Kind != yaml.MappingNode {
		return nil, result.New("input", "请求体根节点必须是 JSON object")
	}
	var object map[string]any
	if err := yaml.Unmarshal(data, &object); err != nil {
		return nil, result.New("input", "请求体 YAML 格式无效")
	}
	if object == nil {
		return nil, result.New("input", "请求体根节点必须是 JSON object")
	}
	return object, nil
}

func validateYAMLNode(node *yaml.Node) error {
	if node.Kind == yaml.AliasNode {
		return errors.New("YAML 请求体不支持别名")
	}
	if node.Tag != "" && !knownYAMLTag(node) {
		return errors.New("YAML 请求体包含未知标签")
	}
	switch node.Kind {
	case yaml.MappingNode:
		seen := make(map[string]struct{}, len(node.Content)/2)
		for index := 0; index < len(node.Content); index += 2 {
			key := node.Content[index]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
				return errors.New("YAML 请求体键必须是字符串")
			}
			if _, exists := seen[key.Value]; exists {
				return errors.New("请求体包含重复键")
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

package output

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

const (
	FormatHuman = "human"
	FormatJSON  = "json"
	FormatYAML  = "yaml"
)

func NormalizeFormat(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case FormatJSON:
		return FormatJSON, nil
	case FormatYAML, "yml":
		return FormatYAML, nil
	default:
		return "", fmt.Errorf("不支持的输出格式 %q", value)
	}
}

func Render(w io.Writer, value any, format string) error {
	return RenderCommand(w, value, format, "")
}

// RenderCommand 在统一脱敏结果上选择机器编码或人工摘要。
func RenderCommand(w io.Writer, value any, format, command string) error {
	if format == FormatHuman {
		safe, err := prepare(value)
		if err != nil {
			return err
		}
		return renderHuman(w, safe, command)
	}
	format, err := NormalizeFormat(format)
	if err != nil {
		return err
	}
	safe, err := prepare(value)
	if err != nil {
		return err
	}
	switch format {
	case FormatJSON:
		data, err := json.MarshalIndent(safe, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(w, string(data))
		return err
	case FormatYAML:
		data, err := yaml.Marshal(yamlValue(safe))
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	default:
		return fmt.Errorf("不支持的输出格式 %q", format)
	}
}

func prepare(value any) (any, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, err
	}
	return redactValue(decoded, ""), nil
}

func redactValue(value any, field string) any {
	if isSecretField(field) {
		if isMaskedSecretValue(value) {
			return value
		}
		return "[REDACTED]"
	}
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			result[key] = redactValue(item, key)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for i, item := range typed {
			result[i] = redactValue(item, "")
		}
		return result
	default:
		return value
	}
}

func isMaskedSecretValue(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case string:
		return typed == "" || typed == "***"
	case map[string]any:
		for _, item := range typed {
			if !isMaskedSecretValue(item) {
				return false
			}
		}
		return true
	case []any:
		for _, item := range typed {
			if !isMaskedSecretValue(item) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func yamlValue(value any) any {
	switch typed := value.(type) {
	case json.Number:
		if integer, err := strconv.ParseInt(string(typed), 10, 64); err == nil {
			return integer
		}
		if decimal, err := strconv.ParseFloat(string(typed), 64); err == nil {
			return decimal
		}
		return string(typed)
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			result[key] = yamlValue(item)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for i, item := range typed {
			result[i] = yamlValue(item)
		}
		return result
	default:
		return value
	}
}

func formatCell(value any) string {
	if value == nil {
		return ""
	}
	if typed, ok := value.(string); ok {
		var builder strings.Builder
		for _, char := range typed {
			switch char {
			case '\t':
				builder.WriteString("\\t")
			case '\r':
				builder.WriteString("\\r")
			case '\n':
				builder.WriteString("\\n")
			default:
				if char < 0x20 || char == 0x7f || (char >= 0x80 && char <= 0x9f) {
					builder.WriteString("\\u")
					fmt.Fprintf(&builder, "%04x", char)
				} else {
					builder.WriteRune(char)
				}
			}
		}
		return builder.String()
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "[unprintable]"
	}
	return formatCell(string(data))
}

func isSecretField(name string) bool {
	name = strings.ToLower(name)
	switch name {
	case "api_key_id", "token_count", "input_tokens", "output_tokens", "total_tokens", "max_tokens":
		return false
	}
	for _, part := range []string{"apikey", "api_key", "token", "password", "secret", "authorization"} {
		if strings.Contains(name, part) {
			return true
		}
	}
	return false
}

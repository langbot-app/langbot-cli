package output

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"go.yaml.in/yaml/v3"
)

const (
	FormatJSON  = "json"
	FormatTable = "table"
	FormatYAML  = "yaml"
)

func NormalizeFormat(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", FormatJSON:
		return FormatJSON, nil
	case FormatTable:
		return FormatTable, nil
	case FormatYAML, "yml":
		return FormatYAML, nil
	default:
		return "", fmt.Errorf("不支持的输出格式 %q", value)
	}
}

func Render(w io.Writer, value any, format string) error {
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
	case FormatTable:
		return renderTable(w, safe)
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

func renderTable(w io.Writer, value any) error {
	if value == nil {
		_, err := fmt.Fprintln(w, "-")
		return err
	}
	if envelope, ok := value.(map[string]any); ok {
		if data, exists := envelope["data"]; exists {
			return renderEnvelopeTable(w, envelope, data)
		}
	}
	switch typed := value.(type) {
	case []any:
		return renderRows(w, typed)
	case map[string]any:
		return renderKeyValues(w, typed, "")
	default:
		_, err := fmt.Fprintln(w, formatCell(typed))
		return err
	}
}

func renderEnvelopeTable(w io.Writer, envelope map[string]any, data any) error {
	if err := renderEnvelopeMeta(w, envelope); err != nil {
		return err
	}
	dataMap, isObject := data.(map[string]any)
	if !isObject {
		return renderKeyValues(w, map[string]any{"data": data}, "")
	}
	itemKey := ""
	var items []any
	for _, key := range []string{"contexts", "checks"} {
		if candidate, ok := dataMap[key].([]any); ok {
			itemKey, items = key, candidate
			break
		}
	}
	if itemKey == "" {
		return renderKeyValues(w, map[string]any{"data": data}, "")
	}
	other := make(map[string]any, len(dataMap))
	for key, value := range dataMap {
		if key != itemKey {
			other[key] = value
		}
	}
	if len(other) != 0 {
		if err := renderKeyValues(w, other, "data"); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(w, itemKey); err != nil {
		return err
	}
	if len(items) == 0 {
		return nil
	}
	rows := make([]map[string]any, 0, len(items))
	for _, item := range items {
		row, ok := item.(map[string]any)
		if !ok {
			row = map[string]any{"value": item}
		}
		rows = append(rows, row)
	}
	return renderMaps(w, rows)
}

func renderEnvelopeMeta(w io.Writer, envelope map[string]any) error {
	meta := make(map[string]any)
	for _, key := range []string{"ok", "error", "meta"} {
		if value, exists := envelope[key]; exists {
			meta[key] = value
		}
	}
	if len(meta) == 0 {
		return nil
	}
	return renderKeyValues(w, meta, "")
}

func renderRows(rowsWriter io.Writer, rows []any) error {
	objects := make([]map[string]any, 0, len(rows))
	for _, item := range rows {
		if row, ok := item.(map[string]any); ok {
			objects = append(objects, row)
		} else {
			objects = append(objects, map[string]any{"value": item})
		}
	}
	if len(objects) == 0 {
		_, err := fmt.Fprintln(rowsWriter, "-")
		return err
	}
	return renderMaps(rowsWriter, objects)
}

func renderMaps(w io.Writer, rows []map[string]any) error {
	columns := make([]string, 0)
	seen := make(map[string]struct{})
	for _, row := range rows {
		for key := range row {
			if _, ok := seen[key]; !ok {
				seen[key] = struct{}{}
				columns = append(columns, key)
			}
		}
	}
	sort.Strings(columns)
	if len(columns) == 0 {
		_, err := fmt.Fprintln(w, "-")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, strings.Join(columns, "\t")); err != nil {
		return err
	}
	for _, row := range rows {
		cells := make([]string, len(columns))
		for i, column := range columns {
			cells[i] = formatCell(row[column])
		}
		if _, err := fmt.Fprintln(tw, strings.Join(cells, "\t")); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func renderKeyValues(w io.Writer, value map[string]any, prefix string) error {
	rows := make([][]string, 0)
	var visit func(string, any)
	visit = func(key string, item any) {
		switch typed := item.(type) {
		case map[string]any:
			keys := make([]string, 0, len(typed))
			for child := range typed {
				keys = append(keys, child)
			}
			sort.Strings(keys)
			for _, child := range keys {
				name := child
				if key != "" {
					name = key + "." + child
				}
				visit(name, typed[child])
			}
		case []any:
			if len(typed) == 0 {
				rows = append(rows, []string{key, "[]"})
				return
			}
			for index, child := range typed {
				visit(fmt.Sprintf("%s[%d]", key, index), child)
			}
		default:
			rows = append(rows, []string{key, formatCell(item)})
		}
	}
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		name := key
		if prefix != "" {
			name = prefix + "." + key
		}
		visit(name, value[key])
	}
	if len(rows) == 0 {
		_, err := fmt.Fprintln(w, "-")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "field\tvalue"); err != nil {
		return err
	}
	for _, row := range rows {
		if _, err := fmt.Fprintf(tw, "%s\t%s\n", formatCell(row[0]), row[1]); err != nil {
			return err
		}
	}
	return tw.Flush()
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
	if name == "api_key_id" {
		return false
	}
	for _, part := range []string{"apikey", "api_key", "token", "password", "secret", "authorization"} {
		if strings.Contains(name, part) {
			return true
		}
	}
	return false
}

package app

import (
	"fmt"
	"sort"

	"github.com/langbot-app/langbot-cli/internal/redact"
)

// verifyResourceWrite compares submitted fields, not just resource existence.
// Redacted values cannot prove persistence; expose that limit explicitly.
func verifyResourceWrite(preflight WritePreflight, data, expected, actual map[string]any) (Result, error) {
	checked, hidden := []string{}, []string{}
	matched := verifyResourceValue(expected, actual, "", &checked, &hidden)
	sort.Strings(checked)
	sort.Strings(hidden)
	data["verified"] = matched && len(hidden) == 0
	data["verification"] = map[string]any{
		"scope": "submitted_fields", "observable_fields_match": matched,
		"checked_fields": checked, "unverified_fields": hidden,
	}
	finished := Result{Data: data, Meta: preflight.Meta()}
	if !matched {
		return finished, verificationError("写操作已返回成功，但回读的提交字段不一致；资源可能已修改，请检查后再操作")
	}
	return finished, nil
}

func verifyResourceValue(expected, actual any, path string, checked, hidden *[]string) bool {
	// A server-side whole-value mask is also unobservable, regardless of field name.
	if text, ok := actual.(string); ok && (text == "***" || text == redact.Mask) {
		*hidden = append(*hidden, path)
		return true
	}
	switch value := expected.(type) {
	case map[string]any:
		got, ok := actual.(map[string]any)
		if !ok || (path != "" && len(value) != len(got)) {
			return false
		}
		matched := true
		for key, item := range value {
			child := key
			if path != "" {
				child = path + "." + key
			}
			actualItem, exists := got[key]
			if !exists {
				matched = false
				continue
			}
			if redact.IsSecretField(key) {
				if actualItem == nil || actualItem == "" {
					*checked = append(*checked, child)
					if !observableConfigEqual(item, actualItem) {
						matched = false
					}
					continue
				}
				*hidden = append(*hidden, child)
				continue
			}
			if !verifyResourceValue(item, actualItem, child, checked, hidden) {
				matched = false
			}
		}
		if len(value) == 0 {
			*checked = append(*checked, path)
		}
		return matched
	case []any:
		got, ok := actual.([]any)
		if !ok || len(value) != len(got) {
			return false
		}
		matched := true
		for i, item := range value {
			if !verifyResourceValue(item, got[i], fmt.Sprintf("%s[%d]", path, i), checked, hidden) {
				matched = false
			}
		}
		if len(value) == 0 {
			*checked = append(*checked, path)
		}
		return matched
	default:
		// URL masking and connection-key replacement use the same visible projection.
		// Do not compare credential text or include values in diagnostics.
		field := path
		for i := len(field) - 1; i >= 0; i-- {
			if field[i] == '.' {
				field = field[i+1:]
				break
			}
		}
		visible := redact.Configuration(expected, field)
		if !observableConfigEqual(visible, expected) {
			*hidden = append(*hidden, path)
		}
		*checked = append(*checked, path)
		return observableConfigEqual(visible, actual)
	}
}

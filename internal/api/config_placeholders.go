package api

import (
	"strings"

	"github.com/langbot-app/langbot-cli/internal/redact"
	"github.com/langbot-app/langbot-cli/internal/result"
)

func hasConfigPlaceholder(value any) bool {
	switch v := value.(type) {
	case string:
		return strings.Contains(v, "***") || strings.Contains(v, redact.Mask)
	case map[string]any:
		for _, item := range v {
			if hasConfigPlaceholder(item) {
				return true
			}
		}
	case []any:
		for _, item := range v {
			if hasConfigPlaceholder(item) {
				return true
			}
		}
	}
	return false
}

// Restore only exact placeholders. Partial URL placeholders fail closed rather
// than accidentally changing a credential or restoring it to a different host.
func restoreConfigPlaceholders(expected, current any) (any, error) {
	failure := func() (any, error) {
		return nil, result.New("precondition", "无法安全保留脱敏的 Bot 配置，请提供该字段的实际值；未发送更新")
	}
	switch v := expected.(type) {
	case string:
		if v == "***" || v == redact.Mask {
			if current == nil || hasConfigPlaceholder(current) {
				return failure()
			}
			return current, nil
		}
		if hasConfigPlaceholder(v) {
			return failure()
		}
		return v, nil
	case map[string]any:
		old, _ := current.(map[string]any)
		copy := make(map[string]any, len(v))
		for key, item := range v {
			restored, err := restoreConfigPlaceholders(item, old[key])
			if err != nil {
				return nil, err
			}
			copy[key] = restored
		}
		return copy, nil
	case []any:
		old, _ := current.([]any)
		copy := make([]any, len(v))
		for i, item := range v {
			var previous any
			if i < len(old) {
				previous = old[i]
			}
			restored, err := restoreConfigPlaceholders(item, previous)
			if err != nil {
				return nil, err
			}
			copy[i] = restored
		}
		return copy, nil
	default:
		return expected, nil
	}
}

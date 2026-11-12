package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/langbot-app/langbot-cli/internal/result"
)

func TestResourceVerificationChecksSubmittedFields(t *testing.T) {
	for _, test := range []struct {
		name              string
		expected, actual  map[string]any
		matches, verified bool
	}{
		{"unchanged name", map[string]any{"name": "new"}, map[string]any{"name": "old"}, false, false},
		{"missing false", map[string]any{"enable": false}, map[string]any{}, false, false},
		{"changed false", map[string]any{"enable": false}, map[string]any{"enable": true}, false, false},
		{"nested ignored", map[string]any{"config": map[string]any{"temperature": json.Number("0.5")}}, map[string]any{"config": map[string]any{"temperature": json.Number("0.7")}}, false, false},
		{"numeric representations and server defaults", map[string]any{"config": map[string]any{"limit": 2}}, map[string]any{"config": map[string]any{"limit": json.Number("2")}, "default": true}, true, true},
		{"ignored config clearing", map[string]any{"config": map[string]any{}}, map[string]any{"config": map[string]any{"old": true}}, false, false},
		{"masked credentials", map[string]any{"name": "new", "adapter_config": map[string]any{"token": "sensitive-value"}}, map[string]any{"name": "new", "adapter_config": map[string]any{"token": "***"}}, true, false},
		{"whole config masked", map[string]any{"config": map[string]any{"limit": 2}}, map[string]any{"config": "***"}, true, false},
		{"array changed", map[string]any{"stages": []any{"one", "two"}}, map[string]any{"stages": []any{"two", "one"}}, false, false},
		{"missing credential", map[string]any{"config": map[string]any{"token": "sensitive-value"}}, map[string]any{"config": map[string]any{}}, false, false},
		{"credential not saved", map[string]any{"config": map[string]any{"token": "sensitive-value"}}, map[string]any{"config": map[string]any{"token": ""}}, false, false},
		{"credential clearing observable", map[string]any{"config": map[string]any{"token": ""}}, map[string]any{"config": map[string]any{"token": ""}}, true, true},
		{"binding defaults", map[string]any{"event_bindings": []any{map[string]any{"target_uuid": "pipeline-a"}}}, map[string]any{"event_bindings": []any{map[string]any{"target_uuid": "pipeline-a", "id": "generated", "priority": 0}}}, true, true},
		{"submitted binding ID changed", map[string]any{"event_bindings": []any{map[string]any{"id": "expected"}}}, map[string]any{"event_bindings": []any{map[string]any{"id": "different", "priority": 0}}}, false, false},
		{"binding clearing ignored", map[string]any{"event_bindings": []any{}}, map[string]any{"event_bindings": []any{map[string]any{"id": "existing"}}}, false, false},
		{"binding order changed", map[string]any{"event_bindings": []any{map[string]any{"target_uuid": "a"}, map[string]any{"target_uuid": "b"}}}, map[string]any{"event_bindings": []any{map[string]any{"target_uuid": "b"}, map[string]any{"target_uuid": "a"}}}, false, false},
		{"binding filter clearing ignored", map[string]any{"event_bindings": []any{map[string]any{"filters": []any{}}}}, map[string]any{"event_bindings": []any{map[string]any{"id": "generated", "filters": []any{map[string]any{"field": "text"}}}}}, false, false},
		{"processor defaults", map[string]any{"plugin_processors": []any{map[string]any{"processor_uuid": "processor-a"}}}, map[string]any{"plugin_processors": []any{map[string]any{"processor_uuid": "processor-a", "enabled": true}}}, true, true},
		{"other records remain exact", map[string]any{"config": []any{map[string]any{"limit": 2}}}, map[string]any{"config": []any{map[string]any{"limit": 2, "extra": true}}}, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := verifyResourceWrite(WritePreflight{}, writeResultData("bot.update", "test"), test.expected, test.actual)
			data := got.Data.(map[string]any)
			if (err == nil) != test.matches || data["verified"] != test.verified {
				t.Fatalf("unexpected verification: %#v, %v", data, err)
			}
			if err != nil && (result.AsError(err).Type != "verification_failed" || strings.Contains(err.Error(), "sensitive-value")) {
				t.Fatalf("unsafe failure: %v", err)
			}
		})
	}
}

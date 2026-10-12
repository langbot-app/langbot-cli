package redact

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestConfigurationRetainsOrdinaryFieldsAndMasksCredentials(t *testing.T) {
	input := map[string]any{"port": 5300, "enabled": false, "max_tokens": 2048, "headers": map[string]any{"Content-Type": "application/json", "Authorization": "Bearer private-auth"}, "items": []any{map[string]any{"clientKey": "private-key", "temperature": 0.5}}, "url": "https://alice:private-password@example.com/api?api_key=private-query&limit=3"}
	got := Configuration(input, "").(map[string]any)
	encoded, _ := json.Marshal(got)
	for _, secret := range []string{"private-auth", "private-key", "private-password", "private-query"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("credential leaked: %s", encoded)
		}
	}
	if got["port"] != 5300 || got["enabled"] != false || got["max_tokens"] != 2048 || got["headers"].(map[string]any)["Content-Type"] != "application/json" {
		t.Fatalf("ordinary configuration lost: %#v", got)
	}
	if input["headers"].(map[string]any)["Authorization"] != "Bearer private-auth" {
		t.Fatal("input mutated")
	}
}

func TestConfigurationURLMaskingFailsClosed(t *testing.T) {
	if got := Configuration("https://example.com?api_key=private;other=value", "url"); got != "***" {
		t.Fatalf("malformed query was not masked: %v", got)
	}
	if got := Configuration("https://example.com?SIG=private-signature", "url"); strings.Contains(got.(string), "private-signature") {
		t.Fatal("uppercase signature leaked")
	}
}

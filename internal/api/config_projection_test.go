package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResourceConfigProjectionPreservesSafeNestedSettings(t *testing.T) {
	config := map[string]any{"enabled": false, "timeout": json.Number("11"), "headers": map[string]any{"Content-Type": "application/json", "Authorization": "private-auth"}, "items": []any{map[string]any{"value": "connection-secret", "count": json.Number("2")}}}
	for _, test := range []struct {
		name, field string
		project     func(map[string]any, string) map[string]any
	}{
		{"bot", "adapter_config", projectBot},
		{"pipeline", "config", projectPipeline},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := test.project(map[string]any{"uuid": "test", test.field: config, "unknown": "untrusted"}, "connection-secret")
			visible := got[test.field].(map[string]any)
			if visible["enabled"] != false || visible["timeout"] != json.Number("11") || visible["headers"].(map[string]any)["Content-Type"] != "application/json" {
				t.Fatalf("ordinary config lost: %#v", got)
			}
			if _, exists := got["unknown"]; exists {
				t.Fatal("unknown top-level field exposed")
			}
			encoded, _ := json.Marshal(got)
			if strings.Contains(string(encoded), "private-auth") || strings.Contains(string(encoded), "connection-secret") {
				t.Fatal("credential leaked")
			}
		})
	}
}

func TestBotMaskedReadModifyWritePreservesCredentials(t *testing.T) {
	var writes int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			fmt.Fprint(w, `{"code":0,"data":{"bot":{"uuid":"bot-a","adapter_config":{"inbound_secret":"original-private-value","callback_timeout":7}}}}`)
			return
		}
		writes++
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		config := body["adapter_config"].(map[string]any)
		if config["inbound_secret"] != "original-private-value" || config["callback_timeout"] != float64(11) {
			t.Error("masked update overwrote credential or dropped setting")
		}
		fmt.Fprint(w, `{"code":0,"data":null}`)
	}))
	defer server.Close()
	body := map[string]any{"adapter_config": map[string]any{"inbound_secret": "***", "callback_timeout": 11}}
	_, err := (Client{}).BotUpdate(context.Background(), Target{Endpoint: server.URL, APIKey: "connection-key"}, "bot-a", body)
	if err != nil || writes != 1 {
		t.Fatalf("update failed: writes=%d err=%v", writes, err)
	}
	if body["adapter_config"].(map[string]any)["inbound_secret"] != "***" {
		t.Fatal("caller received a restored credential")
	}
}

func TestBotMaskedUpdateFailsClosedWhenSecretUnavailable(t *testing.T) {
	for _, current := range []string{`{"token":"***"}`, `{}`} {
		var writes int
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				writes++
				t.Error("unsafe update was sent")
			}
			fmt.Fprintf(w, `{"code":0,"data":{"bot":{"uuid":"bot-a","adapter_config":%s}}}`, current)
		}))
		_, err := (Client{}).BotUpdate(context.Background(), Target{Endpoint: server.URL}, "bot-a", map[string]any{"adapter_config": map[string]any{"token": "***"}})
		server.Close()
		if err == nil || writes != 0 {
			t.Fatalf("unsafe masked update accepted: %v", err)
		}
	}
}

func TestConfigPlaceholderRestorationHandlesArraysAndRejectsPartialURLs(t *testing.T) {
	got, err := restoreConfigPlaceholders([]any{map[string]any{"token": "***", "enabled": false}}, []any{map[string]any{"token": "private-value", "enabled": true}})
	if err != nil || got.([]any)[0].(map[string]any)["token"] != "private-value" {
		t.Fatal("nested placeholder not preserved")
	}
	if _, err := restoreConfigPlaceholders("https://***@example.com", "https://user:password@example.com"); err == nil {
		t.Fatal("partial credential placeholder was accepted")
	}
}

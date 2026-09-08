package requestbody

import (
	"context"
	"strings"
	"testing"

	"github.com/langbot-app/langbot-cli/internal/result"
)

func TestParseJSONObjectRejectsTrailingAndDuplicateKeys(t *testing.T) {
	for _, body := range []string{
		`{"name":"safe","name":"secret"}`,
		`{"name":"safe"} {"token":"secret"}`,
		`["not-an-object"]`,
	} {
		_, err := Parse("body.json", []byte(body))
		if result.AsError(err).Kind != "input" {
			t.Fatalf("Parse(%q) error = %+v, want input", body, result.AsError(err))
		}
		if strings.Contains(err.Error(), "secret") {
			t.Fatalf("Parse(%q) exposed body content: %v", body, err)
		}
	}
}

func TestParseYAMLRejectsUnknownTagsDuplicateKeysAndNonStringKeys(t *testing.T) {
	for _, body := range []string{
		"name: safe\nname: secret\n",
		"!custom\nname: secret\n",
		"1: secret\n",
		"- secret\n",
	} {
		_, err := Parse("body.yaml", []byte(body))
		if result.AsError(err).Kind != "input" {
			t.Fatalf("Parse(%q) error = %+v, want input", body, result.AsError(err))
		}
		if strings.Contains(err.Error(), "secret") {
			t.Fatalf("Parse(%q) exposed body content: %v", body, err)
		}
	}
}

func TestParseYAMLObjectAndLoadStdin(t *testing.T) {
	value, err := Parse("body.yaml", []byte("name: bot\nenabled: true\nconfig:\n  retries: 2\n"))
	if err != nil || value["name"] != "bot" || value["enabled"] != true {
		t.Fatalf("Parse YAML = %#v, error = %v", value, err)
	}
	loaded, err := Load(context.Background(), "-", strings.NewReader("name: pipeline\n"))
	if err != nil || loaded["name"] != "pipeline" {
		t.Fatalf("Load stdin = %#v, error = %v", loaded, err)
	}
}

func TestValidateSourcesRejectsSharedStdin(t *testing.T) {
	if err := ValidateSources("-", true); result.AsError(err).Kind != "input" {
		t.Fatalf("shared stdin error = %+v, want input", result.AsError(err))
	}
	if err := ValidateSources("body.json", true); err != nil {
		t.Fatalf("file body with API key stdin failed: %v", err)
	}
}

func TestLoadRejectsOversizedInputAndHonorsCancellation(t *testing.T) {
	_, err := Load(context.Background(), "-", strings.NewReader(strings.Repeat("x", maxBytes+1)))
	if result.AsError(err).Kind != "input" || !strings.Contains(err.Error(), "过大") {
		t.Fatalf("oversized body error = %+v", result.AsError(err))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = Load(ctx, "-", strings.NewReader("name: delayed"))
	if result.AsError(err).Kind != "network" {
		t.Fatalf("cancelled body error = %+v", result.AsError(err))
	}
}

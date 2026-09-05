package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/langbot-app/langbot-cli/internal/command"
)

type execution struct {
	code int
	out  string
	data map[string]any
}

func invokeRaw(path string, env map[string]string, in io.Reader, args ...string) (int, string, string, error) {
	return invokeRawLookupContext(context.Background(), path, env, func(name string) (string, bool) {
		value, ok := env[name]
		return value, ok
	}, in, args...)
}

func invokeRawLookup(path string, env map[string]string, lookup func(string) (string, bool), in io.Reader, args ...string) (int, string, string, error) {
	return invokeRawLookupContext(context.Background(), path, env, lookup, in, args...)
}

func invokeRawLookupContext(ctx context.Context, path string, env map[string]string, lookup func(string) (string, bool), in io.Reader, args ...string) (int, string, string, error) {
	var out, diagnostic bytes.Buffer
	code := command.Execute(ctx, args, command.Dependencies{
		In: in, Out: &out, Err: &diagnostic,
		LookupEnv:         lookup,
		DefaultConfigPath: func() (string, error) { return path, nil },
		Version:           "test", Commit: "test", BuildDate: "test",
	})
	if diagnostic.Len() != 0 {
		return code, out.String(), diagnostic.String(), fmt.Errorf("unexpected diagnostic output")
	}
	for name, secret := range env {
		if (strings.HasSuffix(name, "_KEY") || name == "LANGBOT_API_KEY") && len(secret) > 4 && strings.Contains(out.String(), secret) {
			return code, out.String(), diagnostic.String(), fmt.Errorf("command leaked credential from %s", name)
		}
	}
	return code, out.String(), diagnostic.String(), nil
}

func invoke(path string, env map[string]string, in io.Reader, args ...string) (int, string, string, error) {
	code, out, diagnostic, err := invokeRaw(path, env, in, args...)
	if err != nil {
		return code, out, diagnostic, err
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(out), &envelope); err != nil {
		return code, out, diagnostic, fmt.Errorf("command did not return one JSON result: %w", err)
	}
	return code, out, diagnostic, nil
}

func run(t *testing.T, path string, env map[string]string, stdin string, args ...string) execution {
	t.Helper()
	code, out, _, err := invoke(path, env, strings.NewReader(stdin), args...)
	if err != nil {
		t.Fatalf("command %v failed to capture result: %v; output %q", args, err, out)
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(out), &envelope); err != nil {
		t.Fatalf("command %v did not return one JSON result: %v; output %q", args, err, out)
	}
	return execution{code: code, out: out, data: envelope}
}

func runLookup(t *testing.T, path string, env map[string]string, lookup func(string) (string, bool), in io.Reader, args ...string) execution {
	t.Helper()
	code, out, _, err := invokeRawLookup(path, env, lookup, in, args...)
	if err != nil {
		t.Fatalf("command %v failed to capture result: %v; output %q", args, err, out)
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(out), &envelope); err != nil {
		t.Fatalf("command %v did not return one JSON result: %v; output %q", args, err, out)
	}
	return execution{code: code, out: out, data: envelope}
}

func runText(t *testing.T, path string, env map[string]string, in io.Reader, args ...string) (int, string) {
	t.Helper()
	code, out, diagnostic, err := invokeRaw(path, env, in, args...)
	if err != nil {
		t.Fatalf("command %v failed: %v; output %q; diagnostic %q", args, err, out, diagnostic)
	}
	return code, out
}

func requireSuccess(t *testing.T, r execution) {
	t.Helper()
	if r.code != 0 || r.data["ok"] != true {
		t.Fatalf("command failed: exit=%d %s", r.code, r.out)
	}
}

func TestContextSnapshotAndCredentialIsolation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	keys := map[string]string{"ALPHA_KEY": "alpha-secret-value", "BETA_KEY": "beta-secret-value"}
	started, release := make(chan struct{}, 1), make(chan struct{})
	var hold atomic.Bool
	var crossed atomic.Bool
	var countA, countB atomic.Int32
	server := func(version, expected string, count *atomic.Int32, wait bool) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			count.Add(1)
			if r.URL.Path != "/prefix/api/v1/system/info" || r.Header.Get("X-API-Key") != expected {
				crossed.Store(true)
				w.WriteHeader(http.StatusForbidden)
				return
			}
			if wait && hold.Load() {
				started <- struct{}{}
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
			}
			fmt.Fprintf(w, `{"code":0,"data":{"version":%q,"edition":"community"}}`, version)
		}))
	}
	a := server("alpha", keys["ALPHA_KEY"], &countA, true)
	defer a.Close()
	b := server("beta", keys["BETA_KEY"], &countB, false)
	defer b.Close()
	for _, item := range []struct{ name, url, env string }{{"alpha", a.URL, "ALPHA_KEY"}, {"beta", b.URL, "BETA_KEY"}} {
		requireSuccess(t, run(t, path, nil, "", "context", "add", item.name, "--endpoint", item.url+"/prefix", "--api-key-env", item.env))
	}
	requireSuccess(t, run(t, path, nil, "", "context", "use", "alpha"))
	hold.Store(true)
	done := make(chan execution, 1)
	go func() {
		code, out, _, err := invoke(path, keys, strings.NewReader(""), "status")
		if err != nil {
			done <- execution{code: -1, out: err.Error()}
			return
		}
		var data map[string]any
		if err := json.Unmarshal([]byte(out), &data); err != nil {
			done <- execution{code: -1, out: err.Error()}
			return
		}
		done <- execution{code: code, out: out, data: data}
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first request did not start")
	}
	requireSuccess(t, run(t, path, nil, "", "context", "use", "beta"))
	close(release)
	var first execution
	select {
	case first = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight command did not finish")
	}
	requireSuccess(t, first)
	if crossed.Load() {
		t.Fatal("request target or credential crossed contexts")
	}
	if !strings.Contains(first.out, `"server_version": "alpha"`) {
		t.Fatalf("in-flight command lost its snapshot: %s", first.out)
	}
	requireSuccess(t, run(t, path, keys, "", "status"))
	before := countB.Load()
	blocked := run(t, path, keys, "", "--context", "alpha", "--endpoint", b.URL+"/prefix", "status")
	if blocked.code == 0 || countB.Load() != before {
		t.Fatal("endpoint override reused the previous target's key")
	}
	requireSuccess(t, run(t, path, keys, keys["BETA_KEY"]+"\n", "--context", "alpha", "--endpoint", b.URL+"/prefix", "--api-key-stdin", "status"))
	show := run(t, path, nil, "", "context", "show", "alpha")
	requireSuccess(t, show)
	if !strings.Contains(show.out, "env:ALPHA_KEY") {
		t.Fatal("offline show lost the credential reference")
	}
}

func TestAllChecksRetainFailuresAndRejectOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	var calls atomic.Int32
	var wrongCredential atomic.Bool
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch r.Header.Get("X-API-Key") {
		case "good-key-value":
			fmt.Fprint(w, `{"code":0,"data":{"version":"4.10.10","edition":"community"}}`)
		case "denied-key-value":
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"code":403,"msg":"forbidden"}`)
		default:
			wrongCredential.Store(true)
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer s.Close()
	for _, name := range []string{"good", "denied", "missing"} {
		requireSuccess(t, run(t, path, nil, "", "context", "add", name, "--endpoint", s.URL, "--api-key-env", strings.ToUpper(name)+"_KEY"))
	}
	keys := map[string]string{"GOOD_KEY": "good-key-value", "DENIED_KEY": "denied-key-value"}
	r := run(t, path, keys, "", "context", "check", "--all")
	if r.code == 0 || calls.Load() != 2 {
		t.Fatalf("unexpected batch result: calls=%d %s", calls.Load(), r.out)
	}
	if wrongCredential.Load() {
		t.Fatal("batch did not use its context credential")
	}
	checks := r.data["data"].(map[string]any)["checks"].([]any)
	if len(checks) != 3 {
		t.Fatalf("batch lost outcomes: %s", r.out)
	}
	for _, name := range []string{"LANGBOT_API_KEY", "LANGBOT_ENDPOINT"} {
		before := calls.Load()
		blocked := run(t, path, map[string]string{name: ""}, "", "context", "check", "--all")
		if blocked.code != 2 || calls.Load() != before {
			t.Fatal("batch accepted a global override")
		}
	}
}

func TestLiveCore(t *testing.T) {
	endpoint := os.Getenv("LBCTL_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("set LBCTL_TEST_ENDPOINT to run the read-only real Core check")
	}
	r := run(t, filepath.Join(t.TempDir(), "config.yaml"), nil, "", "--endpoint", endpoint, "status")
	requireSuccess(t, r)
	data := r.data["data"].(map[string]any)
	if data["identity"] != "unknown" || data["capabilities"] != "unknown" || data["server_version"] == "" {
		t.Fatalf("unexpected real Core discovery boundary: %s", r.out)
	}
}

func TestInputErrorsNeverMutateOrExposeArguments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	for _, args := range [][]string{
		{"unknown-secret-value"},
		{"context", "unknown-secret-value"},
		{"version", "--unknown-secret-value"},
		{"version", "unexpected-secret-value"},
		{"-o", "invalid-secret-value", "context", "add", "demo", "--endpoint", "http://localhost:5300"},
		{"context", "add", "demo", "--endpoint", "http://localhost:5300", "--api-key-env", ""},
		{"--config=", "context", "list"},
	} {
		r := run(t, path, nil, "", args...)
		if r.code != 2 || r.data["ok"] != false || strings.Contains(r.out, "secret-value") {
			t.Fatalf("invalid input was accepted or echoed: %v: %s", args, r.out)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("invalid arguments changed the config")
	}
}

func TestIdentityCommandsKeepPreconditionWithContextOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	for _, name := range []string{"whoami", "capabilities"} {
		result := run(t, path, nil, "", "--context", "demo", name)
		if result.code != 6 || result.data["ok"] != false {
			t.Fatalf("%s with context override should remain a precondition failure: %s", name, result.out)
		}
	}
}

func TestRawRejectsUnknownOperationsBeforeCallingServer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"code":0,"data":{"version":"4.10.10","edition":"community"}}`)
	}))
	defer s.Close()
	for _, args := range [][]string{
		{"raw", "POST", "/api/v1/system/info"},
		{"raw", "GET", "/api/v1/bots"},
		{"raw", "GET", "//other.example/api/v1/system/info"},
		{"raw", "GET", "/api/v1/system/info?query=1"},
		{"raw", "GET", "/api/v1/system/info", "--file", "body.json"},
		{"raw", "GET", "/api/v1/system/info", "--file", "-", "--api-key-stdin"},
	} {
		args = append([]string{"--endpoint", s.URL}, args...)
		r := run(t, path, nil, "stdin-secret-value", args...)
		if r.code == 0 || calls.Load() != 0 || strings.Contains(r.out, "stdin-secret-value") {
			t.Fatalf("raw bypassed operation policy: %s", r.out)
		}
	}
	requireSuccess(t, run(t, path, nil, "", "--endpoint", s.URL, "raw", "GET", "/api/v1/system/info"))
	if calls.Load() != 1 {
		t.Fatal("known raw operation did not execute exactly once")
	}
}

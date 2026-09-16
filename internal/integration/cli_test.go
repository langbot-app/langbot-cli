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
	code, out, diagnostic, err := invokeRaw(path, env, in, prependJSONOutput(args)...)
	if err != nil {
		return code, out, diagnostic, err
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(out), &envelope); err != nil {
		return code, out, diagnostic, fmt.Errorf("command did not return one JSON result: %w", err)
	}
	return code, out, diagnostic, nil
}

func prependJSONOutput(args []string) []string {
	return append([]string{"--output", "json"}, args...)
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
	code, out, _, err := invokeRawLookup(path, env, lookup, in, prependJSONOutput(args)...)
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
			if supplied := r.Header.Get("X-API-Key"); supplied != expected {
				if supplied != "" {
					crossed.Store(true)
				}
				w.WriteHeader(http.StatusForbidden)
				return
			}
			switch r.URL.Path {
			case "/prefix/api/v1/system/info":
				if wait && hold.Load() {
					started <- struct{}{}
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
				}
				fmt.Fprintf(w, `{"code":0,"data":{"version":%q,"edition":"community"}}`, version)
			case "/prefix/api/v1/system/context":
				fmt.Fprint(w, `{"code":0,"data":{"instance_uuid":"instance-`+version+`","workspace_uuid":"workspace-`+version+`","api_key_id":"key-`+version+`","permissions":[]}}`)
			case "/prefix/api/v1/system/capabilities":
				fmt.Fprint(w, `{"code":0,"data":{"schema_version":1,"operations":{}}}`)
			default:
				crossed.Store(true)
				w.WriteHeader(http.StatusNotFound)
			}
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
		code, out, _, err := invoke(path, keys, strings.NewReader(""), "context", "check")
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
	requireSuccess(t, run(t, path, keys, "", "context", "check"))
	before := countB.Load()
	blocked := run(t, path, keys, "", "--context", "alpha", "--endpoint", b.URL+"/prefix", "context", "check")
	if blocked.code != 3 || countB.Load() != before || crossed.Load() || !strings.Contains(blocked.out, "必须显式提供新的 API Key") {
		t.Fatalf("endpoint override should require a new credential before sending a request: code=%d before=%d after=%d crossed=%t output=%s", blocked.code, before, countB.Load(), crossed.Load(), blocked.out)
	}
	requireSuccess(t, run(t, path, keys, keys["BETA_KEY"]+"\n", "--context", "alpha", "--endpoint", b.URL+"/prefix", "--api-key-stdin", "context", "check"))
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
			if r.URL.Path == "/api/v1/system/context" {
				fmt.Fprint(w, `{"code":0,"data":{"instance_uuid":"instance-a","workspace_uuid":"workspace-a","api_key_id":"key-a","permissions":["read"]}}`)
			} else {
				fmt.Fprint(w, `{"code":0,"data":{"version":"4.10.10","edition":"community"}}`)
			}
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
	if r.code == 0 || calls.Load() != 4 {
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
	r := run(t, filepath.Join(t.TempDir(), "config.yaml"), nil, "", "--endpoint", endpoint, "context", "check")
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
		code, out := runText(t, path, nil, strings.NewReader(""), args...)
		if code != 2 || !strings.Contains(out, "错误：") || strings.Contains(out, "Type:") || strings.Contains(out, "secret-value") {
			t.Fatalf("invalid input was accepted or echoed: %v: %s", args, out)
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("invalid arguments changed the config")
	}
}

func TestIdentityCommandsKeepPreconditionWithContextOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	result := run(t, path, nil, "", "--context", "demo", "whoami")
	if result.code != 2 || result.data["ok"] != false {
		t.Fatalf("whoami with missing context should be an input failure: %s", result.out)
	}
	result = run(t, path, nil, "", "--context", "demo", "capabilities")
	if result.code != 2 || result.data["ok"] != false || !strings.Contains(result.out, `"capabilities": "unknown"`) {
		t.Fatalf("capabilities should resolve its connection before discovery: %s", result.out)
	}
}

func TestCapabilitiesUsesAuthenticatedIdentityAndReportsStableStatuses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	const key = "capability-key-value"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != key {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"code":401,"msg":"unauthorized"}`)
			return
		}
		switch r.URL.Path {
		case "/proxy/api/v1/system/context":
			fmt.Fprint(w, `{"code":0,"data":{"instance_uuid":"instance-a","workspace_uuid":"workspace-a","api_key_id":"key-a","permissions":[]}}`)
		case "/proxy/api/v1/system/capabilities":
			fmt.Fprintf(w, `{"code":0,"msg":%q,"data":{"schema_version":1,"operations":{"bot.list":{"supported":true},"pipeline.list":{"supported":false},"unknown-secret-operation":{"supported":true,"value":%q}}}}`, key, key)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"code":404,"msg":"missing"}`)
		}
	}))
	defer server.Close()

	requireSuccess(t, run(t, path, nil, "", "context", "add", "production", "--endpoint", server.URL+"/proxy", "--api-key-env", "CAPABILITY_KEY"))
	result := run(t, path, map[string]string{"CAPABILITY_KEY": key}, "", "--context", "production", "capabilities")
	requireSuccess(t, result)
	data := result.data["data"].(map[string]any)
	if data["instance_uuid"] != "instance-a" || data["workspace_uuid"] != "workspace-a" || data["schema_version"] != float64(1) {
		t.Fatalf("capability result lost identity or schema: %s", result.out)
	}
	operations := data["operations"].(map[string]any)
	if operations["bot.list"] != "supported" || operations["pipeline.list"] != "unsupported" || operations["bot.get"] != "unknown" {
		t.Fatalf("capability statuses = %#v", operations)
	}
	if strings.Contains(result.out, key) || strings.Contains(result.out, "unknown-secret-operation") {
		t.Fatalf("capability output exposed secret or unknown operation: %s", result.out)
	}
}

func TestCapabilitiesAuthenticationFailureDoesNotProbeCapabilityEndpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	var capabilityCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/system/capabilities" {
			capabilityCalls.Add(1)
		}
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"code":401,"msg":"unauthorized"}`)
	}))
	defer server.Close()

	requireSuccess(t, run(t, path, nil, "", "context", "add", "production", "--endpoint", server.URL, "--api-key-env", "CAPABILITY_KEY"))
	result := run(t, path, map[string]string{"CAPABILITY_KEY": "invalid-key"}, "", "--context", "production", "capabilities")
	if result.code != 3 || result.data["error"].(map[string]any)["type"] != "auth" || capabilityCalls.Load() != 0 {
		t.Fatalf("authentication failure was not stopped at identity: %s", result.out)
	}
}

func TestCapabilitiesPermissionFailureKeepsConfirmedIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	const key = "capability-key-value"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/system/context" {
			fmt.Fprint(w, `{"code":0,"data":{"instance_uuid":"instance-a","workspace_uuid":"workspace-a","api_key_id":"key-a","permissions":[]}}`)
			return
		}
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"code":403,"msg":"forbidden"}`)
	}))
	defer server.Close()

	requireSuccess(t, run(t, path, nil, "", "context", "add", "production", "--endpoint", server.URL, "--api-key-env", "CAPABILITY_KEY"))
	result := run(t, path, map[string]string{"CAPABILITY_KEY": key}, "", "--context", "production", "capabilities")
	if result.code != 4 || result.data["error"].(map[string]any)["type"] != "permission" {
		t.Fatalf("capability permission error was not preserved: %s", result.out)
	}
	data := result.data["data"].(map[string]any)
	if data["instance_uuid"] != "instance-a" || data["workspace_uuid"] != "workspace-a" || data["capabilities"] != "unknown" {
		t.Fatalf("capability permission error lost identity: %s", result.out)
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

func TestWhoamiAndContextCheckUseAuthenticatedIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	const key = "identity-key-value"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != key {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"code":401,"msg":"unauthorized"}`)
			return
		}
		switch r.URL.Path {
		case "/api/v1/system/info":
			fmt.Fprint(w, `{"code":0,"data":{"version":"4.10.10","edition":"community"}}`)
		case "/api/v1/system/context":
			fmt.Fprint(w, `{"code":0,"data":{"instance_uuid":"instance-a","workspace_uuid":"workspace-a","api_key_id":"key-a","permissions":["write","read","read"]}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"code":404,"msg":"missing"}`)
		}
	}))
	defer server.Close()

	requireSuccess(t, run(t, path, map[string]string{"IDENTITY_KEY": key}, "", "context", "add", "production", "--endpoint", server.URL, "--api-key-env", "IDENTITY_KEY", "--expect-workspace", "workspace-a"))
	whoami := run(t, path, map[string]string{"IDENTITY_KEY": key}, "", "--context", "production", "whoami")
	requireSuccess(t, whoami)
	if !strings.Contains(whoami.out, `"instance_uuid": "instance-a"`) || !strings.Contains(whoami.out, `"workspace_uuid": "workspace-a"`) || strings.Contains(whoami.out, key) {
		t.Fatalf("whoami identity or secret handling is wrong: %s", whoami.out)
	}
	checked := run(t, path, map[string]string{"IDENTITY_KEY": key}, "", "context", "check", "production")
	requireSuccess(t, checked)
	data := checked.data["data"].(map[string]any)
	if data["reachable"] != true || data["diagnostic_ok"] != true {
		t.Fatalf("successful check flags are wrong: %s", checked.out)
	}
	identity := data["identity"].(map[string]any)
	if identity["workspace_uuid"] != "workspace-a" || identity["api_key_id"] != "key-a" {
		t.Fatalf("check identity = %#v", identity)
	}
}

func TestContextCheckDistinguishesPublicDiagnosticFromAuthentication(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/system/info" {
			fmt.Fprint(w, `{"code":0,"data":{"version":"4.10.10","edition":"community"}}`)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"code":401,"msg":"unauthorized"}`)
	}))
	defer server.Close()
	requireSuccess(t, run(t, path, nil, "", "context", "add", "production", "--endpoint", server.URL, "--api-key-env", "MISSING_KEY"))
	checked := run(t, path, map[string]string{"MISSING_KEY": "invalid-key"}, "", "context", "check", "production")
	if checked.code != 3 {
		t.Fatalf("invalid identity did not return auth exit code: %s", checked.out)
	}
	data := checked.data["data"].(map[string]any)
	if data["reachable"] != true || data["diagnostic_ok"] != false || data["identity"] != "unknown" {
		t.Fatalf("public diagnosis was confused with identity: %s", checked.out)
	}
}

func TestContextCheckKeepsReachableWhenIdentityRequestHasNoHTTPResponse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/system/info" {
			fmt.Fprint(w, `{"code":0,"data":{"version":"4.10.10","edition":"community"}}`)
			return
		}
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("test server does not support connection hijacking")
		}
		connection, _, err := hijacker.Hijack()
		if err != nil {
			t.Fatalf("hijack: %v", err)
		}
		_ = connection.Close()
	}))
	defer server.Close()
	requireSuccess(t, run(t, path, nil, "", "context", "add", "production", "--endpoint", server.URL))
	checked := run(t, path, nil, "", "context", "check", "production")
	if checked.code != 7 {
		t.Fatalf("identity network failure did not return network exit code: %s", checked.out)
	}
	data := checked.data["data"].(map[string]any)
	if data["reachable"] != true || data["diagnostic_ok"] != false || data["identity"] != "unknown" {
		t.Fatalf("identity network failure lost public reachability: %s", checked.out)
	}
}

func TestExpectedWorkspaceMismatchIsPreconditionAndPreservesIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/system/info" {
			fmt.Fprint(w, `{"code":0,"data":{"version":"4.10.10","edition":"community"}}`)
			return
		}
		fmt.Fprint(w, `{"code":0,"data":{"instance_uuid":"instance-a","workspace_uuid":"actual-workspace","api_key_id":"key-a","permissions":[]}}`)
	}))
	defer server.Close()
	requireSuccess(t, run(t, path, nil, "", "context", "add", "production", "--endpoint", server.URL, "--expect-workspace", "expected-workspace"))
	whoami := run(t, path, nil, "", "--context", "production", "whoami")
	if whoami.code != 6 || whoami.data["error"].(map[string]any)["type"] != "target_mismatch" {
		t.Fatalf("whoami workspace mismatch was not a precondition: %s", whoami.out)
	}
	whoamiData := whoami.data["data"].(map[string]any)
	whoamiIdentity := whoamiData["instance_uuid"]
	if whoamiIdentity != "instance-a" || whoamiData["workspace_uuid"] != "actual-workspace" {
		t.Fatalf("whoami mismatch lost actual identity: %s", whoami.out)
	}
	checked := run(t, path, nil, "", "context", "check", "production")
	if checked.code != 6 || checked.data["error"].(map[string]any)["type"] != "target_mismatch" {
		t.Fatalf("workspace mismatch was not a precondition: %s", checked.out)
	}
	data := checked.data["data"].(map[string]any)
	identity := data["identity"].(map[string]any)
	if identity["workspace_uuid"] != "actual-workspace" || strings.Contains(checked.out, "expected-workspace") {
		t.Fatalf("mismatch lost safe actual identity or exposed unexpected data: %s", checked.out)
	}
}

func TestResourceCommandsConfirmIdentityAndPermissionBeforeReading(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	var resourceCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/system/context" {
			fmt.Fprint(w, `{"code":0,"data":{"instance_uuid":"instance-a","workspace_uuid":"workspace-a","api_key_id":"key-a","permissions":[]}}`)
			return
		}
		resourceCalls.Add(1)
		fmt.Fprint(w, `{"code":0,"data":{"bots":[]}}`)
	}))
	defer server.Close()
	result := run(t, path, nil, "no-view-key\n", "--endpoint", server.URL, "--api-key-stdin", "bot", "list")
	if result.code != 4 || result.data["error"].(map[string]any)["type"] != "permission" {
		t.Fatalf("missing resource.view was not rejected: %s", result.out)
	}
	meta := result.data["meta"].(map[string]any)
	if meta["instance_uuid"] != "instance-a" || meta["workspace_uuid"] != "workspace-a" {
		t.Fatalf("permission error lost confirmed identity metadata: %s", result.out)
	}
	if resourceCalls.Load() != 0 {
		t.Fatalf("resource request was sent without resource.view: %d", resourceCalls.Load())
	}
}

func TestResourceCommandsUseAuthenticatedContextAndSafeProjection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	const key = "resource-key-value"
	var resourceCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-Key") != key {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"code":401,"msg":"unauthorized"}`)
			return
		}
		switch r.URL.Path {
		case "/api/v1/system/context":
			fmt.Fprint(w, `{"code":0,"data":{"instance_uuid":"instance-a","workspace_uuid":"workspace-a","api_key_id":"key-a","permissions":["resource.view"]}}`)
		case "/api/v1/platform/bots":
			resourceCalls.Add(1)
			fmt.Fprint(w, `{"code":0,"data":{"bots":[{"uuid":"bot-a","name":"Bot A","adapter":"http_bot","adapter_config":{"token":"bot-secret"}}]}}`)
		case "/api/v1/platform/bots/bot-a":
			resourceCalls.Add(1)
			fmt.Fprint(w, `{"code":0,"data":{"bot":{"uuid":"bot-a","name":"Bot A","adapter":"http_bot","adapter_config":{"token":"bot-secret"}}}}`)
		case "/api/v1/pipelines":
			resourceCalls.Add(1)
			fmt.Fprint(w, `{"code":0,"data":{"pipelines":[{"uuid":"pipeline-a","name":"Pipeline A","config":{"api_key":"provider-secret"}}]}}`)
		case "/api/v1/pipelines/pipeline-a":
			resourceCalls.Add(1)
			fmt.Fprint(w, `{"code":0,"data":{"pipeline":{"uuid":"pipeline-a","name":"Pipeline A","config":{"token":"pipeline-secret"}}}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	for _, item := range []struct {
		args []string
		want string
	}{
		{args: []string{"bot", "list"}, want: "bot-a"},
		{args: []string{"bot", "get", "bot-a"}, want: "bot-a"},
		{args: []string{"pipeline", "list"}, want: "pipeline-a"},
		{args: []string{"pipeline", "get", "pipeline-a"}, want: "pipeline-a"},
	} {
		args := append([]string{"--endpoint", server.URL, "--api-key-stdin"}, item.args...)
		result := run(t, path, nil, key+"\n", args...)
		requireSuccess(t, result)
		meta := result.data["meta"].(map[string]any)
		if meta["instance_uuid"] != "instance-a" || meta["workspace_uuid"] != "workspace-a" {
			t.Fatalf("resource success lost confirmed identity metadata: %s", result.out)
		}
		if !strings.Contains(result.out, item.want) || strings.Contains(result.out, "bot-secret") || strings.Contains(result.out, "provider-secret") || strings.Contains(result.out, "pipeline-secret") {
			t.Fatalf("resource output was missing safe data or exposed config: %s", result.out)
		}
	}
	if resourceCalls.Load() != 4 {
		t.Fatalf("resource request count = %d, want 4", resourceCalls.Load())
	}
}

func TestResourceCommandsRejectWorkspaceMismatchBeforeReading(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	const key = "resource-key-value"
	var resourceCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/system/context" {
			fmt.Fprint(w, `{"code":0,"data":{"instance_uuid":"instance-a","workspace_uuid":"actual-workspace","api_key_id":"key-a","permissions":["resource.view"]}}`)
			return
		}
		resourceCalls.Add(1)
	}))
	defer server.Close()
	requireSuccess(t, run(t, path, nil, "", "context", "add", "production", "--endpoint", server.URL, "--api-key-env", "RESOURCE_KEY", "--expect-workspace", "expected-workspace"))
	result := run(t, path, map[string]string{"RESOURCE_KEY": key}, "", "--context", "production", "bot", "list")
	if result.code != 6 || result.data["error"].(map[string]any)["type"] != "target_mismatch" {
		t.Fatalf("workspace mismatch was not rejected: %s", result.out)
	}
	meta := result.data["meta"].(map[string]any)
	if meta["instance_uuid"] != "instance-a" || meta["workspace_uuid"] != "actual-workspace" {
		t.Fatalf("workspace mismatch lost confirmed identity metadata: %s", result.out)
	}
	if resourceCalls.Load() != 0 {
		t.Fatalf("resource request was sent after workspace mismatch")
	}
}

package integration_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type panicReader struct{}

func (panicReader) Read([]byte) (int, error) {
	panic("stdin was read by an offline context command")
}

func TestContextShowSeparatesSavedAndEffectiveWithoutReadingSecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	endpoint := "http://127.0.0.1:5300/base"
	requireSuccess(t, run(t, path, nil, "", "context", "add", "demo", "--endpoint", endpoint, "--api-key-env", "CUSTOM_KEY"))

	var customKeyLookups atomic.Int32
	lookup := func(name string) (string, bool) {
		if name == "CUSTOM_KEY" {
			customKeyLookups.Add(1)
			return "secret-value", true
		}
		return "", false
	}
	show := runLookup(t, path, nil, lookup, panicReader{}, "--endpoint", endpoint+"/override", "--api-key-stdin", "context", "show", "demo")
	requireSuccess(t, show)
	if customKeyLookups.Load() != 0 {
		t.Fatal("context show read the saved custom credential")
	}
	if !strings.Contains(show.out, "env:CUSTOM_KEY") || !strings.Contains(show.out, "stdin") || !strings.Contains(show.out, `"temporary": true`) {
		t.Fatalf("saved/effective views lost source or override metadata: %s", show.out)
	}
}

func TestContextUpdateClearsOnlyChangedTargetBinding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	first := "http://127.0.0.1:5300/base"
	second := "http://127.0.0.1:5301/base"
	requireSuccess(t, run(t, path, nil, "", "context", "add", "changed", "--endpoint", first, "--api-key-env", "FIRST_KEY", "--expect-workspace", "workspace-a"))
	requireSuccess(t, run(t, path, nil, "", "context", "update", "changed", "--endpoint", second))
	changed := run(t, path, nil, "", "context", "show", "changed")
	changedSaved := changed.data["data"].(map[string]any)["saved"].(map[string]any)
	if changedSaved["credential_source"] != "none" || changedSaved["expected_workspace_uuid"] != "" {
		t.Fatalf("changed endpoint retained binding: %s", changed.out)
	}

	requireSuccess(t, run(t, path, nil, "", "context", "add", "same", "--endpoint", first, "--api-key-env", "FIRST_KEY", "--expect-workspace", "workspace-a"))
	requireSuccess(t, run(t, path, nil, "", "context", "update", "same", "--endpoint", first))
	same := run(t, path, nil, "", "context", "show", "same")
	sameSaved := same.data["data"].(map[string]any)["saved"].(map[string]any)
	if sameSaved["credential_source"] != "env:FIRST_KEY" || sameSaved["expected_workspace_uuid"] != "workspace-a" {
		t.Fatalf("equivalent endpoint unexpectedly cleared binding: %s", same.out)
	}
}

func TestContextRemoveCurrentRequiresConfirmationAndClearsDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	requireSuccess(t, run(t, path, nil, "", "context", "add", "demo", "--endpoint", "http://127.0.0.1:5300"))
	requireSuccess(t, run(t, path, nil, "", "context", "use", "demo"))
	blocked := run(t, path, nil, "", "context", "remove", "demo")
	if blocked.code != 2 || blocked.data["ok"] != false {
		t.Fatalf("removing current context without --yes was accepted: %s", blocked.out)
	}
	requireSuccess(t, run(t, path, nil, "", "context", "remove", "demo", "--yes"))
	list := run(t, path, nil, "", "context", "list")
	data := list.data["data"].(map[string]any)
	if data["current_context"] != "" || len(data["contexts"].([]any)) != 0 {
		t.Fatalf("current context was not cleared after confirmed removal: %s", list.out)
	}
}

func TestCheckAllUsesBoundedConcurrentIndependentTimeouts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	var active, maxActive, started, calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		started.Add(1)
		current := active.Add(1)
		for {
			previous := maxActive.Load()
			if current <= previous || maxActive.CompareAndSwap(previous, current) {
				break
			}
		}
		<-r.Context().Done()
		active.Add(-1)
	}))
	defer server.Close()
	for i := 0; i < 6; i++ {
		requireSuccess(t, run(t, path, nil, "", "context", "add", fmt.Sprintf("ctx-%d", i), "--endpoint", server.URL))
	}

	begin := time.Now()
	checked := run(t, path, nil, "", "context", "check", "--all", "--timeout", "80ms")
	elapsed := time.Since(begin)
	if checked.code == 0 {
		t.Fatalf("timed out batch unexpectedly succeeded: %s", checked.out)
	}
	checks := checked.data["data"].(map[string]any)["checks"].([]any)
	if len(checks) != 6 || calls.Load() != 6 || started.Load() != 6 {
		t.Fatalf("batch did not retain every independent outcome: calls=%d started=%d result=%s", calls.Load(), started.Load(), checked.out)
	}
	if maxActive.Load() > 4 {
		t.Fatalf("check-all exceeded worker bound: max active=%d", maxActive.Load())
	}
	if elapsed > 2*time.Second {
		t.Fatalf("independent timeouts took too long: %s", elapsed)
	}
}

func TestOutputFormatsAndBusinessErrorExit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	requireSuccess(t, run(t, path, nil, "", "context", "add", "demo", "--endpoint", "http://127.0.0.1:5300"))
	for _, format := range []string{"json", "yaml"} {
		code, out := runText(t, path, nil, strings.NewReader(""), "--output", format, "context", "list")
		if code != 0 || !strings.Contains(out, "demo") {
			t.Fatalf("format %s did not render a successful result: code=%d output=%q", format, code, out)
		}
		if format == "yaml" && !strings.Contains(out, "ok: true") {
			t.Fatalf("yaml output is not an envelope: %q", out)
		}
	}
	code, out := runText(t, path, nil, strings.NewReader(""), "context", "list")
	if code != 0 || !strings.Contains(out, "Contexts:") || !strings.Contains(out, "demo") {
		t.Fatalf("default output is not concise context list: code=%d output=%q", code, out)
	}
	if code, out := runText(t, path, nil, strings.NewReader(""), "--output", "table", "context", "list"); code != 2 || !strings.Contains(out, "输出格式无效") {
		t.Fatalf("table output was not rejected: code=%d output=%q", code, out)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"code":403,"msg":"denied"}`)
	}))
	defer server.Close()
	requireSuccess(t, run(t, path, nil, "", "context", "update", "demo", "--endpoint", server.URL))
	requireSuccess(t, run(t, path, nil, "", "context", "use", "demo"))
	failed := run(t, path, nil, "", "status")
	if failed.code != 10 || failed.data["ok"] != false {
		t.Fatalf("business error did not produce server exit code: %s", failed.out)
	}
	errData := failed.data["error"].(map[string]any)
	if errData["type"] != "server" || errData["server_code"] == nil {
		t.Fatalf("business error was not preserved: %s", failed.out)
	}
}

func TestOfflineVersionAndHelpDoNotReadConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist", "config.yaml")
	code, out := runText(t, path, nil, strings.NewReader(""), "version")
	if code != 0 || strings.TrimSpace(out) != "test" {
		t.Fatalf("offline version unexpectedly needed config: code=%d output=%q", code, out)
	}
	code, out = runText(t, path, nil, strings.NewReader(""), "help")
	if code != 0 || !strings.Contains(out, "LangBot 服务管理 CLI") {
		t.Fatalf("help unexpectedly needed config: code=%d output=%q", code, out)
	}
}

func TestHTTPAuthFailuresRemainReachableDiagnostics(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(fmt.Sprintf("status-%d", status), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				fmt.Fprintf(w, `{"code":%d,"msg":"denied"}`, status)
			}))
			defer server.Close()
			requireSuccess(t, run(t, path, nil, "", "context", "add", "demo", "--endpoint", server.URL))
			requireSuccess(t, run(t, path, nil, "", "context", "use", "demo"))
			result := run(t, path, nil, "", "status")
			if result.code == 0 {
				t.Fatalf("HTTP %d unexpectedly succeeded: %s", status, result.out)
			}
			data := result.data["data"].(map[string]any)
			connection := data["connection"].(map[string]any)
			if connection["reachable"] != true || data["discovery"] != "failed" {
				t.Fatalf("HTTP %d was classified as unreachable instead of an auth diagnostic: %s", status, result.out)
			}
		})
	}
}

func TestConnectionFailureIsUnreachableDiagnostic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	result := run(t, path, nil, "", "--endpoint", "http://127.0.0.1:1", "--timeout", "100ms", "status")
	if result.code != 7 {
		t.Fatalf("connection failure exit code = %d, want 7: %s", result.code, result.out)
	}
	data := result.data["data"].(map[string]any)
	connection := data["connection"].(map[string]any)
	if connection["reachable"] != false || data["discovery"] != "failed" {
		t.Fatalf("connection failure was classified as reachable: %s", result.out)
	}
}

func TestContextShowInvalidOverridePreservesSavedView(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	requireSuccess(t, run(t, path, nil, "", "context", "add", "demo", "--endpoint", "http://127.0.0.1:5300", "--api-key-env", "DEMO_KEY"))
	result := run(t, path, nil, "", "--endpoint", "ftp://invalid.example", "context", "show", "demo")
	if result.code != 2 || result.data["ok"] != false {
		t.Fatalf("invalid endpoint was not rejected with input exit code: %s", result.out)
	}
	data, ok := result.data["data"].(map[string]any)
	if !ok {
		t.Fatalf("invalid endpoint response lost data payload: %s", result.out)
	}
	saved, ok := data["saved"].(map[string]any)
	if !ok || saved["credential_source"] != "env:DEMO_KEY" || saved["endpoint"] != "http://127.0.0.1:5300" {
		t.Fatalf("saved configuration was not preserved: %s", result.out)
	}
}

func TestCheckAllExplicitTimeoutOverridesInvalidEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/system/context" {
			fmt.Fprint(w, `{"code":0,"data":{"instance_uuid":"instance-a","workspace_uuid":"workspace-a","api_key_id":"key-a","permissions":[]}}`)
			return
		}
		fmt.Fprint(w, `{"code":0,"data":{"version":"4.10.10","edition":"community"}}`)
	}))
	defer server.Close()
	for _, name := range []string{"one", "two"} {
		requireSuccess(t, run(t, path, nil, "", "context", "add", name, "--endpoint", server.URL))
	}
	result := run(t, path, map[string]string{"LANGBOT_TIMEOUT": "not-a-duration"}, "", "context", "check", "--all", "--timeout", "500ms")
	if result.code != 0 || result.data["ok"] != true {
		t.Fatalf("explicit check-all timeout did not override invalid environment: %s", result.out)
	}
}

func TestCanceledStdinCredentialExitsWithoutHTTP(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, `{"code":0,"data":{"version":"4.10.10","edition":"community"}}`)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	reader, writer := io.Pipe()
	defer writer.Close()
	done := make(chan struct {
		code int
		out  string
	}, 1)
	go func() {
		code, out, _, _ := invokeRawLookupContext(ctx, path, nil, func(string) (string, bool) {
			return "", false
		}, reader, "--api-key-stdin", "--endpoint", server.URL, "status")
		done <- struct {
			code int
			out  string
		}{code: code, out: out}
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case result := <-done:
		if result.code != 7 || strings.Contains(result.out, "secret") || calls.Load() != 0 {
			t.Fatalf("canceled stdin request was not a clean network cancellation: code=%d calls=%d output=%s", result.code, calls.Load(), result.out)
		}
	case <-time.After(2 * time.Second):
		_ = writer.CloseWithError(context.Canceled)
		t.Fatal("canceling stdin credential did not return promptly")
	}
}

var _ io.Reader = panicReader{}

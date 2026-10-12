package integration_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPipelineRunOutcomesAndNoRetry(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	permissions := []string{"resource.view", "runtime.operate"}
	status := "completed"
	supported := true
	var writes atomic.Int32
	submitted := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/system/context":
			p, _ := json.Marshal(permissions)
			fmt.Fprintf(w, `{"code":0,"data":{"instance_uuid":"instance-a","workspace_uuid":"workspace-a","api_key_id":"key-a","permissions":%s}}`, p)
		case "/api/v1/system/capabilities":
			if supported {
				fmt.Fprint(w, capabilitiesJSON("pipeline.run", "pipeline.get"))
			} else {
				fmt.Fprint(w, capabilitiesJSON("pipeline.get"))
			}
		case "/api/v1/pipelines/p/run":
			if r.Method != http.MethodPost {
				t.Error("run must POST")
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["message"] != "hello" {
				t.Error("message not sent")
			}
			writes.Add(1)
			current := status
			if current == "timeout" {
				submitted <- struct{}{}
				time.Sleep(40 * time.Millisecond)
			}
			fmt.Fprintf(w, `{"code":0,"data":{"status":%q,"reply":"answer","session_id":"s"}}`, current)
		default:
			writeCLIError(w, http.StatusNotFound)
		}
	}))
	defer server.Close()
	configureWriteContext(t, configPath, server.URL, "RUN_KEY")
	env := map[string]string{"RUN_KEY": "connection-secret"}
	requireSuccess(t, run(t, configPath, env, "", "pipeline", "run", "p", "--message", "hello", "--dry-run"))
	if writes.Load() != 0 {
		t.Fatal("dry-run executed")
	}
	requireSuccess(t, run(t, configPath, env, "", "pipeline", "run", "p", "--message", "hello"))
	status = "failed"
	if got := run(t, configPath, env, "", "pipeline", "run", "p", "--message", "hello"); got.code != 10 {
		t.Fatalf("failed run: %s", got.out)
	}
	status = "unknown"
	if got := run(t, configPath, env, "", "pipeline", "run", "p", "--message", "hello"); got.code != 7 || !strings.Contains(got.out, "unknown") {
		t.Fatalf("unknown run: %s", got.out)
	}
	status = "timeout"
	before := writes.Load()
	got := run(t, configPath, env, "", "pipeline", "run", "p", "--message", "hello", "--timeout", "10ms")
	select {
	case <-submitted:
	case <-time.After(time.Second):
		t.Fatal("run endpoint not reached")
	}
	if got.code != 7 || !strings.Contains(got.out, "unknown") || writes.Load() != before+1 {
		t.Fatalf("timeout/retry: %s", got.out)
	}
	permissions = []string{"resource.view"}
	before = writes.Load()
	if got := run(t, configPath, env, "", "pipeline", "run", "p", "--message", "hello"); got.code != 4 || writes.Load() != before {
		t.Fatalf("permission: %s", got.out)
	}
	permissions = []string{"resource.view", "runtime.operate"}
	supported = false
	if got := run(t, configPath, env, "", "pipeline", "run", "p", "--message", "hello"); got.code != 6 || writes.Load() != before {
		t.Fatalf("capability: %s", got.out)
	}
	if got := run(t, configPath, env, "", "pipeline", "run", "p"); got.code != 2 {
		t.Fatalf("missing message: %s", got.out)
	}
}

package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func TestSanitizeError(t *testing.T) {
	for _, secret := range []string{`C:\Users\alice\AppData\Local\uv`, `/home/alice/.langbot`, `/Users/alice/Library`,
		`token=secret-value`, `API_KEY="private key"`, `Authorization: Bearer abc.def`, `username=alice`, `sk-123456789abc`,
		`ghp_abcdef123`, `eyJhbGciOiJI.eyJhIjoiYiJ9.signature`, `192.168.1.2`, `2001:db8::1`, `https://bob:pass@example.org/path?key=secret`} {
		if clean := SanitizeError("failed: " + secret); strings.Contains(clean, secret) {
			t.Errorf("not redacted: %q", secret)
		}
	}
	clean := SanitizeError(strings.Repeat("失败", 1000))
	if len(clean) > 2048 || !utf8.ValidString(clean) {
		t.Fatalf("invalid truncation: %d", len(clean))
	}
	if clean := SanitizeError("download failed: exit status 1"); clean != "download failed: exit status 1" {
		t.Fatal(clean)
	}
}

func lookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) { value, ok := values[key]; return value, ok }
}

func TestOptOut(t *testing.T) {
	for _, values := range []map[string]string{{"LANGBOT_TELEMETRY": "0"}, {"DO_NOT_TRACK": "1"}} {
		home := t.TempDir()
		client := New(Options{Home: home, LookupEnv: lookup(values)})
		if client != nil {
			t.Fatal("opt-out created client")
		}
		_, op := client.Begin(t.Context(), "install")
		op.Finish(errors.New("failure"), "server")
		client.Close()
		if _, err := os.Stat(filepath.Join(home, ".langbot")); !os.IsNotExist(err) {
			t.Fatal("opt-out wrote identity")
		}
	}
}

func TestLifecycleAndPersistentIdentity(t *testing.T) {
	var mutex sync.Mutex
	var events []Event
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var event Event
		if err := json.NewDecoder(request.Body).Decode(&event); err != nil {
			t.Error(err)
		}
		mutex.Lock()
		events = append(events, event)
		mutex.Unlock()
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	home := t.TempDir()
	firstRuns := 0
	for index := 0; index < 2; index++ {
		client := New(Options{Home: home, Version: "1.2.3", LookupEnv: lookup(map[string]string{
			"LBCTL_TELEMETRY_ENDPOINT": server.URL, "LBCTL_INSTALL_SOURCE": "install.ps1", "LBCTL_LANG": "zh-Hans", "USER": "alice",
		}), OnFirstRun: func() { firstRuns++ }})
		ctx, op := client.Begin(t.Context(), "install")
		SetStep(ctx, "download_uv")
		SetEnvironment(ctx, "native", "systemd-user", "cn", "v4.10.0")
		if index == 0 {
			op.Finish(errors.New("alice failed at /home/alice token=private"), "server")
		} else {
			op.Finish(nil, "")
		}
		client.Close()
	}
	mutex.Lock()
	defer mutex.Unlock()
	if firstRuns != 1 || len(events) != 4 {
		t.Fatalf("hook=%d events=%d", firstRuns, len(events))
	}
	ids := map[string]bool{}
	var failed, succeeded, started int
	for _, event := range events {
		if !uuidPattern.MatchString(event.InstanceID) || event.InstanceID != events[0].InstanceID || ids[event.EventID] {
			t.Fatalf("invalid identity: %+v", event)
		}
		ids[event.EventID] = true
		if event.Source != "install.ps1" || event.Locale != "zh-Hans" {
			t.Fatalf("metadata: %+v", event)
		}
		switch event.Outcome {
		case "started":
			started++
		case "failed":
			failed++
			if event.Step != "download_uv" || event.Driver != "native" || strings.Contains(event.Error, "alice") || strings.Contains(event.Error, "private") {
				t.Fatalf("failure: %+v", event)
			}
		case "succeeded":
			succeeded++
		}
	}
	if started != 2 || failed != 1 || succeeded != 1 {
		t.Fatalf("outcomes: %d/%d/%d", started, failed, succeeded)
	}
}

type blockingTransport struct{ done chan time.Duration }

func (transport blockingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	started := time.Now()
	<-request.Context().Done()
	transport.done <- time.Since(started)
	return nil, request.Context().Err()
}

func TestTimeoutAndBoundedFlush(t *testing.T) {
	for _, flush := range []bool{false, true} {
		t.Run(map[bool]string{false: "request_timeout", true: "exit_flush"}[flush], func(t *testing.T) {
			done := make(chan time.Duration, 1)
			client := New(Options{Home: t.TempDir(), LookupEnv: lookup(nil), Transport: blockingTransport{done}})
			start := time.Now()
			client.Begin(context.Background(), "start")
			if time.Since(start) > 200*time.Millisecond {
				t.Fatal("Begin blocked")
			}
			if flush {
				client.Close()
				if elapsed := time.Since(start); elapsed > 1200*time.Millisecond {
					t.Fatalf("flush blocked: %s", elapsed)
				}
			}
			select {
			case elapsed := <-done:
				if elapsed > 2200*time.Millisecond {
					t.Fatalf("request timeout: %s", elapsed)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("request did not time out")
			}
			if !flush {
				client.Close()
			}
		})
	}
}

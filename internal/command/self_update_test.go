package command

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/langbot-app/langbot-cli/internal/selfupdate"
	"github.com/spf13/cobra"
)

type updateTransport func(*http.Request) (*http.Response, error)

func (f updateTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func testUpdater(t *testing.T, calls *atomic.Int32) *selfupdate.Updater {
	t.Helper()
	asset := "lbctl_" + runtime.GOOS + "_" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		asset += ".exe"
	}
	return &selfupdate.Updater{CacheDir: t.TempDir(), HTTP: &http.Client{Transport: updateTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.URL.String() != "https://api.github.com/repos/langbot-app/langbot-cli/releases/latest" || r.Header.Get("Authorization") != "" || r.Header.Get("X-API-Key") != "" {
			t.Errorf("unexpected updater request %s", r.URL)
		}
		body := fmt.Sprintf(`{"tag_name":"v0.4.2","assets":[{"name":%q,"state":"uploaded","size":100},{"name":"checksums.txt","state":"uploaded","size":100}]}`, asset)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}}
}

func TestSelfUpdateCheckUsesHumanAndMachineOutput(t *testing.T) {
	for _, format := range []string{"", "json", "yaml"} {
		var out, diagnostic bytes.Buffer
		var calls atomic.Int32
		args := []string{"self", "update", "--check"}
		if format != "" {
			args = append(args, "-o", format)
		}
		code := Execute(context.Background(), args, Dependencies{
			Out: &out, Err: &diagnostic, Version: "v0.4.1", SelfUpdate: testUpdater(t, &calls),
			DefaultConfigPath: func() (string, error) { t.Fatal("self update read Workspace configuration"); return "", nil },
		})
		if code != 0 || diagnostic.Len() != 0 || calls.Load() != 1 {
			t.Fatalf("check failed: code=%d calls=%d output=%s diagnostic=%s", code, calls.Load(), &out, &diagnostic)
		}
		if format == "json" {
			var envelope struct {
				OK   bool            `json:"ok"`
				Data selfupdate.Info `json:"data"`
			}
			if json.Unmarshal(out.Bytes(), &envelope) != nil || !envelope.OK || !envelope.Data.UpdateAvailable || envelope.Data.Updated {
				t.Fatalf("invalid update envelope: %s", &out)
			}
		} else if !strings.Contains(out.String(), "lbctl self update") || !strings.Contains(out.String(), "v0.4.2") {
			t.Fatalf("update instruction missing: %s", &out)
		}
	}
}

func TestSelfUpdateRejectsInstanceFlagsBeforeNetworking(t *testing.T) {
	for _, flag := range [][]string{
		{"--context", "test"}, {"--endpoint", "http://localhost:5300"}, {"--api-key-stdin"}, {"--timeout", "0s"},
	} {
		var out bytes.Buffer
		var calls atomic.Int32
		args := append([]string{"self", "update", "--check"}, flag...)
		code := Execute(context.Background(), args, Dependencies{Out: &out, Err: io.Discard, Version: "v0.4.1", SelfUpdate: testUpdater(t, &calls)})
		if code != 2 || calls.Load() != 0 {
			t.Fatalf("invalid flags reached updater: args=%v code=%d calls=%d", args, code, calls.Load())
		}
	}
}

func TestSelfUpdateSchemaAndHelpStayOffline(t *testing.T) {
	for _, args := range [][]string{{"self", "--help"}, {"self", "update", "--help"}, {"schema", "--command", "self.update"}} {
		var out bytes.Buffer
		var calls atomic.Int32
		code := Execute(context.Background(), args, Dependencies{Out: &out, Err: io.Discard, Version: "v0.4.1", SelfUpdate: testUpdater(t, &calls)})
		if code != 0 || calls.Load() != 0 || !strings.Contains(out.String(), "update") {
			t.Fatalf("offline help/schema failed: args=%v code=%d output=%s", args, code, &out)
		}
		if args[0] == "schema" && (!strings.Contains(out.String(), `"requires_connection": false`) || !strings.Contains(out.String(), `"mutating": true`)) {
			t.Fatalf("self-update schema requires a Core connection: %s", &out)
		}
	}
}

func TestAutomaticNoticeOnlyChecksForHumanTerminals(t *testing.T) {
	terminal, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()
	if !terminalWriter(terminal) {
		t.Skip("null device is not exposed as a character device")
	}
	for _, name := range []string{"human", "json", "pipe", "ci", "disabled", "schema", "version", "context"} {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			deps := Dependencies{Out: terminal, Err: terminal, Version: "v0.4.1", SelfUpdate: testUpdater(t, &calls), LookupEnv: func(key string) (string, bool) {
				if (name == "ci" && key == "CI") || (name == "disabled" && key == "LBCTL_NO_UPDATE_CHECK") {
					return "1", true
				}
				return "", false
			}}
			flags := &globalFlags{}
			root := &cobra.Command{Use: "lbctl"}
			cmd := &cobra.Command{Use: "status"}
			if name == "schema" || name == "version" || name == "context" {
				cmd.Use = name
			}
			root.AddCommand(cmd)
			if name == "json" {
				flags.outputSet = true
			}
			if name == "pipe" {
				deps.Out = io.Discard
			}
			finish := startUpdateNotice(context.Background(), cmd, deps, flags)
			finish()
			want := int32(0)
			if name == "human" {
				want = 1
			}
			if calls.Load() != want {
				t.Fatalf("automatic checks=%d want=%d", calls.Load(), want)
			}
		})
	}
}

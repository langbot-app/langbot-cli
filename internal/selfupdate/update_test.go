package selfupdate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func assetName() string {
	name := "lbctl_" + runtime.GOOS + "_" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

func releaseJSON(tag string) string {
	return fmt.Sprintf(`{"tag_name":%q,"assets":[{"name":%q,"state":"uploaded","size":100},{"name":"checksums.txt","state":"uploaded","size":100}]}`, tag, assetName())
}

func response(body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestVersionOrderingAndReleaseReadiness(t *testing.T) {
	for _, test := range []struct {
		current, latest string
		want            bool
	}{
		{"v0.4.1", "v0.4.2", true}, {"v0.4.10", "v0.4.2", false},
		{"v0.5.0", "v0.4.2", false}, {"v0.4.2", "v0.4.2", false},
		{"v0.4.2-rc.1", "v0.4.2", true}, {"v0.5.0-rc.1", "v0.4.2", false},
		{"v0.4.2+build.1", "v0.4.2", false}, {"dev", "v0.4.2", true},
		{"v0.4.1", "v0.4.2+build.1", true}, {"v0.4.2", "v0.4.2+build.1", false},
	} {
		got, err := newer(test.current, test.latest)
		if err != nil || got != test.want {
			t.Fatalf("newer(%q,%q) = %v,%v", test.current, test.latest, got, err)
		}
	}
	for _, body := range []string{
		`{}`, `{"tag_name":"v0.4.2-rc.1"}`, `{"tag_name":"../../other"}`,
		`{"tag_name":"v0.4.2","draft":true}`, `{"tag_name":"v0.4.2","prerelease":true}`,
		`{"tag_name":"v0.4.2","assets":[]}`, `invalid`,
	} {
		u := Updater{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.String() != latestReleaseURL || r.Header.Get("Authorization") != "" || r.Header.Get("X-API-Key") != "" {
				t.Error("version lookup escaped the public release endpoint")
			}
			return response(body), nil
		})}}
		if _, err := u.Check(context.Background(), "v0.4.1"); err == nil {
			t.Fatalf("invalid or incomplete release accepted: %s", body)
		}
	}
}

func TestUpdateRejectsRedirectsOutsideOfficialReleaseHosts(t *testing.T) {
	var calls int
	u := Updater{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		res := response("")
		res.StatusCode = http.StatusFound
		res.Header.Set("Location", "https://untrusted.example/binary")
		return res, nil
	})}}
	if _, err := u.Check(context.Background(), "v0.4.1"); err == nil || calls != 1 {
		t.Fatalf("redirect was followed: calls=%d error=%v", calls, err)
	}
}

// Build a real executable so Windows image locks and executable validation are exercised.
func buildVersionBinary(t *testing.T, dir, version string) []byte {
	t.Helper()
	source := filepath.Join(dir, "main.go")
	program := `package main
import ("encoding/json"; "fmt"; "os"; "time")
var version string
func main() {
 if len(os.Args) > 1 && os.Args[1] == "hold" {
  fmt.Println("ready")
  for { time.Sleep(time.Second) }
 }
 json.NewEncoder(os.Stdout).Encode(map[string]any{"ok":true,"data":map[string]string{"version":version}})
}`
	if err := os.WriteFile(source, []byte(program), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, version+".exe")
	cmd := exec.Command("go", "build", "-ldflags=-X main.version="+version, "-o", path, source)
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build helper: %v\n%s", err, data)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestUpdateVerifiesDownloadsAndReplacesRunningExecutable(t *testing.T) {
	buildDir := t.TempDir()
	oldBinary := buildVersionBinary(t, buildDir, "v0.4.1")
	newBinary := buildVersionBinary(t, buildDir, "v0.4.2")
	for _, name := range []string{"running", "checksum", "wrong-version", "invalid-binary", "interrupted-download", "concurrent", "disk-already-updated"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "lbctl.exe")
			installed := oldBinary
			if name == "disk-already-updated" {
				installed = newBinary
			}
			if err := os.WriteFile(path, installed, 0o755); err != nil {
				t.Fatal(err)
			}
			u := Updater{Executable: func() (string, error) { return path, nil }}
			if name == "running" {
				cmd := exec.Command(path, "hold")
				stdout, err := cmd.StdoutPipe()
				if err != nil || cmd.Start() != nil {
					t.Fatal("could not start old executable")
				}
				t.Cleanup(func() {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
					u.Cleanup()
					if _, err := os.Lstat(backupPath(path)); !errors.Is(err, os.ErrNotExist) {
						t.Errorf("old executable backup was not cleaned: %v", err)
					}
				})
				ready := make([]byte, 5)
				if _, err := io.ReadFull(stdout, ready); err != nil || string(ready) != "ready" {
					t.Fatal("old executable did not start")
				}
			}
			payload := newBinary
			if name == "wrong-version" {
				payload = oldBinary
			}
			if name == "invalid-binary" {
				payload = []byte("not executable")
			}
			digest := fmt.Sprintf("%x", sha256.Sum256(payload))
			if name == "checksum" {
				digest = strings.Repeat("0", 64)
			}
			var downloads atomic.Int32
			u.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Header.Get("Authorization") != "" || r.Header.Get("X-API-Key") != "" {
					t.Error("credential sent to updater")
				}
				switch r.URL.String() {
				case latestReleaseURL:
					return response(releaseJSON("v0.4.2")), nil
				case "https://github.com/" + repository + "/releases/download/v0.4.2/checksums.txt":
					return response(digest + "  " + assetName() + "\n"), nil
				case "https://github.com/" + repository + "/releases/download/v0.4.2/" + assetName():
					downloads.Add(1)
					res := response("")
					res.Body = io.NopCloser(bytes.NewReader(payload))
					if name == "interrupted-download" {
						res.Body = io.NopCloser(io.MultiReader(bytes.NewReader(payload[:100]), iotest.ErrReader(io.ErrUnexpectedEOF)))
					}
					return res, nil
				default:
					t.Errorf("unexpected request %s", r.URL)
					return nil, errors.New("unexpected URL")
				}
			})}
			wantFailure := name == "checksum" || name == "wrong-version" || name == "invalid-binary" || name == "interrupted-download"
			if name == "concurrent" {
				var wg sync.WaitGroup
				var updates atomic.Int32
				for range 2 {
					wg.Go(func() {
						info, err := u.Update(ctx, "v0.4.1")
						if err != nil {
							t.Error(err)
						}
						if info.Updated {
							updates.Add(1)
						}
					})
				}
				wg.Wait()
				if updates.Load() != 1 || downloads.Load() != 1 {
					t.Fatalf("concurrent update repeated: updated=%d downloads=%d", updates.Load(), downloads.Load())
				}
			} else {
				info, err := u.Update(ctx, "v0.4.1")
				if (err != nil) != wantFailure || info.Updated != (!wantFailure && name != "disk-already-updated") {
					t.Fatalf("update result: %+v error=%v", info, err)
				}
			}
			want := "v0.4.2"
			if wantFailure {
				want = "v0.4.1"
			}
			if version, err := binaryVersion(ctx, path); err != nil || version != want {
				t.Fatalf("installed version=%s error=%v want=%s", version, err, want)
			}
			files, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".lbctl-update-*"))
			if len(files) != 0 {
				t.Fatalf("staged files leaked: %v", files)
			}
			if name == "disk-already-updated" && downloads.Load() != 0 {
				t.Fatal("downloaded an already installed version")
			}
		})
	}
}

func TestReplacementFailureRetainsOriginal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lbctl.exe")
	if err := os.WriteFile(path, []byte("original"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := replaceExecutable(path, path+".missing"); err == nil {
		t.Fatal("missing staged file accepted")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "original" {
		t.Fatalf("original not restored: %q %v", data, err)
	}
}

func TestReminderCachesResultsAndRetriesFailures(t *testing.T) {
	var calls atomic.Int32
	var failing atomic.Bool
	u := Updater{CacheDir: t.TempDir(), HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if failing.Load() {
			return nil, errors.New("offline")
		}
		return response(releaseJSON("v0.4.2")), nil
	})}}
	for range 2 {
		if info := u.Reminder(context.Background(), "v0.4.1"); !info.UpdateAvailable || info.UpdateCommand != UpdateCommand {
			t.Fatalf("reminder missing: %+v", info)
		}
	}
	if calls.Load() != 1 || u.Reminder(context.Background(), "v0.4.2").UpdateAvailable {
		t.Fatal("fresh cache was not reused or prompted the current version")
	}
	for _, current := range []string{"v0.4.2-rc.1", "v0.4.1+build.1"} {
		if !u.Reminder(context.Background(), current).UpdateAvailable {
			t.Fatalf("release build lost its update notice: %s", current)
		}
	}
	path := filepath.Join(u.CacheDir, "update-check.json")
	data, _ := json.Marshal(checkCache{CheckedAt: time.Now().Add(-25 * time.Hour), Latest: "v0.4.2"})
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	failing.Store(true)
	for range 2 {
		if !u.Reminder(context.Background(), "v0.4.1").UpdateAvailable {
			t.Fatal("offline refresh discarded the previous known update")
		}
	}
	if calls.Load() != 2 {
		t.Fatal("failed refresh was not throttled")
	}
}

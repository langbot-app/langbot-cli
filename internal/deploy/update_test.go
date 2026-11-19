package deploy

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUpdateCacheSharesDeadlineAndDoesNotCacheFailure(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("AppData", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	started := make(chan struct{}, 2)
	check := func(ctx context.Context) (string, error) {
		started <- struct{}{}
		<-ctx.Done()
		return "", ctx.Err()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := RefreshUpdateCacheWith(ctx, check, check); err == nil {
		t.Fatal("failed check succeeded")
	}
	if len(started) != 2 || !ReadUpdateCache().LBCTLCheckedAt.IsZero() || !ReadUpdateCache().CoreCheckedAt.IsZero() {
		t.Fatal("checks were not shared or failure was cached")
	}
	lbctl := func(context.Context) (string, error) { return "v0.5.3", nil }
	core := func(context.Context) (string, error) { return "v4.11.8", nil }
	if err := RefreshUpdateCacheWith(context.Background(), lbctl, core); err != nil {
		t.Fatal(err)
	}
	cache := ReadUpdateCache()
	if cache.LBCTL != "v0.5.3" || cache.Core != "v4.11.8" || cache.NeedsRefresh() {
		t.Fatalf("cache=%+v", cache)
	}
	if err := RefreshUpdateCacheWith(context.Background(), check, check); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateCacheRefreshesEachReleaseIndependently(t *testing.T) {
	for _, failed := range []string{"lbctl", "core"} {
		t.Run(failed, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("AppData", t.TempDir())
			t.Setenv("HOME", t.TempDir())
			calls := map[string]int{"lbctl": 0, "core": 0}
			lbctlCalls, coreCalls := 0, 0
			check := func(name string, count *int) func(context.Context) (string, error) {
				return func(context.Context) (string, error) {
					*count++
					if name == failed && *count == 1 {
						return "", fmt.Errorf("offline")
					}
					return "v1.0.0", nil
				}
			}
			lbctl, core := check("lbctl", &lbctlCalls), check("core", &coreCalls)
			if err := RefreshUpdateCacheWith(context.Background(), lbctl, core); err != nil {
				t.Fatal(err)
			}
			partial := ReadUpdateCache()
			if !partial.NeedsRefresh() || partial.LBCTLCheckedAt.IsZero() != (failed == "lbctl") || partial.CoreCheckedAt.IsZero() != (failed == "core") {
				t.Fatalf("partial cache: %+v", partial)
			}
			if err := RefreshUpdateCacheWith(context.Background(), lbctl, core); err != nil {
				t.Fatal(err)
			}
			calls["lbctl"], calls["core"] = lbctlCalls, coreCalls
			for name, count := range calls {
				want := 1
				if name == failed {
					want = 2
				}
				if count != want {
					t.Fatalf("%s checks=%d, want %d", name, count, want)
				}
			}
			if ReadUpdateCache().NeedsRefresh() {
				t.Fatal("successful cache is stale")
			}
		})
	}
}

func TestSelfUpdateChecksChecksumAndReplacesBinary(t *testing.T) {
	for _, mode := range []string{"update", "check", "bad-checksum", "brew", "gzip", "bad-gzip", "bad-gzip-checksum", "bad-expanded-checksum"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "lbctl")
			if mode == "brew" {
				path = filepath.Join(t.TempDir(), "Cellar", "lbctl", "bin", "lbctl")
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(path, []byte("old binary"), 0o755); err != nil {
				t.Fatal(err)
			}
			const asset = "lbctl_linux_amd64"
			compressed := strings.Contains(mode, "gzip") || mode == "bad-expanded-checksum"
			binary := []byte("new binary")
			var archive bytes.Buffer
			gzipWriter := gzip.NewWriter(&archive)
			if _, err := gzipWriter.Write(binary); err != nil {
				t.Fatal(err)
			}
			if err := gzipWriter.Close(); err != nil {
				t.Fatal(err)
			}
			if mode == "bad-gzip" {
				archive.Truncate(archive.Len() - 1)
			}
			downloads := 0
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				switch request.URL.Path {
				case "/latest.json":
					fmt.Fprint(writer, `{"version":"v0.5.0"}`)
				case "/releases/v0.5.0/checksums.txt":
					digest := fmt.Sprintf("%x", sha256.Sum256(binary))
					if mode == "bad-checksum" || mode == "bad-expanded-checksum" {
						digest = strings.Repeat("0", 64)
					}
					fmt.Fprintf(writer, "%s  %s\n", digest, asset)
					if compressed {
						digest := fmt.Sprintf("%x", sha256.Sum256(archive.Bytes()))
						if mode == "bad-gzip-checksum" {
							digest = strings.Repeat("0", 64)
						}
						fmt.Fprintf(writer, "%s  %s.gz\n", digest, asset)
					}
				case "/releases/v0.5.0/" + asset + ".gz":
					if !compressed {
						t.Error("requested an unlisted archive")
					}
					writer.Write(archive.Bytes())
				case "/releases/v0.5.0/" + asset:
					if compressed {
						t.Error("requested the raw binary despite a listed archive")
					}
					downloads++
					writer.Write(binary)
				default:
					t.Errorf("unexpected URL %s", request.URL.Path)
					http.NotFound(writer, request)
				}
			}))
			defer server.Close()
			result, err := (Updater{BaseURL: server.URL, Client: server.Client(), Executable: path, Platform: "linux/amd64"}).Update(context.Background(), "v0.4.1", mode == "check")
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if mode == "update" || mode == "gzip" {
				if err != nil || !result.Updated || string(data) != "new binary" {
					t.Fatalf("%+v %s %v", result, data, err)
				}
			} else if result.Updated || string(data) != "old binary" {
				t.Fatalf("changed binary: %+v %s", result, data)
			}
			if strings.HasPrefix(mode, "bad-") && err == nil {
				t.Fatal("accepted invalid download")
			}
			if (mode == "check" || mode == "brew") && downloads != 0 {
				t.Fatal("downloaded binary during check/package-managed update")
			}
			if mode == "brew" && result.Command != "brew upgrade lbctl" {
				t.Fatal(result)
			}
		})
	}
}

type releaseTransport func(*http.Request) (*http.Response, error)

func (transport releaseTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestLatestCoreSpaceAndGitHubFallback(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		t.Run(fmt.Sprint(fallback), func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				requests++
				if requests == 1 {
					if fallback {
						writer.WriteHeader(503)
						return
					}
					fmt.Fprint(writer, `{"code":0,"data":[{"tag_name":"v4.12.0","prerelease":true},{"tag_name":"v4.11.7"},{"tag_name":"v4.11.8"}]}`)
					return
				}
				fmt.Fprint(writer, `{"tag_name":"v4.11.8"}`)
			}))
			defer server.Close()
			client := &http.Client{Transport: releaseTransport(func(request *http.Request) (*http.Response, error) {
				if requests == 0 && request.URL.String() != CoreReleasesURL {
					t.Error(request.URL)
				}
				if requests == 1 && request.URL.String() != GitHubReleaseURL {
					t.Error(request.URL)
				}
				replacement, _ := http.NewRequestWithContext(request.Context(), request.Method, server.URL, nil)
				return http.DefaultTransport.RoundTrip(replacement)
			})}
			version, err := LatestCore(context.Background(), client)
			if err != nil || version != "v4.11.8" {
				t.Fatalf("%s %v", version, err)
			}
		})
	}
}

func TestRegionEnvironmentAndInstancePaths(t *testing.T) {
	t.Setenv("LANGBOT_REGION", "cn")
	region, err := ResolveRegion(context.Background(), "auto", nil)
	if err != nil || region != "cn" {
		t.Fatalf("%s %v", region, err)
	}
	record := Record{Region: region}
	ApplyRegion(&record)
	if record.ImageRepository != CanonicalImageRepository || record.PythonMirror == "" || !strings.Contains(record.PyPIIndex, "aliyun") {
		t.Fatal(record)
	}
	t.Setenv("LANGBOT_HOME", t.TempDir())
	if _, err := InstanceDir("../escape"); err == nil {
		t.Fatal("accepted traversal")
	}
	if dir, err := InstanceDir("default"); err != nil || filepath.Base(dir) != "default" {
		t.Fatalf("%s %v", dir, err)
	}
}

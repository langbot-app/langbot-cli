package deploy

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

// trickle serves body in small chunks with a pause between them, like a slow
// but healthy link (CN users on download.langbot.app).
func trickle(body []byte, chunk int, pause time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		for start := 0; start < len(body); start += chunk {
			end := min(start+chunk, len(body))
			if _, err := w.Write(body[start:end]); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(pause):
			}
		}
	}
}

func TestFetchWithStallTimeoutFinishesSlowSteadyDownload(t *testing.T) {
	body := bytes.Repeat([]byte("x"), 4096)
	server := httptest.NewServer(trickle(body, 256, 40*time.Millisecond)) // ~640ms total
	defer server.Close()
	// A whole-request deadline shorter than the transfer cuts it off ...
	if _, err := Fetch(context.Background(), &http.Client{Timeout: 300 * time.Millisecond}, server.URL, 1<<20); err == nil {
		t.Fatal("expected whole-request deadline to cut off the slow download")
	}
	// ... while a stall timeout of the same size lets it finish.
	data, err := FetchWithStallTimeout(context.Background(), &http.Client{}, server.URL, 1<<20, 300*time.Millisecond)
	if err != nil || !bytes.Equal(data, body) {
		t.Fatalf("slow steady download failed: %v (%d bytes)", err, len(data))
	}
}

func TestFetchWithStallTimeoutAbortsStalledDownload(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("partial"))
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	started := time.Now()
	_, err := FetchWithStallTimeout(context.Background(), &http.Client{}, server.URL, 1<<20, 200*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), server.URL) {
		t.Fatalf("expected stall error naming the source, got %v", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("stall detection took %s", elapsed)
	}
}

func fakeUVArchive(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	archive := tar.NewWriter(gz)
	content := []byte("#!/bin/sh\nexit 0\n")
	if err := archive.WriteHeader(&tar.Header{Name: "uv-dir/uv", Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	_, _ = archive.Write(content)
	_ = archive.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func withUVSources(t *testing.T, github, mirror string, archive []byte) {
	t.Helper()
	platform := CurrentPlatform()
	oldAsset, oldGitHub, oldMirror, oldStall := UVAssets[platform], uvGitHubBase, uvMirrorBase, uvDownloadStallTimeout
	sum := sha256.Sum256(archive)
	UVAssets[platform] = UVAsset{"uv-test.tar.gz", hex.EncodeToString(sum[:])}
	uvGitHubBase, uvMirrorBase, uvDownloadStallTimeout = github, mirror, 300*time.Millisecond
	t.Cleanup(func() {
		UVAssets[platform], uvGitHubBase, uvMirrorBase, uvDownloadStallTimeout = oldAsset, oldGitHub, oldMirror, oldStall
	})
}

func TestEnsureUVCompletesSlowMirrorDownload(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("tar.gz fake asset")
	}
	archive := fakeUVArchive(t)
	mirror := httptest.NewServer(trickle(archive, 16, 30*time.Millisecond))
	defer mirror.Close()
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	defer github.Close()
	withUVSources(t, github.URL, mirror.URL, archive)
	uv, err := EnsureUV(context.Background(), t.TempDir(), "cn", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(uv); err != nil || info.Size() == 0 {
		t.Fatalf("uv not written: %v", err)
	}
}

func TestEnsureUVReportsEverySourceFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("tar.gz fake asset")
	}
	release := make(chan struct{})
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer mirror.Close()
	defer close(release)
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	defer github.Close()
	withUVSources(t, github.URL, mirror.URL, fakeUVArchive(t))
	_, err := EnsureUV(context.Background(), t.TempDir(), "cn", io.Discard)
	if err == nil {
		t.Fatal("expected failure")
	}
	if !strings.Contains(err.Error(), mirror.URL) || !strings.Contains(err.Error(), github.URL) {
		t.Fatalf("error must name both sources, got %q", err)
	}
}

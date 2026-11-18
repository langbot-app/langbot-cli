package deploy

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestResolveRegionFallsBackToLocaleWhenProbeUnreachable(t *testing.T) {
	block := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer close(block)
	oldURL, oldTimeout, oldSystem := regionTraceURL, regionProbeTimeout, regionSystemLocale
	defer func() { regionTraceURL, regionProbeTimeout, regionSystemLocale = oldURL, oldTimeout, oldSystem }()
	regionTraceURL, regionProbeTimeout = server.URL, 50*time.Millisecond
	t.Setenv("LANGBOT_REGION", "")
	for _, key := range []string{"LBCTL_LANG", "LANGBOT_LANG", "LC_ALL", "LC_MESSAGES"} {
		t.Setenv(key, "")
	}
	cases := []struct{ lang, system, want string }{
		{"zh_CN.UTF-8", "en-US", "cn"},
		{"C.UTF-8", "zh-Hans-CN", "cn"},
		{"C.UTF-8", "zh-CN", "cn"},
		{"zh_TW.UTF-8", "zh-CN", "global"},
		{"C.UTF-8", "zh-Hant-TW", "global"},
		{"en_US.UTF-8", "zh-CN", "global"},
		{"", "", "global"},
	}
	for _, item := range cases {
		t.Setenv("LANG", item.lang)
		system := item.system
		regionSystemLocale = func() string { return system }
		region, err := ResolveRegion(context.Background(), "auto", server.Client())
		if err != nil || region != item.want {
			t.Fatalf("LANG=%q system=%q: got %q %v, want %q", item.lang, item.system, region, err, item.want)
		}
	}
	// A reachable probe still wins over the locale.
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ip=1.2.3.4\nloc=US\n") }))
	defer ok.Close()
	regionTraceURL = ok.URL
	t.Setenv("LANG", "zh_CN.UTF-8")
	if region, _ := ResolveRegion(context.Background(), "auto", ok.Client()); region != "global" {
		t.Fatalf("probe result ignored: %s", region)
	}
}

func fakeUVInstall(t *testing.T, script string) (string, Record) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell fake uv")
	}
	root := t.TempDir()
	tools := filepath.Join(root, "tools")
	uvDir := filepath.Join(tools, "uv", UVVersion)
	if err := os.MkdirAll(uvDir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := "#!/bin/sh\nLOG=" + filepath.Join(root, "calls.log") + "\necho \"$*\" >> \"$LOG\"\n" + script
	if err := os.WriteFile(filepath.Join(uvDir, "uv"), []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	record := Record{CoreVersion: "v4.11.8", Region: "cn", PyPIIndex: "https://mirrors.aliyun.com/pypi/simple", ToolsDir: tools}
	return root, record
}

func TestPrepareNativeRetriesTransientWindowsFileLock(t *testing.T) {
	oldDelay := nativeRetryDelay
	nativeRetryDelay = time.Millisecond
	defer func() { nativeRetryDelay = oldDelay }()
	root, record := fakeUVInstall(t, `case "$1" in pip)
  if [ ! -f "$LOG.locked" ]; then touch "$LOG.locked"; echo 'error: Failed to install: websockets-15.0.1-cp312-cp312-win_amd64.whl' >&2; echo '  Caused by: 拒绝访问。 (os error -2147024891)' >&2; exit 2; fi;;
esac
exit 0
`)
	if err := PrepareNative(context.Background(), root, record, io.Discard); err != nil {
		t.Fatal(err)
	}
	calls, _ := os.ReadFile(filepath.Join(root, "calls.log"))
	if strings.Count(string(calls), "mirrors.aliyun.com") != 2 || strings.Contains(string(calls), "tuna") {
		t.Fatalf("expected same-index retry only:\n%s", calls)
	}
}

func TestPrepareNativeReportsPrimaryIndexFailure(t *testing.T) {
	root, record := fakeUVInstall(t, `case "$*" in
  *aliyun*) echo 'error: primary index broke: no matching distribution' >&2; exit 2;;
  *tuna*) echo 'error: HTTP status client error (403 Forbidden)' >&2; exit 2;;
esac
exit 0
`)
	err := PrepareNative(context.Background(), root, record, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "primary index broke") || strings.Contains(err.Error(), "403") {
		t.Fatalf("want primary error, got %v", err)
	}
	calls, _ := os.ReadFile(filepath.Join(root, "calls.log"))
	if strings.Count(string(calls), "mirrors.aliyun.com") != 1 || !strings.Contains(string(calls), "tuna") {
		t.Fatalf("non-lock failure must not retry, but must still try fallback:\n%s", calls)
	}
}

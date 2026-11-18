package command

import (
	"bytes"
	"runtime"
	"strings"
	"testing"

	"github.com/langbot-app/langbot-cli/internal/deploy"
)

func TestInstallBoxSelectionDefaultsFreshNativeToHost(t *testing.T) {
	deps := Dependencies{LocalDir: t.TempDir(), In: strings.NewReader(""), Err: &bytes.Buffer{}}
	backend, err := installBoxSelection(deps, "native", "", "v4.11.8", "", false, false)
	if err != nil || (backend != "host") == hostBoxDefaultAllowed(runtime.GOOS, "v4.11.8") {
		t.Fatalf("backend=%q err=%v", backend, err)
	}
}

func TestInstallBoxSelectionKeepsExplicitAndNonNative(t *testing.T) {
	deps := Dependencies{LocalDir: t.TempDir()}
	for _, test := range []struct{ driver, backend, want string }{
		{"native", "local", "local"},
		{"native", "off", "off"},
		{deploy.DriverDockerCompose, "", ""},
	} {
		got, err := installBoxSelection(deps, test.driver, "", "", test.backend, true, false)
		if err != nil || got != test.want {
			t.Fatalf("%+v: got %q err=%v", test, got, err)
		}
	}
}

func TestInstallBoxSelectionLeavesExistingInstallation(t *testing.T) {
	dir := t.TempDir()
	if err := (deploy.Store{Dir: dir}).Write(t.Context(), deploy.Record{Driver: deploy.DriverNative, DeploymentID: "existing", CoreVersion: "v4.11.8", Port: 5300}); err != nil {
		t.Fatal(err)
	}
	got, err := installBoxSelection(Dependencies{LocalDir: dir}, "native", "", "", "", true, false)
	if err != nil || got != "" {
		t.Fatalf("got %q err=%v", got, err)
	}
}

func TestPromptBoxBackend(t *testing.T) {
	for input, want := range map[string]string{"\n": "host", "1\n": "host", "2\n": "local", "docker\n": "docker", "4": "off"} {
		var out bytes.Buffer
		got, err := promptBoxBackend(strings.NewReader(input), &out)
		if err != nil || got != want {
			t.Fatalf("%q: got %q err=%v", input, got, err)
		}
		if !strings.Contains(out.String(), "prompt injection") {
			t.Fatalf("prompt omits the risk: %s", out.String())
		}
	}
	if _, err := promptBoxBackend(strings.NewReader("9\n"), &bytes.Buffer{}); err == nil {
		t.Fatal("invalid selection accepted")
	}
}

func TestHostBoxDefaultAllowedOnWindowsNeedsSupportedCore(t *testing.T) {
	if !hostBoxDefaultAllowed("linux", "") || !hostBoxDefaultAllowed("darwin", "v4.0.0") {
		t.Fatal("POSIX must allow host by default")
	}
	if hostBoxDefaultAllowed("windows", "v4.11.8") {
		t.Fatal("Windows Core without SDK 0.7.26 must not default to host")
	}
	for _, version := range []string{"", "v4.11.9", "v4.12.0"} {
		if !hostBoxDefaultAllowed("windows", version) {
			t.Fatalf("Windows Core %q supports host Box", version)
		}
	}
}

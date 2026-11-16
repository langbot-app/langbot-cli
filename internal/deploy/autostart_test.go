package deploy

import (
	"context"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestAutostartToggleCommands(t *testing.T) {
	for _, test := range []struct {
		manager string
		enabled bool
		args    []string
	}{
		{"systemd", true, []string{"systemctl", "enable", "langbot-test"}},
		{"systemd", false, []string{"systemctl", "disable", "langbot-test"}},
		{"systemd-user", true, []string{"systemctl", "--user", "enable", "langbot-test"}},
		{"systemd-user", false, []string{"systemctl", "--user", "disable", "langbot-test"}},
		{"schtasks", true, []string{"schtasks.exe", "/Change", "/TN", "langbot-test", "/Enable"}},
		{"schtasks", false, []string{"schtasks.exe", "/Change", "/TN", "langbot-test", "/Disable"}},
	} {
		command := nativeAutostartToggle(context.Background(), Record{ServiceManager: test.manager, DeploymentID: "test"}, test.enabled)
		if !reflect.DeepEqual(command.Args, test.args) {
			t.Fatalf("%v != %v", command.Args, test.args)
		}
	}
}

func TestLaunchdAutostartDoesNotStopRunningInstance(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	dir, record := nativeFixture(t)
	record.ServiceManager = "launchd"
	for _, enabled := range []bool{true, false, true} {
		if err := ChangeAutostart(context.Background(), dir, record, enabled, nil); err != nil {
			t.Fatal(err)
		}
		info, err := AutostartStatus(context.Background(), record)
		if err != nil || info.Enabled != enabled || info.Location != filepath.Join(home, "Library", "LaunchAgents", "langbot-native-test.plist") {
			t.Fatalf("%+v: %v", info, err)
		}
		if _, err := os.Stat(filepath.Join(dir, "native.stop")); !os.IsNotExist(err) {
			t.Fatal("autostart toggle requested process stop")
		}
		var readErr error
		record, readErr = (Store{Dir: dir}).Read()
		if readErr != nil || record.Autostart == nil || *record.Autostart != enabled {
			t.Fatalf("%+v: %v", record, readErr)
		}
	}
}

type autostartRunner struct {
	calls []string
	fail  bool
}

func (runner *autostartRunner) Compose(_ context.Context, _ Record, args ...string) (CommandResult, error) {
	runner.calls = append(runner.calls, "compose "+strings.Join(args, " "))
	return CommandResult{Stdout: "container-one\ncontainer-two\n"}, nil
}

func (runner *autostartRunner) Docker(_ context.Context, args ...string) (CommandResult, error) {
	runner.calls = append(runner.calls, "docker "+strings.Join(args, " "))
	if runner.fail && strings.Contains(strings.Join(args, " "), "--restart=no") {
		return CommandResult{Stderr: "update denied"}, errors.New("exit 1")
	}
	return CommandResult{}, nil
}

func TestDockerAutostartPersistsAcrossUpgradeWithoutRestart(t *testing.T) {
	record, dir := upgradeFixture(t)
	runner := &autostartRunner{}
	for _, enabled := range []bool{false, true, false} {
		if err := ChangeAutostart(context.Background(), dir, record, enabled, runner); err != nil {
			t.Fatal(err)
		}
		var err error
		record, err = (Store{Dir: dir}).Read()
		if err != nil || record.Autostart == nil || *record.Autostart != enabled {
			t.Fatalf("%+v: %v", record, err)
		}
		info, err := AutostartStatus(context.Background(), record)
		if err != nil || info.Enabled != enabled {
			t.Fatalf("%+v: %v", info, err)
		}
		content, _, err := ComposeForUpgrade(record, dir, "v4.10.10")
		policy := `restart: "no"`
		if enabled {
			policy = "restart: unless-stopped"
		}
		if err != nil || strings.Count(string(content), policy) != 3 {
			t.Fatalf("%s: %v", content, err)
		}
	}
	if !reflect.DeepEqual(runner.calls[:2], []string{"compose ps --all --quiet", "docker update --restart=no container-one container-two"}) {
		t.Fatal(runner.calls)
	}
	for _, call := range runner.calls {
		if strings.HasPrefix(call, "compose stop") || strings.Contains(call, "up -d") {
			t.Fatal("autostart restarted containers: " + call)
		}
	}
}

func TestDockerAutostartRollsBackFailedPolicyUpdate(t *testing.T) {
	record, dir := upgradeFixture(t)
	original, _ := os.ReadFile(record.ComposeFile)
	err := ChangeAutostart(context.Background(), dir, record, false, &autostartRunner{fail: true})
	if err == nil {
		t.Fatal("expected update failure")
	}
	current, _ := os.ReadFile(record.ComposeFile)
	saved, _ := (Store{Dir: dir}).Read()
	if string(current) != string(original) || saved.Autostart == nil || !*saved.Autostart {
		t.Fatal("failed toggle changed saved policy")
	}
}

func TestLegacyDockerRestartPolicyRemainsUpgradeable(t *testing.T) {
	record, dir := upgradeFixture(t)
	record.Autostart = nil
	content, err := os.ReadFile(record.ComposeFile)
	if err != nil {
		t.Fatal(err)
	}
	content = []byte(strings.ReplaceAll(string(content), "restart: unless-stopped", "restart: on-failure"))
	if err := os.WriteFile(record.ComposeFile, content, 0o600); err != nil {
		t.Fatal(err)
	}
	updated, _, err := ComposeForUpgrade(record, dir, "v4.10.10")
	if err != nil || strings.Count(string(updated), "restart: on-failure") != 3 {
		t.Fatalf("%s: %v", updated, err)
	}
}

func TestScheduledTaskUTF16Status(t *testing.T) {
	text := `<?xml version="1.0" encoding="UTF-16"?><Task><Settings><Enabled>false</Enabled></Settings><Actions><Exec><Command>C:\中文\lbctl.exe</Command></Exec></Actions></Task>`
	units := utf16.Encode([]rune(text))
	encoded := []byte{0xff, 0xfe}
	for _, unit := range units {
		encoded = binary.LittleEndian.AppendUint16(encoded, unit)
	}
	for _, input := range [][]byte{encoded, []byte(text)} {
		var task struct {
			Enabled bool `xml:"Settings>Enabled"`
		}
		if err := xml.Unmarshal(scheduledTaskXML(input), &task); err != nil || task.Enabled {
			t.Fatalf("%+v: %v", task, err)
		}
	}
}

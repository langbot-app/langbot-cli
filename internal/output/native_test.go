package output

import (
	"bytes"
	"strings"
	"testing"

	"github.com/langbot-app/langbot-cli/internal/deploy"
	"github.com/langbot-app/langbot-cli/internal/i18n"
)

func TestNativeLifecycleRendering(t *testing.T) {
	for _, test := range []struct {
		action, state, want string
	}{
		{"install", "running", "已安装 LangBot v4.11.8 → http://127.0.0.1:15300"},
		{"start", "running", "已启动本机部署"},
		{"stop", "stopped", "已停止本机部署"},
		{"restart", "running", "已重启本机部署"},
		{"upgrade", "", "已升级 LangBot v4.11.7 → v4.11.8"},
		{"uninstall", "", "已卸载本机受管部署；数据和部署记录已保留"},
	} {
		t.Run(test.action, func(t *testing.T) {
			data := map[string]any{"action": test.action, "changed": true, "completed": true, "project_removed": true,
				"source_version": "v4.11.7", "target_version": "v4.11.8",
				"record": deploy.Record{Driver: deploy.DriverNative, CoreVersion: "v4.11.8", Endpoint: "http://127.0.0.1:15300"},
				"status": map[string]any{"status": test.state}}
			var out bytes.Buffer
			if err := RenderCommand(&out, map[string]any{"ok": true, "data": data}, FormatDefault, test.action); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), test.want) || strings.Contains(out.String(), "待确认") {
				t.Fatal(out.String())
			}
			if test.action == "install" {
				for _, next := range []string{"lbctl status", "lbctl logs", "lbctl upgrade --yes"} {
					if !strings.Contains(out.String(), next) {
						t.Fatalf("missing %s: %s", next, out.String())
					}
				}
			}
		})
	}
}

func TestNativeInstallFailureRendering(t *testing.T) {
	data := map[string]any{"action": "install", "changed": true, "step": "install_service", "log_path": "logs/install.log", "runtime_log_path": "logs/langbot.log", "retry_command": "lbctl install --dir '中文 dir'", "rollback_command": "lbctl uninstall --dir '中文 dir'"}
	var out bytes.Buffer
	if err := renderLocalAction(&out, data); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"安装失败", "install_service", "logs/install.log", "logs/langbot.log", "lbctl install", "lbctl uninstall"} {
		if !strings.Contains(out.String(), value) {
			t.Fatalf("missing %s: %s", value, out.String())
		}
	}
	if strings.Contains(out.String(), "待确认") {
		t.Fatal(out.String())
	}
}

func TestNativeInstallFailureOmitsUnavailableDetails(t *testing.T) {
	for _, step := range []string{"check_ports", "register", "write_record"} {
		var out bytes.Buffer
		if err := renderLocalAction(&out, map[string]any{"action": "install", "step": step}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "失败步骤："+step) {
			t.Fatal(out.String())
		}
		for _, absent := range []string{"<nil>", "日志：", "回滚：", "重试："} {
			if strings.Contains(out.String(), absent) {
				t.Fatal(out.String())
			}
		}
	}
}

func TestInstallAutostartHint(t *testing.T) {
	defer i18n.Use(i18n.Chinese)()
	var out bytes.Buffer
	err := renderLocalAction(&out, map[string]any{"action": "install", "changed": true, "status": map[string]any{"status": "running"}, "autostart": true, "autostart_mechanism": "HKCU Run", "autostart_disable_command": "lbctl autostart disable --name 'mybot'"})
	if err != nil || !strings.Contains(out.String(), "已开启开机/登录自启 (HKCU Run)") || strings.Contains(out.String(), "Autostart enabled") || !strings.Contains(out.String(), "lbctl autostart disable --name 'mybot'") {
		t.Fatalf("%s: %v", out.String(), err)
	}
}

func TestNativeDryRunAndFailedUpgradeRendering(t *testing.T) {
	for _, test := range []struct {
		data map[string]any
		want string
	}{
		{map[string]any{"action": "install", "dry_run": true, "version": "v4.11.8"}, "安装计划"},
		{map[string]any{"action": "upgrade", "completed": false}, "升级未完成"},
		{map[string]any{"action": "upgrade", "dry_run": true, "source_version": "v4.11.7", "target_version": "v4.11.8"}, "升级计划：LangBot v4.11.7 → v4.11.8"},
	} {
		var out bytes.Buffer
		if err := renderLocalAction(&out, test.data); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), test.want) || strings.Contains(out.String(), "下一步") {
			t.Fatal(out.String())
		}
	}
}

func TestNativeDoctorLabel(t *testing.T) {
	got := lifecycleLabel(map[string]any{"management": "local_managed", "driver": "native"})
	if got != "原生部署 · lbctl 管理" {
		t.Fatal(got)
	}
}

func TestUpdateOutputUsesChineseLabels(t *testing.T) {
	var out bytes.Buffer
	value := deploy.UpdateResult{Current: "v0.4.0", Latest: "v0.4.1", Available: true, Updated: true}
	if err := RenderCommand(&out, map[string]any{"ok": true, "data": value}, FormatDefault, "update"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"当前", "最新版本", "已更新"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %s: %s", want, out.String())
		}
	}
}

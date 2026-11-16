package output

import (
	"bytes"
	"strings"
	"testing"

	"github.com/langbot-app/langbot-cli/internal/deploy"
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

package output

import (
	"bytes"
	"strings"
	"testing"
)

func TestDefaultUninstallOutputIsConcise(t *testing.T) {
	var out bytes.Buffer
	value := map[string]any{
		"ok": true,
		"data": map[string]any{
			"action": "uninstall", "project_removed": true, "purge_data": false,
			"data_dir": "/secret/local/data", "record_preserved": true,
		},
	}
	if err := RenderCommand(&out, value, FormatDefault, "uninstall"); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != "已卸载本机受管部署；数据和部署记录已保留" || strings.Contains(out.String(), "/secret/local/data") {
		t.Fatalf("unexpected uninstall output: %q", out.String())
	}
}

func TestHumanActionNeverConfusesPreflightOrSubmissionWithCompletion(t *testing.T) {
	for _, test := range []struct {
		data         map[string]any
		ok           bool
		want, absent string
	}{
		{map[string]any{"dry_run": true}, true, "预检通过", "操作已完成"},
		{map[string]any{"dry_run": true}, false, "操作未完整确认", "预检通过"},
		{map[string]any{"submitted": true, "task_id": "42"}, true, "请求已提交", "操作已完成"},
		{map[string]any{"submitted": true, "step": "completed", "task_id": "42"}, true, "任务已完成", "请查看任务状态确认"},
		{map[string]any{"dry_run": true, "business_validation": "server_preview"}, true, "服务端预览已完成", "未确认服务端业务校验"},
		{map[string]any{"verified": false, "verification": map[string]any{"observable_fields_match": true, "unverified_fields": []string{"token"}}}, true, "隐藏字段未验证", "回读验证通过"},
	} {
		var out bytes.Buffer
		value := map[string]any{"ok": test.ok, "data": test.data}
		if !test.ok {
			value["error"] = map[string]any{"type": "precondition", "message": "检查失败"}
		}
		if err := RenderCommand(&out, value, FormatDefault, "bot.update"); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), test.want) || strings.Contains(out.String(), test.absent) {
			t.Fatalf("misleading outcome: %s", &out)
		}
	}
}

func TestLongHumanListsPreserveIDsAndMachineNames(t *testing.T) {
	t.Setenv("COLUMNS", "60")
	name := strings.Repeat("模型", 30)
	id := "12345678-1234-1234-1234-123456789012"
	value := map[string]any{"ok": true, "data": map[string]any{"models": []any{map[string]any{"name": name, "uuid": id, "provider_uuid": id}}}}
	var human, machine bytes.Buffer
	if err := RenderCommand(&human, value, FormatDefault, "model.list"); err != nil {
		t.Fatal(err)
	}
	if err := RenderCommand(&machine, value, FormatJSON, "model.list"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(human.String(), name) || !strings.Contains(human.String(), id) || !strings.Contains(machine.String(), name) {
		t.Fatalf("list lost identity or machine content: %s / %s", &human, &machine)
	}
}

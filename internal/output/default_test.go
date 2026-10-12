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

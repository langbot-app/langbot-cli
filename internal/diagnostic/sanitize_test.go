package diagnostic

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestText(t *testing.T) {
	for _, test := range []struct{ raw, want string }{
		{`https://u:p@mirrors.aliyun.com/pypi/simple?token=x&other=y`, `https://mirrors.aliyun.com/pypi/simple?token=[redacted]&other=[redacted]`},
		{"Failed to fetch: `https://127.0.0.1:9/simple/langbot/`", "Failed to fetch: `https://127.0.0.1:9/simple/langbot/`"},
		{"Failed to fetch: `https://user:pass@example.org/simple/?token=secret`", "Failed to fetch: `https://example.org/simple/?token=[redacted]`"},
		{`Access denied: C:\Users\10105\AppData\Local\LangBot\config.yaml`, `Access denied: C:\Users\<user>\AppData\Local\LangBot\config.yaml`},
		{`launchctl: /Users/alice/Library/LaunchAgents/langbot.plist: Input/output error`, `launchctl: /Users/<user>/Library/LaunchAgents/langbot.plist: Input/output error`},
		{`Permission denied: /home/alice/.langbot/default/logs/install.log`, `Permission denied: /home/<user>/.langbot/default/logs/install.log`},
		{`C:/Users/alice/AppData/Local/uv`, `C:/Users/<user>/AppData/Local/uv`},
		{`C:\Users\中文 用户\AppData\Local\uv`, `C:\Users\<user>\AppData\Local\uv`},
		{`/Users/Some User/Library/logs`, `/Users/<user>/Library/logs`},
		{`/var/log/langbot.log /opt/langbot/config.yaml`, `/var/log/langbot.log /opt/langbot/config.yaml`},
		{`127.0.0.1 10.0.0.5 192.168.0.2 172.16.0.5 ::1 fd00::1`, `127.0.0.1 10.0.0.5 192.168.0.2 172.16.0.5 ::1 fd00::1`},
		{`8.8.8.8 2001:4860:4860::8888`, `[redacted] [redacted]`},
		{`api_key: "test key"`, `api_key: [redacted]`},
		{`password=private secret: private token: private`, `password=[redacted] secret: [redacted] token: [redacted]`},
		{`Authorization: Bearer abc.def`, `Authorization: [redacted]`},
		{`command --api-key "private value" --port 5300`, `command --api-key [redacted] --port 5300`},
		{"secret: |\n  private\n  value\nport: 5300", "secret: [redacted]\n  [redacted]\n  [redacted]\nport: 5300"},
		{`sk-abc123 ghp_abcdef eyJhbGciOiJI.eyJhIjoiYiJ9.signature`, `[redacted] [redacted] [redacted]`},
	} {
		if got := Text(test.raw); got != test.want {
			t.Errorf("%q: got %q, want %q", test.raw, got, test.want)
		}
		if got := Text(test.want); got != test.want {
			t.Errorf("not idempotent: %q -> %q", test.want, got)
		}
	}
}

func TestObjectAndBudget(t *testing.T) {
	value := Object(map[string]any{"config": map[string]any{"api_key": "private", "port": 5300}, "args": []any{"--token", "private", "--index-url", "https://u:p@example.org/simple?key=private"}, "stdout": strings.Repeat("日志\n", 10000), "stderr": strings.Repeat("failed\n", 10000), "log": map[string]any{"tail": strings.Repeat("日志\n", 10000)}})
	Fit(value, 12000)
	data, err := json.Marshal(value)
	if err != nil || len(data) > 12000 || !utf8.Valid(data) || strings.Contains(string(data), "private") || strings.Contains(string(data), "u:p") {
		t.Fatalf("budget/redaction: bytes=%d err=%v", len(data), err)
	}
	if got := Tail("one\ntwo\nthree\n", 2, 100); got != "two\nthree" {
		t.Fatal(got)
	}
	if got := Tail(strings.Repeat("文", 100), 0, 10); len(got) > 10 || !utf8.ValidString(got) {
		t.Fatal(got)
	}
}

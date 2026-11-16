package progress

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestProgressFixedRegionAndFallback(t *testing.T) {
	for _, mode := range []string{"ansi", "carriage", "plain"} {
		t.Run(mode, func(t *testing.T) {
			var out bytes.Buffer
			display := New(&out, func(string) (string, bool) { return "", false }, false, false)
			display.mu.Lock()
			display.carriage, display.animated = mode != "plain", mode == "ansi"
			display.mu.Unlock()
			display.Step("Start service")
			display.Write([]byte(strings.Repeat("long output ", 30)))
			time.Sleep(250 * time.Millisecond)
			display.Finish(false)
			text := out.String()
			if mode == "ansi" && !strings.Contains(text, "\x1b[1A") {
				t.Fatal("fixed region not cleared")
			}
			if mode != "ansi" && strings.Contains(text, "\x1b") {
				t.Fatal("ANSI in fallback")
			}
			if mode == "plain" && strings.ContainsAny(text, "\r|") {
				t.Fatal("heartbeat in plain output")
			}
		})
	}
}

func TestCellWidthAndPlainPanel(t *testing.T) {
	if got := fit("中文 output", 5); got != "中文 " {
		t.Fatal(got)
	}
	if got := fit("e\u0301 test", 2); got != "e\u0301 " {
		t.Fatal(got)
	}
	var out bytes.Buffer
	if err := Panel(&out, []string{"WebUI: http://localhost:5300", "lbctl status"}); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(out.String(), "\x1b╭") {
		t.Fatal(out.String())
	}
}

func TestMachineSuppressesBrand(t *testing.T) {
	var out bytes.Buffer
	display := New(&out, func(string) (string, bool) { return "", false }, true, false)
	display.Info(BrandLines())
	display.Finish(true)
	if out.Len() != 0 {
		t.Fatal(out.String())
	}
}

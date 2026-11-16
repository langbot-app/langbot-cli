package progress

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/langbot-app/langbot-cli/internal/i18n"
)

func TestLabelsAlignDisplayColumns(t *testing.T) {
	for _, fields := range [][][2]string{
		{{"WebUI", "VALUE"}, {"Instance", "VALUE"}, {"Directory", "VALUE"}, {"Log", "VALUE"}},
		{{"WebUI", "VALUE"}, {"实例", "VALUE"}, {"目录", "VALUE"}, {"日志", "VALUE"}},
	} {
		column := -1
		for _, line := range Labels(fields) {
			width := Width(strings.Split(line, "VALUE")[0])
			if column >= 0 && column != width {
				t.Fatal(line, width, column)
			}
			column = width
		}
	}
}

func TestBrandKeepsOpenSourceTogether(t *testing.T) {
	defer i18n.Use(i18n.English)()
	for _, width := range []int{56, 60, 70, 76, 96, 0} {
		lines := wrapBrand(BrandLines()[0], width)
		if !strings.Contains(strings.Join(lines, "\n"), "· Open source") {
			t.Fatal(lines)
		}
		for _, line := range lines {
			if width > 0 && Width(line) > width {
				t.Fatal(width, line)
			}
		}
	}
}

func TestWorkingProgressLeavesNoCompletedLine(t *testing.T) {
	for _, lang := range []string{i18n.English, i18n.Chinese} {
		for _, success := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/%t", lang, success), func(t *testing.T) {
				defer i18n.Use(lang)()
				var out bytes.Buffer
				display := New(&out, func(string) (string, bool) { return "", false }, false, false)
				display.Step("Working")
				display.mu.Lock()
				display.created = time.Now().Add(-time.Second)
				display.animated, display.painted = true, true
				display.mu.Unlock()
				display.Finish(success)
				if out.String() != "\r\x1b[2K\x1b[1A\r\x1b[2K" {
					t.Fatal(out.String())
				}
			})
		}
	}
}

func TestWrapPreservesASCIIWordsAndUnredirectedLines(t *testing.T) {
	lines := wrap("中文中文Discord、Slack平台", 12)
	if !strings.Contains(strings.Join(lines, "\n"), "Discord") {
		t.Fatal(lines)
	}
	for _, line := range lines {
		if Width(line) > 12 {
			t.Fatal(line)
		}
	}
	var out bytes.Buffer
	text := strings.Repeat("中文 Discord ", 20)
	Dim(&out, text)
	if out.String() != "  "+text+"\n" {
		t.Fatal("redirected text was wrapped", out.String())
	}
	out.Reset()
	Brand(&out)
	if strings.Count(out.String(), "\n") != 4 {
		t.Fatal("redirected brand was wrapped", out.String())
	}
}

func TestProgressFixedRegionAndFallback(t *testing.T) {
	for _, mode := range []string{"ansi", "carriage", "plain"} {
		t.Run(mode, func(t *testing.T) {
			var out bytes.Buffer
			display := New(&out, func(string) (string, bool) { return "", false }, false, false)
			display.mu.Lock()
			display.carriage, display.animated = mode != "plain", mode == "ansi"
			display.created = time.Now().Add(-time.Second)
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

func TestPanelDisplayWidths(t *testing.T) {
	for _, line := range wrap("Terms: https://langbot.app/terms LANGBOT_TELEMETRY=0 or DO_NOT_TRACK=1", 45) {
		if Width(line) > 45 || strings.Contains(line, "https://langbot.app/ter") && !strings.Contains(line, "https://langbot.app/terms") {
			t.Fatal(line)
		}
	}
	for _, columns := range []int{60, 80, 100} {
		var out bytes.Buffer
		if err := renderPanel(&out, []string{"LangBot 已就绪", "中文 · English → ✓", "全角：标点！🙂", strings.Repeat("混排 abc · ", 20)}, columns, true); err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
			if width := Width(line); width > min(columns-2, 76) {
				t.Fatalf("columns=%d width=%d: %q", columns, width, line)
			}
			if strings.ContainsAny(line, "╮╯") || strings.HasSuffix(strings.TrimRight(line, "\x1b[0m"), "│") {
				t.Fatalf("right border must stay open: %q", line)
			}
		}
	}
	if Width("中文·→✓！🙂") != 11 {
		t.Fatal("incorrect mixed character width")
	}
	var narrow bytes.Buffer
	renderPanel(&narrow, []string{"Title", "content"}, 50, true)
	if strings.ContainsAny(narrow.String(), "╭│\x1b") {
		t.Fatal(narrow.String())
	}
}

func TestInstantCommandsStaySilent(t *testing.T) {
	for _, carriage := range []bool{false, true} {
		var out bytes.Buffer
		display := New(&out, func(string) (string, bool) { return "", false }, false, false)
		display.mu.Lock()
		display.carriage, display.animated = carriage, carriage
		display.mu.Unlock()
		display.Step("Working")
		display.Step("Resolve version")
		display.Finish(true)
		if out.Len() != 0 {
			t.Fatalf("instant progress: %q", out.String())
		}
	}
}

func TestStepTotalsAlignmentAndDuplicates(t *testing.T) {
	var out bytes.Buffer
	display := New(&out, func(string) (string, bool) { return "", false }, false, false)
	display.Step("Working")
	display.Step("Register deployment")
	display.Step("Check ports")
	display.Step("Register deployment")
	display.mu.Lock()
	display.total = 12
	display.created = time.Now().Add(-time.Second)
	active := display.line("⠹", true)
	display.mu.Unlock()
	display.Finish(true)
	if !strings.Contains(active, "[2/12]") || Width(active) != 76 || strings.Count(out.String(), "Register deployment") != 1 || strings.Contains(out.String(), "Working") || strings.Contains(out.String(), "...") {
		t.Fatal(active, out.String())
	}
}

func TestNativeInstallUsesSixVisiblePhases(t *testing.T) {
	var out bytes.Buffer
	display := New(&out, func(string) (string, bool) { return "", false }, false, false)
	ctx := WithContext(context.Background(), display)
	NativeInstallPlan(ctx)
	display.mu.Lock()
	display.created = time.Now().Add(-time.Second)
	display.mu.Unlock()
	for _, step := range []string{"resolve_version", "validate_target", "register", "check_ports", "register", "write_record", "prepare_logs", "download_uv", "install_python", "create_venv", "install_package", "install_service", "start_service", "health_check"} {
		StepID(ctx, step)
	}
	display.Finish(true)
	if strings.Count(out.String(), "✓") != 6 || display.number != 6 || display.total != 6 {
		t.Fatal(out.String())
	}
	if got := byteSize(21699161); got != "20.7 MiB" {
		t.Fatal(got)
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

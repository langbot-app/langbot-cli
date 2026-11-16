package progress

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestPlainProgressAndFailureTail(t *testing.T) {
	for _, machine := range []bool{false, true} {
		var out bytes.Buffer
		display := New(&out, func(key string) (string, bool) { return "", false }, machine, false)
		display.Step("Install Python 3.12")
		display.Write([]byte("\x1b[31mfirst line\x1b[0m\nlast line\n"))
		if strings.Contains(out.String(), "first line") {
			t.Fatal("subprocess output was not folded")
		}
		display.Finish(false)
		text := out.String()
		if strings.ContainsAny(text, "\x1b\r") || !strings.Contains(text, "first line") || !strings.Contains(text, "last line") {
			t.Fatal(text)
		}
		display.Finish(false)
		if out.String() != text {
			t.Fatal("finish is not idempotent")
		}
	}
}

func TestVerboseAndDownload(t *testing.T) {
	var out bytes.Buffer
	display := New(&out, func(key string) (string, bool) { return "dumb", true }, false, true)
	display.Write([]byte("visible\n"))
	ctx := WithContext(context.Background(), display)
	var downloaded bytes.Buffer
	downloaded.ReadFrom(Download(ctx, strings.NewReader("download"), 8))
	display.Finish(true)
	if !strings.Contains(out.String(), "visible") || !strings.Contains(out.String(), "100%") {
		t.Fatal(out.String())
	}
}

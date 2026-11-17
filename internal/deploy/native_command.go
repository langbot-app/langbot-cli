package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf16"

	"github.com/langbot-app/langbot-cli/internal/diagnostic"
	"github.com/langbot-app/langbot-cli/internal/i18n"
	"github.com/langbot-app/langbot-cli/internal/telemetry"
)

var ErrNativeInterrupted = errors.New("native command interrupted")
var ErrNativeDependency = errors.New("native dependency failed")

type commandTail struct{ data []byte }

type commandWriter struct {
	mu  sync.Mutex
	out io.Writer
}

func (writer *commandWriter) Write(data []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.out.Write(data)
}

func (tail *commandTail) Write(data []byte) (int, error) {
	count := len(data)
	if len(data) >= 65536 {
		tail.data = append(tail.data[:0], data[len(data)-65536:]...)
		return count, nil
	}
	tail.data = append(tail.data, data...)
	if len(tail.data) > 65536 {
		tail.data = tail.data[len(tail.data)-65536:]
	}
	return count, nil
}

func runNativeCommand(ctx context.Context, command *exec.Cmd, out io.Writer) (string, error) {
	var stdout, stderr commandTail
	if out == nil {
		out = io.Discard
	}
	writer := &commandWriter{out: out}
	command.Stdout = io.MultiWriter(writer, &stdout)
	command.Stderr = io.MultiWriter(writer, &stderr)
	configureCommandCancellation(command)
	err := command.Run()
	output := string(stderr.data)
	if strings.TrimSpace(output) == "" {
		output = string(stdout.data)
	}
	output = diagnostic.Tail(output, 0, 2048)
	if err != nil {
		exitCode := -1
		if command.ProcessState != nil {
			exitCode = command.ProcessState.ExitCode()
		}
		telemetry.RecordCommand(ctx, command.Args, exitCode, string(stdout.data), string(stderr.data))
		if command.ProcessState != nil {
			if state, ok := command.ProcessState.Sys().(interface{ Signaled() bool }); ok && state.Signaled() {
				err = fmt.Errorf("%w: %w", ErrNativeInterrupted, err)
			}
		}
		return output, fmt.Errorf("%s: %w: %s", filepath.Base(command.Path), err, strings.TrimSpace(output))
	}
	return output, nil
}

type nativeRunKey interface {
	SetStringValue(string, string) error
	DeleteValue(string) error
}

func writeNativeRun(key nativeRunKey, record Record, binary, dir string) error {
	command := `"` + binary + `" _native-run "` + dir + strings.Repeat(`\`, len(dir)-len(strings.TrimRight(dir, `\`))) + `"`
	if len(utf16.Encode([]rune(command))) > 260 {
		return errors.New(i18n.T("HKCU Run command exceeds 260 characters; use shorter lbctl and deployment paths"))
	}
	return key.SetStringValue(nativeServiceName(record), command)
}

func deleteNativeRun(key nativeRunKey, record Record) error {
	err := key.DeleteValue(nativeServiceName(record))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

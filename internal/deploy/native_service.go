package deploy

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
)

func NativeRunning(dir string) bool {
	lock := flock.New(filepath.Join(dir, "native.run.lock"))
	if locked, err := lock.TryLock(); err == nil && locked {
		_ = lock.Unlock()
		return false
	}
	data, err := os.ReadFile(filepath.Join(dir, "native.pid"))
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	return err == nil && nativePIDAlive(pid)
}

func SelectNativeManager(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if runtime.GOOS == "windows" {
		if err := exec.CommandContext(ctx, "schtasks.exe", "/Query").Run(); err == nil {
			return "schtasks"
		}
	}
	if runtime.GOOS == "darwin" {
		if current, err := user.Current(); err == nil && exec.CommandContext(ctx, "launchctl", "print", "gui/"+current.Uid).Run() == nil {
			return "launchd"
		}
	}
	if runtime.GOOS == "linux" {
		args := []string{"show-environment"}
		if !currentUserIsRoot() {
			args = append([]string{"--user"}, args...)
		}
		if exec.CommandContext(ctx, "systemctl", args...).Run() == nil {
			if currentUserIsRoot() {
				return "systemd"
			}
			return "systemd-user"
		}
	}
	return "process"
}

func nativeServiceName(record Record) string { return "langbot-" + record.DeploymentID }

func nativeServicePath(record Record) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch record.ServiceManager {
	case "systemd":
		return filepath.Join("/etc/systemd/system", nativeServiceName(record)+".service"), nil
	case "systemd-user":
		base, err := os.UserConfigDir()
		return filepath.Join(base, "systemd/user", nativeServiceName(record)+".service"), err
	case "launchd":
		return filepath.Join(home, "Library/LaunchAgents", nativeServiceName(record)+".plist"), nil
	}
	return "", nil
}

func xmlText(value string) string {
	var text strings.Builder
	_ = xml.EscapeText(&text, []byte(value))
	return text.String()
}

func nativeExecutable() (string, error) {
	binary, err := os.Executable()
	if err != nil {
		return "", err
	}
	if candidate, err := exec.LookPath("lbctl"); err == nil {
		currentInfo, currentErr := os.Stat(binary)
		candidateInfo, candidateErr := os.Stat(candidate)
		if currentErr == nil && candidateErr == nil && os.SameFile(currentInfo, candidateInfo) {
			return filepath.Abs(candidate)
		}
	}
	return binary, nil
}

func InstallNativeService(ctx context.Context, dir string, record Record) error {
	binary, err := nativeExecutable()
	if err != nil {
		return err
	}
	path, err := nativeServicePath(record)
	if err != nil {
		return err
	}
	var content string
	switch record.ServiceManager {
	case "systemd", "systemd-user":
		quote := func(value string) string { return strconv.Quote(strings.ReplaceAll(value, "%", "%%")) }
		target := "default.target"
		if record.ServiceManager == "systemd" {
			target = "multi-user.target"
		}
		content = "[Unit]\nDescription=LangBot\nAfter=network-online.target\n[Service]\nType=simple\nExecStart=" + quote(binary) + " _native-run " + quote(dir) + "\nWorkingDirectory=" + quote(dir) + "\nRestart=on-failure\nRestartSec=5\nKillMode=control-group\nTimeoutStopSec=30\n[Install]\nWantedBy=" + target + "\n"
	case "launchd":
		content = `<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd"><plist version="1.0"><dict><key>Label</key><string>` + xmlText(nativeServiceName(record)) + `</string><key>ProgramArguments</key><array><string>` + xmlText(binary) + `</string><string>_native-run</string><string>` + xmlText(dir) + `</string></array><key>RunAtLoad</key><true/><key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict></dict></plist>`
	case "schtasks":
		return exec.CommandContext(ctx, "schtasks.exe", "/Create", "/F", "/SC", "ONLOGON", "/TN", nativeServiceName(record), "/TR", `"`+binary+`" _native-run "`+dir+`"`).Run()
	case "process":
		return nil
	default:
		return fmt.Errorf("unknown native service manager")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := writeAtomic(path, []byte(content), 0o600); err != nil {
		return err
	}
	if strings.HasPrefix(record.ServiceManager, "systemd") {
		if err := nativeSystemctl(ctx, record, "daemon-reload"); err != nil {
			return err
		}
		return nativeSystemctl(ctx, record, "enable", nativeServiceName(record))
	}
	return nil
}

func nativeSystemctl(ctx context.Context, record Record, args ...string) error {
	if record.ServiceManager == "systemd-user" {
		args = append([]string{"--user"}, args...)
	}
	output, err := exec.CommandContext(ctx, "systemctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl: %s: %w", strings.TrimSpace(string(output)), err)
	}
	return nil
}

func NativeStart(ctx context.Context, dir string, record Record) error {
	if NativeRunning(dir) {
		return nil
	}
	if err := CheckNativePorts(record); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(dir, "native.stop")); err != nil && !os.IsNotExist(err) {
		return err
	}
	var err error
	switch record.ServiceManager {
	case "systemd", "systemd-user":
		err = nativeSystemctl(ctx, record, "start", nativeServiceName(record))
	case "launchd":
		path, pathErr := nativeServicePath(record)
		if pathErr != nil {
			return pathErr
		}
		err = exec.CommandContext(ctx, "launchctl", "load", path).Run()
	case "schtasks":
		err = exec.CommandContext(ctx, "schtasks.exe", "/Run", "/TN", nativeServiceName(record)).Run()
	case "process":
		binary, binaryErr := os.Executable()
		if binaryErr != nil {
			return binaryErr
		}
		command := exec.Command(binary, "_native-run", dir)
		detachNative(command)
		if err = command.Start(); err == nil {
			err = command.Process.Release()
		}
	default:
		return fmt.Errorf("unknown native service manager")
	}
	if err != nil {
		return err
	}
	ready := time.NewTimer(10 * time.Second)
	defer ready.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for !NativeRunning(dir) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ready.C:
			return fmt.Errorf("native supervisor did not start; inspect %s", filepath.Join(dir, "logs", "langbot.log"))
		case <-ticker.C:
		}
	}
	return nil
}

func NativeStop(ctx context.Context, dir string, record Record) error {
	switch record.ServiceManager {
	case "systemd", "systemd-user":
		if err := nativeSystemctl(ctx, record, "stop", nativeServiceName(record)); err != nil {
			return err
		}
	case "launchd":
		path, err := nativeServicePath(record)
		if err != nil {
			return err
		}
		if NativeRunning(dir) {
			if err := exec.CommandContext(ctx, "launchctl", "unload", path).Run(); err != nil {
				return err
			}
		}
	default:
		if !NativeRunning(dir) {
			return nil
		}
		if err := writeAtomic(filepath.Join(dir, "native.stop"), []byte("stop"), 0o600); err != nil {
			return err
		}
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for NativeRunning(dir) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
	return nil
}

func RemoveNativeService(ctx context.Context, record Record) error {
	if strings.HasPrefix(record.ServiceManager, "systemd") {
		if err := nativeSystemctl(ctx, record, "disable", nativeServiceName(record)); err != nil {
			return err
		}
	}
	if record.ServiceManager == "schtasks" {
		return exec.CommandContext(ctx, "schtasks.exe", "/Delete", "/F", "/TN", nativeServiceName(record)).Run()
	}
	path, err := nativeServicePath(record)
	if err != nil {
		return err
	}
	if path != "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if strings.HasPrefix(record.ServiceManager, "systemd") {
		return nativeSystemctl(ctx, record, "daemon-reload")
	}
	return nil
}

type rotatingLog struct {
	mu   sync.Mutex
	path string
	file *os.File
	size int64
}

func (log *rotatingLog) Write(data []byte) (int, error) {
	log.mu.Lock()
	defer log.mu.Unlock()
	if log.file == nil || log.size+int64(len(data)) > 10<<20 {
		if log.file != nil {
			if err := log.file.Close(); err != nil {
				return 0, err
			}
			_ = os.Remove(log.path + ".3")
			_ = os.Rename(log.path+".2", log.path+".3")
			_ = os.Rename(log.path+".1", log.path+".2")
			if err := os.Rename(log.path, log.path+".1"); err != nil {
				return 0, err
			}
		}
		var err error
		log.file, err = os.OpenFile(log.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return 0, err
		}
		info, err := log.file.Stat()
		if err != nil {
			return 0, err
		}
		log.size = info.Size()
	}
	count, err := log.file.Write(data)
	log.size += int64(count)
	return count, err
}

func NativeServe(ctx context.Context, dir string) error {
	record, err := (Store{Dir: dir}).Read()
	if err != nil {
		return err
	}
	if record.Driver != DriverNative {
		return fmt.Errorf("not a native deployment")
	}
	lock := flock.New(filepath.Join(dir, "native.run.lock"))
	locked, err := lock.TryLock()
	if err != nil {
		return err
	}
	if !locked {
		return fmt.Errorf("native instance already running")
	}
	defer lock.Unlock()
	if err := CheckNativePorts(record); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(dir, "native.stop")); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o700); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	log := &rotatingLog{path: filepath.Join(dir, "logs", "langbot.log")}
	defer func() {
		if log.file != nil {
			_ = log.file.Close()
		}
	}()
	command := exec.CommandContext(ctx, NativePython(dir, record.CoreVersion), "-m", "langbot")
	command.Dir, command.Env, command.Stdout, command.Stderr = dir, NativeEnvironment(record), log, log
	configureNativeChild(command)
	if err := command.Start(); err != nil {
		fmt.Fprintln(log, err)
		return err
	}
	pidPath := filepath.Join(dir, "native.pid")
	if err := writeAtomic(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		cancel()
		_ = command.Wait()
		return err
	}
	defer os.Remove(pidPath)
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			cancel()
			<-done
			return nil
		case <-ticker.C:
			if _, err := os.Stat(filepath.Join(dir, "native.stop")); err == nil {
				cancel()
				<-done
				return nil
			}
		}
	}
}

func NativeLogs(ctx context.Context, dir string, out io.Writer, tail int, follow bool) error {
	path := filepath.Join(dir, "logs", "langbot.log")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		data, err = nil, nil
	}
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) > tail {
		lines = lines[len(lines)-tail:]
	}
	if len(data) > 0 {
		if _, err := fmt.Fprintln(out, strings.Join(lines, "\n")); err != nil {
			return err
		}
	}
	if !follow {
		return nil
	}
	offset := int64(len(data))
	info, _ := os.Stat(path)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		file, err := os.Open(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		current, err := file.Stat()
		if err != nil {
			file.Close()
			return err
		}
		if info == nil || !os.SameFile(info, current) || current.Size() < offset {
			offset = 0
		}
		info = current
		_, err = file.Seek(offset, io.SeekStart)
		if err == nil {
			var count int64
			count, err = io.Copy(out, file)
			offset += count
		}
		file.Close()
		if err != nil {
			return err
		}
	}
}

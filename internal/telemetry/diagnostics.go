package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
	"github.com/langbot-app/langbot-cli/internal/diagnostic"
)

func SetDirectory(ctx context.Context, dir string) {
	if op, ok := ctx.Value(operationKey{}).(*Operation); ok {
		op.dir = dir
	}
}

func SetTools(ctx context.Context, uv, python string) {
	if op, ok := ctx.Value(operationKey{}).(*Operation); ok {
		op.uv, op.python = uv, python
	}
}

func RecordCommand(ctx context.Context, args []string, exitCode int, stdout, stderr string) {
	if private, _ := ctx.Value(privateCommandKey{}).(bool); private {
		return
	}
	if op, ok := ctx.Value(operationKey{}).(*Operation); ok {
		op.process = map[string]any{"command": filepath.Base(args[0]), "args": diagnostic.Args(args[1:]),
			"exit_code": exitCode, "stdout": diagnostic.Tail(diagnostic.Text(stdout), 60, diagnostic.FieldLimit),
			"stderr": diagnostic.Tail(diagnostic.Text(stderr), 60, diagnostic.FieldLimit)}
		op.processStep = op.Event.Step
	}
}

type privateCommandKey struct{}

func PrivateCommand(ctx context.Context) context.Context {
	return context.WithValue(ctx, privateCommandKey{}, true)
}

func RecordSource(ctx context.Context, address string) {
	op, ok := ctx.Value(operationKey{}).(*Operation)
	if !ok || !downloadStep(op.Event.Step) {
		return
	}
	parsed, err := url.Parse(address)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return
	}
	parsed.User, parsed.RawQuery, parsed.Fragment = nil, "", ""
	address = parsed.String()
	op.sourceMu.Lock()
	defer op.sourceMu.Unlock()
	if op.sources == nil {
		op.sources = make(map[string][]string)
	}
	for _, previous := range op.sources[op.Event.Step] {
		if previous == address {
			return
		}
	}
	op.sources[op.Event.Step] = append(op.sources[op.Event.Step], address)
}

func downloadStep(step string) bool {
	switch step {
	case "download_uv", "install_python", "install_package", "pull_image":
		return true
	}
	return false
}

func (op *Operation) diagnostics() map[string]any {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	details := map[string]any{}
	if op.processStep == op.Event.Step && op.process != nil {
		details["process"] = op.process
	}
	if op.dir != "" {
		name := "install.log"
		if op.Event.Step == "health_check" || op.Event.Step == "start_service" {
			name = "langbot.log"
		}
		if file, err := os.Open(filepath.Join(op.dir, "logs", name)); err == nil {
			defer file.Close()
			if stat, err := file.Stat(); err == nil {
				offset := max(int64(0), stat.Size()-64*1024)
				_, _ = file.Seek(offset, io.SeekStart)
				data, _ := io.ReadAll(io.LimitReader(file, 64*1024))
				if offset > 0 {
					_, tail, found := strings.Cut(string(data), "\n")
					if found {
						data = []byte(tail)
					}
				}
				text := string(data)
				if key, err := os.ReadFile(filepath.Join(op.dir, ".api-key")); err == nil && len(key) > 0 {
					text = strings.ReplaceAll(text, string(key), "[redacted]")
				}
				details["log"] = map[string]any{"file": name, "tail": diagnostic.Tail(diagnostic.Text(text), 80, diagnostic.FieldLimit)}
			}
		}
	}
	var wait sync.WaitGroup
	var environment map[string]any
	var network []any
	wait.Add(2)
	go func() { defer wait.Done(); environment = op.environment(ctx) }()
	go func() { defer wait.Done(); network = probeSources(ctx, op.sources[op.Event.Step]) }()
	wait.Wait()
	details["environment"] = environment
	if len(network) > 0 {
		details["network"] = network
	}
	return diagnostic.Object(op.cleanIdentity(details).(map[string]any))
}

func (op *Operation) cleanIdentity(value any) any {
	switch typed := value.(type) {
	case string:
		return sanitizeIdentity(typed, op.client.home, op.usernames())
	case map[string]any:
		for key, item := range typed {
			typed[key] = op.cleanIdentity(item)
		}
	case []any:
		for index, item := range typed {
			typed[index] = op.cleanIdentity(item)
		}
	case []string:
		for index, item := range typed {
			typed[index] = sanitizeIdentity(item, op.client.home, op.usernames())
		}
	}
	return value
}

func (op *Operation) usernames() []string {
	var names []string
	for _, key := range []string{"USER", "USERNAME", "LOGNAME"} {
		if value, _ := op.client.lookup(key); value != "" {
			names = append(names, value)
		}
	}
	return names
}

func probeSources(ctx context.Context, sources []string) []any {
	results := make([]any, len(sources))
	var wait sync.WaitGroup
	for index, address := range sources {
		wait.Add(1)
		go func() {
			defer wait.Done()
			started := time.Now()
			probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			result := map[string]any{"source": diagnostic.Text(address)}
			request, err := http.NewRequestWithContext(probeCtx, http.MethodHead, address, nil)
			if err == nil {
				client := &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
				var response *http.Response
				response, err = client.Do(request)
				if response != nil {
					result["status_code"] = response.StatusCode
					response.Body.Close()
				}
			}
			if err != nil {
				kind := "connection"
				var dns *net.DNSError
				var network net.Error
				if errors.As(err, &dns) {
					kind = "dns"
				} else if errors.As(err, &network) && network.Timeout() {
					kind = "timeout"
				} else if strings.Contains(err.Error(), "tls") || strings.Contains(err.Error(), "x509") {
					kind = "tls"
				}
				result["error_type"] = kind
			}
			result["duration_ms"] = time.Since(started).Milliseconds()
			results[index] = result
		}()
	}
	wait.Wait()
	return results
}

func (op *Operation) environment(parent context.Context) map[string]any {
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	dir := op.dir
	if dir == "" {
		dir = op.client.home
	}
	for {
		if _, err := os.Stat(dir); err == nil || filepath.Dir(dir) == dir {
			break
		}
		dir = filepath.Dir(dir)
	}
	result := systemEnvironment(dir)
	result["cpu_count"] = runtime.NumCPU()
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY"} {
		upper, _ := op.client.lookup(key)
		lower, _ := op.client.lookup(strings.ToLower(key))
		result[strings.ToLower(key)+"_set"] = upper != "" || lower != ""
	}
	uv, python := op.uv, op.python
	if python == "" && op.dir != "" && op.Event.CoreVersion != "" {
		python = filepath.Join(op.dir, "venvs", op.Event.CoreVersion, "bin", "python")
		if runtime.GOOS == "windows" {
			python = filepath.Join(op.dir, "venvs", op.Event.CoreVersion, "Scripts", "python.exe")
		}
	}
	if _, err := exec.LookPath(uv); err != nil {
		uv = "uv"
	}
	if _, err := exec.LookPath(python); err != nil {
		python = "python3"
		if runtime.GOOS == "windows" {
			python = "python"
		}
	}
	commands := map[string][]string{"docker_version": {"docker", "--version"}, "compose_version": {"docker", "compose", "version", "--short"}, "uv_version": {uv, "--version"}, "python_version": {python, "--version"}}
	if runtime.GOOS == "windows" {
		commands["shell_version"] = []string{"powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "$PSVersionTable.PSVersion.ToString()"}
	} else if shell, _ := op.client.lookup("SHELL"); shell != "" {
		commands["shell_version"] = []string{shell, "--version"}
	}
	if runtime.GOOS == "darwin" {
		commands["os_version"] = []string{"sw_vers", "-productVersion"}
	}
	var mutex sync.Mutex
	var wait sync.WaitGroup
	for key, args := range commands {
		wait.Add(1)
		go func() {
			defer wait.Done()
			command := exec.CommandContext(ctx, args[0], args[1:]...)
			configureDiagnosticCommand(command)
			command.WaitDelay = 100 * time.Millisecond
			if output, err := command.Output(); err == nil {
				mutex.Lock()
				result[key] = diagnostic.Tail(diagnostic.Text(strings.TrimSpace(string(output))), 3, 512)
				mutex.Unlock()
			}
		}()
	}
	wait.Wait()
	return result
}

func (client *Client) failures(command string, increment bool) int {
	dir := filepath.Join(client.home, ".langbot")
	lock := flock.New(filepath.Join(dir, "telemetry-failures.lock"))
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	locked, err := lock.TryLockContext(ctx, 10*time.Millisecond)
	if err != nil || !locked {
		return 0
	}
	defer lock.Unlock()
	path := filepath.Join(dir, "telemetry-failures.json")
	counts := map[string]int{}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &counts)
	}
	count := counts[command]
	if increment {
		counts[command] = count + 1
		data, _ := json.Marshal(counts)
		_ = os.WriteFile(path, data, 0o600)
	}
	return count
}

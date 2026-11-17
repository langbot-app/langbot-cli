package telemetry

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
	"github.com/langbot-app/langbot-cli/internal/diagnostic"
	"github.com/langbot-app/langbot-cli/internal/progress"
)

const DefaultEndpoint = "https://space.langbot.app/api/v1/telemetry"

type Event struct {
	AttemptID        string         `json:"attempt_id,omitempty"`
	PreviousFailures int            `json:"previous_failures"`
	Details          map[string]any `json:"details,omitempty"`
	EventType        string         `json:"event_type"`
	EventID          string         `json:"event_id"`
	InstanceID       string         `json:"instance_id"`
	Command          string         `json:"command"`
	Outcome          string         `json:"outcome"`
	Step             string         `json:"step"`
	ErrorCode        string         `json:"error_code"`
	Error            string         `json:"error"`
	DurationMs       int64          `json:"duration_ms"`
	LBCTLVersion     string         `json:"lbctl_version"`
	OS               string         `json:"os"`
	Arch             string         `json:"arch"`
	Driver           string         `json:"driver"`
	ServiceManager   string         `json:"service_manager"`
	Region           string         `json:"region"`
	CoreVersion      string         `json:"core_version"`
	Locale           string         `json:"locale"`
	Source           string         `json:"source"`
}

type Options struct {
	LookupEnv  func(string) (string, bool)
	Home       string
	Version    string
	Transport  http.RoundTripper
	OnFirstRun func()
}

type Client struct {
	lookup     func(string) (string, bool)
	home       string
	version    string
	transport  http.RoundTripper
	onFirstRun func()
	wait       sync.WaitGroup
	ctx        context.Context
	cancel     context.CancelFunc
}

func New(options Options) *Client {
	if options.LookupEnv == nil {
		options.LookupEnv = os.LookupEnv
	}
	if !Enabled(options.LookupEnv) {
		return nil
	}
	if options.Home == "" {
		options.Home, _ = os.UserHomeDir()
	}
	if options.Home == "" {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Client{lookup: options.LookupEnv, home: options.Home, version: options.Version, transport: options.Transport,
		onFirstRun: options.OnFirstRun, ctx: ctx, cancel: cancel}
}

func Enabled(lookup func(string) (string, bool)) bool {
	telemetry, _ := lookup("LANGBOT_TELEMETRY")
	dnt, _ := lookup("DO_NOT_TRACK")
	return telemetry != "0" && dnt != "1"
}

func newID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return ""
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", value[:4], value[4:6], value[6:8], value[8:10], value[10:])
}

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
var identifierPattern = regexp.MustCompile(`^[a-zA-Z0-9_.+\-]{0,80}$`)

func Identifier(value string) string {
	if identifierPattern.MatchString(value) {
		return value
	}
	return ""
}

func (client *Client) identity() (string, bool) {
	dir := filepath.Join(client.home, ".langbot")
	if os.MkdirAll(dir, 0o700) != nil {
		return "", false
	}
	lock := flock.New(filepath.Join(dir, "telemetry.lock"))
	locked, err := lock.TryLock()
	if err != nil || !locked {
		return "", false
	}
	defer lock.Unlock()
	path := filepath.Join(dir, "telemetry-id")
	if data, err := os.ReadFile(path); err == nil && uuidPattern.MatchString(strings.TrimSpace(string(data))) {
		return strings.TrimSpace(string(data)), false
	}
	identity := newID()
	if identity == "" || os.WriteFile(path, []byte(identity+"\n"), 0o600) != nil {
		return "", false
	}
	return identity, true
}

func (client *Client) send(event Event) {
	if client == nil {
		return
	}
	event.EventID = newID()
	if event.EventID == "" {
		return
	}
	details := event.Details
	event.Details = nil
	base, _ := json.Marshal(event)
	for len(base) > diagnostic.EventLimit-1024 && event.Error != "" {
		event.Error = diagnostic.Tail(event.Error, 0, len(event.Error)/2)
		base, _ = json.Marshal(event)
	}
	diagnostic.Fit(details, diagnostic.EventLimit-len(base)-32)
	event.Details = details
	client.wait.Add(1)
	go func() {
		defer client.wait.Done()
		ctx, cancel := context.WithTimeout(client.ctx, 2*time.Second)
		defer cancel()
		endpoint, _ := client.lookup("LBCTL_TELEMETRY_ENDPOINT")
		if endpoint == "" {
			endpoint = DefaultEndpoint
		}
		body, err := json.Marshal(event)
		if err != nil {
			return
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return
		}
		request.Header.Set("Content-Type", "application/json")
		httpClient := &http.Client{Timeout: 2 * time.Second, Transport: client.transport,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		if response, err := httpClient.Do(request); err == nil {
			response.Body.Close()
		}
	}()
}

func (client *Client) Close() {
	if client == nil {
		return
	}
	defer client.cancel()
	done := make(chan struct{})
	go func() { client.wait.Wait(); close(done) }()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}
}

type Operation struct {
	Event           Event
	started         time.Time
	client          *Client
	dir, uv, python string
	processStep     string
	process         map[string]any
	sources         map[string][]string
	sourceMu        sync.Mutex
}

type operationKey struct{}

func (client *Client) Begin(ctx context.Context, command string) (context.Context, *Operation) {
	if client == nil {
		return ctx, nil
	}
	identity, first := client.identity()
	if identity == "" {
		return ctx, nil
	}
	if first && client.onFirstRun != nil {
		client.onFirstRun()
	}
	source, _ := client.lookup("LBCTL_INSTALL_SOURCE")
	if source != "install.sh" && source != "install.ps1" {
		source = "manual"
	}
	locale := ""
	for _, key := range []string{"LBCTL_LANG", "LC_ALL", "LC_MESSAGES", "LANG"} {
		if value, _ := client.lookup(key); value != "" {
			locale = Identifier(value)
			break
		}
	}
	version := Identifier(client.version)
	if version == "" {
		version = "unknown"
	}
	op := &Operation{client: client, started: time.Now(), Event: Event{EventType: "cli_operation", InstanceID: identity,
		AttemptID: newID(), PreviousFailures: client.failures(command, false),
		Command: command, Outcome: "started", Step: "validation", LBCTLVersion: version, OS: runtime.GOOS, Arch: runtime.GOARCH,
		Source: source, Locale: locale}}
	client.send(op.Event)
	return context.WithValue(ctx, operationKey{}, op), op
}

func SetStep(ctx context.Context, step string) {
	step = Identifier(step)
	if current, ok := ctx.Value(stepKey{}).(*string); ok {
		*current = step
	}
	progress.StepID(ctx, step)
	if op, ok := ctx.Value(operationKey{}).(*Operation); ok {
		op.Event.Step = Identifier(step)
	}
}

type stepKey struct{}

func TrackSteps(ctx context.Context) context.Context {
	return context.WithValue(ctx, stepKey{}, new(string))
}

func CurrentStep(ctx context.Context) string {
	if current, ok := ctx.Value(stepKey{}).(*string); ok {
		return *current
	}
	return ""
}

func SetEnvironment(ctx context.Context, driver, manager, region, coreVersion string) {
	if op, ok := ctx.Value(operationKey{}).(*Operation); ok {
		if driver == "docker_compose" {
			driver = "docker"
		}
		if driver != "native" && driver != "docker" {
			driver = ""
		}
		op.Event.Driver, op.Event.ServiceManager = driver, Identifier(manager)
		if region == "auto" || region == "cn" || region == "global" {
			op.Event.Region = region
		}
		op.Event.CoreVersion = Identifier(coreVersion)
	}
}

func ImagePullFailed(ctx context.Context) {
	op, ok := ctx.Value(operationKey{}).(*Operation)
	if !ok || op.client == nil {
		return
	}
	event := op.Event
	event.Command, event.Step, event.Outcome = "image_pull", "image_pull", "failed"
	event.ErrorCode, event.Error = "image_pull", "image_pull"
	event.DurationMs = time.Since(op.started).Milliseconds()
	event.PreviousFailures = op.client.failures("image_pull", true)
	if op.process != nil {
		event.Details = diagnostic.Object(map[string]any{"process": op.cleanIdentity(op.process)})
	}
	op.client.send(event)
}

func (op *Operation) Finish(err error, code string) {
	if op == nil {
		return
	}
	op.Event.DurationMs = time.Since(op.started).Milliseconds()
	op.Event.Outcome = "succeeded"
	if err != nil {
		op.Event.Outcome = "failed"
		if code == "interrupted" {
			op.Event.Outcome = "interrupted"
		}
		op.Event.ErrorCode = Identifier(code)
		if op.Event.ErrorCode == "" {
			op.Event.ErrorCode = "unknown"
		}
		op.Event.Error = SanitizeError(sanitizeIdentity(err.Error(), op.client.home, op.usernames()))
		op.Event.Details = op.diagnostics()
		op.client.failures(op.Event.Command, true)
	}
	op.client.send(op.Event)
}

package command

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/langbot-app/langbot-cli/internal/config"
)

func memberCLI(t *testing.T, path, input string, environment map[string]string, arguments ...string) (int, string) {
	t.Helper()
	var output bytes.Buffer
	code := executeChinese(context.Background(), append([]string{"--config", path, "-o", "json"}, arguments...), Dependencies{
		In: strings.NewReader(input), Out: &output, Err: &output, Version: "test",
		LookupEnv: func(name string) (string, bool) { value, ok := environment[name]; return value, ok },
	})
	return code, output.String()
}

func codexLoginCLI(t *testing.T, ctx context.Context, path, format string, transport http.RoundTripper, arguments ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	args := []string{"--config", path}
	if format != "" {
		args = append(args, "-o", format)
	}
	args = append(args, "provider", "codex", "login", "provider-1")
	args = append(args, arguments...)
	code := executeChinese(ctx, args, Dependencies{
		In: strings.NewReader(""), Out: &stdout, Err: &stderr, Transport: transport, Version: "test",
		LookupEnv: func(name string) (string, bool) { return "api-private", name == "TEST_KEY" },
	})
	return code, stdout.String(), stderr.String()
}

func codexStartResponse(writer http.ResponseWriter, interval float64, expiry time.Time) {
	_ = json.NewEncoder(writer).Encode(map[string]any{"code": 0, "data": map[string]any{
		"authorization_id": "attempt-private", "user_code": "CODE-1234", "verification_uri": "https://auth.openai.com/codex/device",
		"interval": interval, "expires_at": float64(expiry.UnixNano()) / float64(time.Second),
		"access_token": "oauth-private", "refresh_token": "refresh-private", "device_auth_id": "device-private",
	}})
}

func TestCodexLoginSuccessProgressMemberAndReadback(t *testing.T) {
	for _, format := range []string{"json", "yaml", ""} {
		t.Run(format, func(t *testing.T) {
			starts, polls, statuses := 0, 0, 0
			lastPoll := time.Time{}
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.Header.Get("Authorization") != "Bearer member-private" || request.Header.Get("X-API-Key") != "" {
					t.Error("login must use the member session")
				}
				if request.Header.Get("X-LangBot-Client-Command") != "provider.codex.login" {
					t.Error("missing canonical login identity")
				}
				if operationPreflight(writer, request, true) {
					return
				}
				var body map[string]any
				_ = json.NewDecoder(request.Body).Decode(&body)
				if body["provider"] != "provider-1" {
					t.Error("provider argument changed")
				}
				switch request.URL.Path {
				case "/api/v1/operations/provider.codex.start":
					starts++
					codexStartResponse(writer, 0.005, time.Now().Add(time.Minute))
				case "/api/v1/operations/provider.codex.poll":
					polls++
					if body["authorization_id"] != "attempt-private" {
						t.Error("poll must retain the current attempt")
					}
					if !lastPoll.IsZero() && time.Since(lastPoll) < 8*time.Millisecond {
						t.Error("ignored server polling interval")
					}
					lastPoll = time.Now()
					if polls == 1 {
						fmt.Fprint(writer, `{"code":0,"data":{"status":"pending","interval":0.01,"access_token":"oauth-private"}}`)
					} else {
						fmt.Fprint(writer, `{"code":0,"data":{"status":"connected","access_token":"oauth-private"}}`)
					}
				case "/api/v1/operations/provider.codex.status":
					statuses++
					fmt.Fprint(writer, `{"code":0,"data":{"status":"connected","connected":true,"access_token":"oauth-private","refresh_token":"refresh-private","debug":"opaque-private"}}`)
				default:
					t.Errorf("unexpected operation %s", request.URL.Path)
					writer.WriteHeader(500)
				}
			}))
			defer server.Close()
			path := memberConfig(t, server.URL, "member-private")
			code, stdout, stderr := codexLoginCLI(t, context.Background(), path, format, nil, "--yes")
			if code != 0 || starts != 1 || polls != 2 || statuses != 1 {
				t.Fatalf("code=%d starts=%d polls=%d statuses=%d out=%s err=%s", code, starts, polls, statuses, stdout, stderr)
			}
			for _, secret := range []string{"member-private", "api-private", "attempt-private", "oauth-private", "refresh-private", "device-private", "opaque-private"} {
				if strings.Contains(stdout+stderr, secret) {
					t.Fatalf("login output exposed %q", secret)
				}
			}
			if format == "" {
				if !strings.Contains(stderr, "https://auth.openai.com/codex/device") || !strings.Contains(stderr, "CODE-1234") {
					t.Fatal("human device instructions missing")
				}
				return
			}
			frames := decodeOperationStream(t, stdout, format)
			if len(frames) != 2 {
				t.Fatalf("want initial and final structured frames, got %d", len(frames))
			}
			first := frames[0]["data"].(map[string]any)
			last := frames[1]["data"].(map[string]any)
			if first["user_code"] != "CODE-1234" || first["verification_uri"] != "https://auth.openai.com/codex/device" || last["connected"] != true || last["completed"] != true {
				t.Fatalf("unexpected frames %#v", frames)
			}
		})
	}
}

func TestCodexLoginDoesNotStartWithoutConsentOrMember(t *testing.T) {
	for _, test := range []struct{ token, argument string }{{"member-private", ""}, {"", "--yes"}} {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if !operationPreflight(writer, request, true) {
				t.Error("login started without consent/member")
			}
		}))
		path := memberConfig(t, server.URL, test.token)
		args := []string{}
		if test.argument != "" {
			args = append(args, test.argument)
		}
		code, _, _ := codexLoginCLI(t, context.Background(), path, "json", nil, args...)
		server.Close()
		if code == 0 {
			t.Fatal("login unexpectedly succeeded")
		}
	}
}

func TestCodexLoginFailuresDoNotRetryOrDisconnect(t *testing.T) {
	for _, test := range []struct {
		name, stage, reply string
		status, exit       int
	}{
		{"start conflict", "start", `{"code":"raw-private","msg":"oauth-private"}`, 409, 10},
		{"start unavailable", "start", `{"code":0,"access_token":"oauth-private"}`, 500, 7},
		{"expired attempt", "poll", `{"code":0,"data":{"status":"expired"}}`, 200, 6},
		{"authorization failure", "poll", `{"code":"raw-private","msg":"oauth-private"}`, 400, 10},
		{"poll conflict", "poll", `{"code":"raw-private","msg":"oauth-private"}`, 409, 10},
		{"poll unknown", "poll", `{"code":0}`, 500, 7},
		{"invalid poll", "poll", `{"code":0,"data":{"status":"unexpected","access_token":"oauth-private"}}`, 200, 7},
		{"readback mismatch", "status", `{"code":0,"data":{"status":"disconnected","connected":false}}`, 200, 7},
		{"readback failed", "status", `{"code":"raw-private","msg":"oauth-private"}`, 500, 7},
	} {
		t.Run(test.name, func(t *testing.T) {
			starts, polls := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if operationPreflight(writer, request, true) {
					return
				}
				stage := strings.TrimPrefix(request.URL.Path, "/api/v1/operations/provider.codex.")
				if stage == "start" {
					starts++
				}
				if stage == "poll" {
					polls++
				}
				if stage == "cancel" || stage == "disconnect" {
					t.Fatalf("unexpected %s", stage)
				}
				if stage == test.stage {
					writer.WriteHeader(test.status)
					fmt.Fprint(writer, test.reply)
					return
				}
				if stage == "start" {
					codexStartResponse(writer, 0.001, time.Now().Add(time.Minute))
					return
				}
				fmt.Fprint(writer, `{"code":0,"data":{"status":"connected"}}`)
			}))
			defer server.Close()
			path := memberConfig(t, server.URL, "member-private")
			code, stdout, stderr := codexLoginCLI(t, context.Background(), path, "json", nil, "--yes")
			if code != test.exit || starts != 1 || polls > 1 || strings.Contains(stdout+stderr, "private") {
				t.Fatalf("code=%d starts=%d polls=%d out=%s err=%s", code, starts, polls, stdout, stderr)
			}
		})
	}
}

func TestCodexLoginRateLimitAndExpiry(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(fmt.Sprint(expired), func(t *testing.T) {
			polls := 0
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if operationPreflight(writer, request, true) {
					return
				}
				switch request.URL.Path {
				case "/api/v1/operations/provider.codex.start":
					expiry := time.Now().Add(time.Minute)
					if expired {
						expiry = time.Now().Add(15 * time.Millisecond)
					}
					codexStartResponse(writer, 0.005, expiry)
				case "/api/v1/operations/provider.codex.poll":
					polls++
					if expired || polls == 1 {
						writer.Header().Set("Retry-After", "0")
						writer.WriteHeader(429)
						fmt.Fprint(writer, `{"code":"limited"}`)
					} else {
						fmt.Fprint(writer, `{"code":0,"data":{"status":"connected"}}`)
					}
				case "/api/v1/operations/provider.codex.status":
					fmt.Fprint(writer, `{"code":0,"data":{"status":"connected","connected":true}}`)
				default:
					t.Errorf("unexpected call %s", request.URL.Path)
				}
			}))
			defer server.Close()
			code, stdout, stderr := codexLoginCLI(t, context.Background(), memberConfig(t, server.URL, "member-private"), "json", nil, "--yes")
			if expired && (code != 6 || polls > 3) || !expired && (code != 0 || polls != 2) {
				t.Fatalf("expired=%t code=%d polls=%d out=%s err=%s", expired, code, polls, stdout, stderr)
			}
		})
	}
	now := time.Now().UTC().Truncate(time.Second)
	for _, test := range []struct {
		header string
		want   time.Duration
	}{{"10", 10 * time.Second}, {now.Add(20 * time.Second).Format(http.TimeFormat), 20 * time.Second}, {"0", time.Second}, {"invalid", 6 * time.Second}, {"92233720368547758070", time.Duration(math.MaxInt64)}} {
		if got := codexRetryDelay(test.header, time.Second, now); got != test.want {
			t.Fatalf("header=%q delay=%v want=%v", test.header, got, test.want)
		}
	}
}

func TestCodexLoginMalformedStartIsSafe(t *testing.T) {
	for index, test := range []struct {
		field string
		value any
	}{
		{"authorization_id", ""},
		{"verification_uri", "https://auth.openai.com/codex/device?access_token=oauth-private"},
		{"verification_uri", "javascript:oauth-private"},
		{"user_code", "CODE-\x1b[31m"},
		{"interval", 0},
		{"expires_at", "oauth-private"},
	} {
		t.Run(fmt.Sprintf("%s-%d", test.field, index), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if operationPreflight(writer, request, true) {
					return
				}
				if request.URL.Path != "/api/v1/operations/provider.codex.start" {
					t.Error("malformed authorization must not trigger another operation")
				}
				data := map[string]any{"authorization_id": "attempt-private", "verification_uri": "https://auth.openai.com/codex/device", "user_code": "CODE-1234", "interval": 1, "expires_at": time.Now().Add(time.Minute).Unix()}
				data[test.field] = test.value
				_ = json.NewEncoder(writer).Encode(map[string]any{"code": 0, "data": data})
			}))
			defer server.Close()
			code, stdout, stderr := codexLoginCLI(t, context.Background(), memberConfig(t, server.URL, "member-private"), "json", nil, "--yes")
			frames := decodeOperationStream(t, stdout, "json")
			if code != 7 || len(frames) != 1 || strings.Contains(stdout+stderr, "private") {
				t.Fatalf("code=%d out=%s err=%s", code, stdout, stderr)
			}
		})
	}
}

func TestCodexLoginCancellationHasIndependentBoundedDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if operationPreflight(writer, request, true) {
			return
		}
		if request.URL.Path != "/api/v1/operations/provider.codex.start" {
			t.Error("unexpected server operation")
		}
		codexStartResponse(writer, 0.001, time.Now().Add(time.Minute))
	}))
	defer server.Close()
	cancellations := 0
	transport := operationTransport(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/api/v1/operations/provider.codex.poll":
			cancel()
			return nil, context.Canceled
		case "/api/v1/operations/provider.codex.cancel":
			cancellations++
			deadline, exists := request.Context().Deadline()
			if !exists || time.Until(deadline) > 5*time.Second || request.Context().Err() != nil {
				t.Error("cancellation requires a fresh bounded context")
			}
			<-request.Context().Done()
			return nil, request.Context().Err()
		default:
			return http.DefaultTransport.RoundTrip(request)
		}
	})
	started := time.Now()
	code, stdout, stderr := codexLoginCLI(t, ctx, memberConfig(t, server.URL, "member-private"), "json", transport, "--yes", "--timeout", "100ms")
	frames := decodeOperationStream(t, stdout, "json")
	last := frames[len(frames)-1]["data"].(map[string]any)
	if code != 7 || cancellations != 1 || time.Since(started) > 2*time.Second || last["cancellation"] != "unknown" {
		t.Fatalf("code=%d cancellations=%d out=%s err=%s", code, cancellations, stdout, stderr)
	}
}

func TestCodexLoginCancellationOnlyCancelsItsAttempt(t *testing.T) {
	for _, test := range []struct {
		name                      string
		cancelParent, cancelFails bool
	}{{"wait timeout", false, false}, {"parent cancelled", true, false}, {"unknown cancellation", true, true}} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			starts, cancels := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if operationPreflight(writer, request, true) {
					return
				}
				switch request.URL.Path {
				case "/api/v1/operations/provider.codex.start":
					starts++
					interval := 1.0
					if test.cancelParent {
						interval = 0.001
					}
					codexStartResponse(writer, interval, time.Now().Add(time.Minute))
				case "/api/v1/operations/provider.codex.poll":
					cancel()
					fmt.Fprint(writer, `{"code":0,"data":{"status":"pending","interval":1}}`)
				case "/api/v1/operations/provider.codex.cancel":
					cancels++
					var body map[string]any
					_ = json.NewDecoder(request.Body).Decode(&body)
					if body["provider"] != "provider-1" || body["authorization_id"] != "attempt-private" {
						t.Fatal("cancellation targeted another attempt")
					}
					if test.cancelFails {
						writer.WriteHeader(500)
					}
					fmt.Fprint(writer, `{"code":0,"data":null}`)
				default:
					t.Errorf("unexpected call %s", request.URL.Path)
				}
			}))
			defer server.Close()
			code, stdout, stderr := codexLoginCLI(t, ctx, memberConfig(t, server.URL, "member-private"), "json", nil, "--yes", "--wait-timeout", "100ms")
			want := "confirmed"
			if test.cancelFails {
				want = "unknown"
			}
			frames := decodeOperationStream(t, stdout, "json")
			last := frames[len(frames)-1]["data"].(map[string]any)
			if code != 7 || starts != 1 || cancels != 1 || last["cancellation"] != want || last["status"] != "unknown" {
				t.Fatalf("code=%d starts=%d cancels=%d out=%s err=%s", code, starts, cancels, stdout, stderr)
			}
		})
	}
}

func memberConfig(t *testing.T, endpoint, token string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	err := (config.Store{Path: path}).Update(context.Background(), func(file *config.File) error {
		file.CurrentContext = "test"
		file.Contexts["test"] = config.Context{Endpoint: endpoint, UserToken: token, ExpectedWorkspaceUUID: "workspace", Credential: &config.Credential{Type: "env", Name: "TEST_KEY"}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMemberCredentialSelection(t *testing.T) {
	for _, test := range []struct {
		name, saved, env string
		args             []string
		member           bool
		status           int
	}{
		{"api-default", "saved-token", "env-token", []string{"agent", "list"}, false, 200},
		{"explicit", "saved-token", "", []string{"agent", "list", "--as-member"}, true, 200},
		{"member-auto", "saved-token", "", []string{"workspace", "member", "list"}, true, 200},
		{"env-priority", "saved-token", "env-token", []string{"assistant", "list"}, true, 200},
		{"generic-member", "saved-token", "", []string{"op", "call", "provider.codex.status", "--file", "-"}, true, 200},
		{"expired", "saved-token", "", []string{"assistant", "list"}, true, 401},
		{"forbidden", "saved-token", "", []string{"assistant", "list"}, true, 403},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				calls++
				if !strings.HasPrefix(request.Header.Get("User-Agent"), "lbctl/") || request.Header.Get("X-LangBot-Client-Command") == "" {
					t.Error("missing identity headers")
				}
				if test.member {
					token := test.saved
					if test.env != "" {
						token = test.env
					}
					if request.Header.Get("Authorization") != "Bearer "+token || request.Header.Get("X-API-Key") != "" {
						t.Error("incorrect member headers")
					}
				} else if request.Header.Get("X-API-Key") != "api-secret" || request.Header.Get("Authorization") != "" {
					t.Error("API key default changed")
				}
				if operationPreflight(writer, request, true) {
					return
				}
				if request.Method == "GET" && strings.HasPrefix(request.URL.Path, "/api/v1/operations/") {
					fmt.Fprintf(writer, `{"id":%q,"risk":"read","permission":"resource.view"}`, strings.TrimPrefix(request.URL.Path, "/api/v1/operations/"))
					return
				}
				writer.WriteHeader(test.status)
				fmt.Fprint(writer, `{"code":0,"data":{"value":"ok"}}`)
			}))
			defer server.Close()
			path := memberConfig(t, server.URL, test.saved)
			environment := map[string]string{"TEST_KEY": "api-secret"}
			if test.env != "" {
				environment["LANGBOT_USER_TOKEN"] = test.env
			}
			code, output := memberCLI(t, path, `{"provider":"openai"}`, environment, test.args...)
			if test.status != 200 {
				if code != 3 || !strings.Contains(output, "requires_member_session") {
					t.Fatalf("%d %s", code, output)
				}
			} else if code != 0 {
				t.Fatalf("%d %s", code, output)
			}
			if calls == 0 || strings.Contains(output, "saved-token") || strings.Contains(output, "env-token") || strings.Contains(output, "api-secret") {
				t.Fatalf("calls=%d output=%s", calls, output)
			}
		})
	}
}

func TestMemberRequiredBeforeRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("member-only command sent a request without token") }))
	defer server.Close()
	path := memberConfig(t, server.URL, "")
	for _, args := range [][]string{{"workspace", "member", "list"}, {"workspace", "invitation", "list"}, {"provider", "codex", "status", "openai"}, {"assistant", "list"}, {"pipeline", "migration", "preview"}, {"knowledge-base", "migration", "preview"}, {"op", "call", "assistant.list"}, {"agent", "list", "--as-member"}} {
		code, output := memberCLI(t, path, "", map[string]string{"TEST_KEY": "api-secret"}, args...)
		if code != 3 || !strings.Contains(output, "requires_member_session") {
			t.Fatalf("%v: %d %s", args, code, output)
		}
	}
}

func TestMemberLoginPersistenceAndLogout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/user/auth" || request.Header.Get("Authorization") != "" || request.Header.Get("X-API-Key") != "" {
			t.Error("login request")
		}
		fmt.Fprint(writer, `{"code":0,"data":{"token":"private-session"}}`)
	}))
	defer server.Close()
	path := memberConfig(t, server.URL, "")
	code, output := memberCLI(t, path, "password\n", nil, "auth", "login", "--email", "local@example.test", "--password-stdin")
	if code != 0 || strings.Contains(output, "private-session") {
		t.Fatalf("%d %s", code, output)
	}
	file, err := (config.Store{Path: path}).Load()
	if err != nil || file.Contexts["test"].UserToken != "private-session" {
		t.Fatal("session not saved")
	}
	info, err := os.Stat(path)
	if err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatal("credential mode must be 0600")
	}
	code, output = memberCLI(t, path, "", nil, "context", "show")
	if code != 0 || strings.Contains(output, "private-session") {
		t.Fatalf("%d %s", code, output)
	}
	code, output = memberCLI(t, path, "manual-session\n", nil, "auth", "set-token", "--stdin")
	if code != 0 || strings.Contains(output, "manual-session") {
		t.Fatalf("%d %s", code, output)
	}
	code, output = memberCLI(t, path, "", nil, "auth", "logout")
	if code != 0 {
		t.Fatalf("%d %s", code, output)
	}
	file, err = (config.Store{Path: path}).Load()
	if err != nil || file.Contexts["test"].UserToken != "" {
		t.Fatal("session not removed")
	}
}

func TestImageDownloadAndOperationDecode(t *testing.T) {
	image := []byte{137, 80, 78, 71, 13, 10, 26, 10, 0, 255}
	encoded := base64.StdEncoding.EncodeToString(image)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api/v1/files/image/v1/workspace/sample.png" {
			writer.Write(image)
			return
		}
		if operationPreflight(writer, request, true) {
			return
		}
		if request.Method == "GET" {
			fmt.Fprint(writer, `{"id":"file.image.get","risk":"read","output_schema":{"properties":{"content":{"type":"string","contentEncoding":"base64"}}}}`)
			return
		}
		fmt.Fprintf(writer, `{"code":0,"data":{"content":%q,"media_type":"image/png"}}`, encoded)
	}))
	defer server.Close()
	path := memberConfig(t, server.URL, "")
	environment := map[string]string{"TEST_KEY": "api-secret"}
	destination := filepath.Join(t.TempDir(), "image.png")
	code, output := memberCLI(t, path, "", environment, "file", "image", "get", "v1/workspace/sample.png", "-O", destination)
	if code != 0 || output != "" {
		t.Fatalf("%d %s", code, output)
	}
	data, err := os.ReadFile(destination)
	if err != nil || !bytes.Equal(data, image) {
		t.Fatal("download changed bytes")
	}
	code, output = memberCLI(t, path, "", environment, "file", "image", "get", "v1/workspace/sample.png")
	if code != 0 || !bytes.Equal([]byte(output), image) {
		t.Fatal("stdout changed bytes")
	}
	code, output = memberCLI(t, path, `{"file_key":"sample"}`, environment, "op", "call", "file.image.get", "--file", "-")
	if code != 0 || !strings.Contains(output, encoded) {
		t.Fatalf("base64 JSON changed: %d %s", code, output)
	}
	code, output = memberCLI(t, path, `{"file_key":"sample"}`, environment, "op", "call", "file.image.get", "--file", "-", "--decode-binary", "content", "-O", destination)
	if code != 0 {
		t.Fatalf("%d %s", code, output)
	}
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		t.Fatalf("invalid decoded output %q: %v", output, err)
	}
	if !reflect.DeepEqual(envelope.Data, map[string]any{"file": destination, "bytes": float64(len(image))}) {
		t.Fatalf("generic decoded output changed: %#v", envelope.Data)
	}
	data, err = os.ReadFile(destination)
	if err != nil || !bytes.Equal(data, image) {
		t.Fatal("decode changed bytes")
	}
}

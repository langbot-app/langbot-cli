package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/langbot-app/langbot-cli/internal/api"
	"github.com/langbot-app/langbot-cli/internal/app"
	"github.com/langbot-app/langbot-cli/internal/i18n"
	"github.com/langbot-app/langbot-cli/internal/result"
	"github.com/spf13/cobra"
)

type codexResponseTransport struct {
	http.RoundTripper
	retryAfter string
}

type codexDeviceAuthorization struct {
	attempt  string
	uri      string
	code     string
	interval time.Duration
	expires  time.Time
	progress map[string]any
}

func (transport *codexResponseTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := transport.RoundTripper.RoundTrip(request)
	transport.retryAfter = ""
	if response != nil && response.StatusCode == http.StatusTooManyRequests {
		transport.retryAfter = response.Header.Get("Retry-After")
	}
	return response, err
}

func addCodexLogin(root *cobra.Command, service *app.Service, deps Dependencies, flags *globalFlags) {
	var yes bool
	var waitTimeout time.Duration
	command := &cobra.Command{Use: "login <provider_uuid>", Short: i18n.T("Sign in to a Codex provider with a device code"), Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return emitCall(deps, flags, func() (app.Result, error) {
			if err := api.ValidateResourceID(args[0]); err != nil {
				return app.Result{}, err
			}
			if waitTimeout <= 0 {
				return app.Result{}, result.New("input", i18n.T("Local wait timeout must be positive"))
			}
			session, err := service.OpenOperations(cmd.Context(), connectionOptions(flags), false)
			if err != nil {
				return app.Result{}, codexSafeError(err)
			}
			if err := session.Authorize(friendlyOperationDetails["provider.codex.start"].description()); err != nil {
				return session.Result(nil), err
			}
			if err := confirmOperation(deps, yes, flags.apiKeyStdin); err != nil {
				return session.Result(nil), err
			}
			return runCodexLogin(cmd.Context(), session, deps, flags, args[0], waitTimeout)
		})
	}}
	command.Flags().BoolVar(&yes, "yes", false, i18n.T("Confirm starting Codex device login"))
	command.Flags().DurationVar(&waitTimeout, "wait-timeout", 15*time.Minute, i18n.T("Local wait timeout for Codex device login"))
	commandGroup(root, []string{"provider", "codex"}).AddCommand(command)
}

func runCodexLogin(parent context.Context, session *app.OperationSession, deps Dependencies, flags *globalFlags, provider string, timeout time.Duration) (app.Result, error) {
	data := map[string]any{"provider": provider, "status": "unknown", "completed": false}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	body := map[string]any{"provider": provider}
	value, err := session.Client.OperationCall(ctx, session.Target, "provider.codex.start", body)
	if err != nil {
		return session.Result(data), codexOperationError(err)
	}
	authorization, err := parseCodexAuthorization(value, provider)
	if err != nil {
		return session.Result(data), err
	}
	if !time.Now().Before(authorization.expires) {
		data["status"] = "expired"
		return session.Result(data), codexExpired()
	}
	data["status"] = "pending"
	if flags.output == "json" || flags.output == "yaml" || flags.output == "yml" {
		if emit(deps, flags, session.Result(authorization.progress), nil) != 0 {
			return session.Result(data), cancelCodexAttempt(parent, session, provider, authorization.attempt, data, false)
		}
		if flags.output == "yaml" || flags.output == "yml" {
			fmt.Fprintln(deps.Out, "---")
		}
	} else if _, err := fmt.Fprintf(deps.Err, "%s\n%s\n%s\n%s\n", i18n.T("Open this URL to authorize Codex"), authorization.uri, i18n.T("Enter this device code"), authorization.code); err != nil {
		return session.Result(data), cancelCodexAttempt(parent, session, provider, authorization.attempt, data, false)
	}
	transport := session.Client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	pacing := &codexResponseTransport{RoundTripper: transport}
	pollClient := session.Client
	pollClient.Transport = pacing
	interval := authorization.interval
	for {
		if err := waitCodexPoll(ctx, interval, authorization.expires); err != nil {
			if ctx.Err() != nil {
				return session.Result(data), cancelCodexAttempt(parent, session, provider, authorization.attempt, data, errors.Is(ctx.Err(), context.DeadlineExceeded))
			}
			data["status"] = "expired"
			return session.Result(data), err
		}
		value, err = pollClient.OperationCall(ctx, session.Target, "provider.codex.poll", map[string]any{"provider": provider, "authorization_id": authorization.attempt})
		if ctx.Err() != nil {
			return session.Result(data), cancelCodexAttempt(parent, session, provider, authorization.attempt, data, errors.Is(ctx.Err(), context.DeadlineExceeded))
		}
		if err != nil {
			if result.AsError(err).HTTPStatus == http.StatusTooManyRequests {
				interval = codexRetryDelay(pacing.retryAfter, interval, time.Now())
				continue
			}
			data["status"] = "unknown"
			return session.Result(data), codexOperationError(err)
		}
		poll, ok := value.(map[string]any)
		if !ok {
			data["status"] = "unknown"
			return session.Result(data), codexUnknown(i18n.T("Codex login returned an invalid polling response and the sign-in result is unknown"))
		}
		switch poll["status"] {
		case "pending":
			if declared, exists := poll["interval"]; exists {
				next, valid := codexDuration(declared)
				if !valid {
					data["status"] = "unknown"
					return session.Result(data), codexUnknown(i18n.T("Codex login returned an invalid polling interval and the sign-in result is unknown"))
				}
				interval = max(interval, next)
			}
		case "connected":
			value, err = session.Client.OperationCall(ctx, session.Target, "provider.codex.status", body)
			if err != nil {
				data["status"] = "unknown"
				return session.Result(data), codexUnknown(i18n.T("Codex connection readback failed and the sign-in result is unknown"))
			}
			status, ok := value.(map[string]any)
			if !ok || status["connected"] != true || status["status"] != "connected" {
				data["status"] = "unknown"
				return session.Result(data), codexUnknown(i18n.T("Codex connection verification failed and the sign-in result is unknown"))
			}
			data["status"], data["connected"], data["completed"] = "connected", true, true
			return session.Result(data), nil
		case "expired":
			data["status"] = "expired"
			return session.Result(data), codexExpired()
		default:
			data["status"] = "unknown"
			return session.Result(data), codexUnknown(i18n.T("Codex login returned an unknown polling status and the sign-in result is unknown"))
		}
	}
}

func parseCodexAuthorization(value any, provider string) (codexDeviceAuthorization, error) {
	start, ok := value.(map[string]any)
	authorization := codexDeviceAuthorization{}
	authorization.attempt, _ = start["authorization_id"].(string)
	authorization.uri, _ = start["verification_uri"].(string)
	authorization.code, _ = start["user_code"].(string)
	interval, validInterval := codexDuration(start["interval"])
	expiry, validExpiry := codexDuration(start["expires_at"])
	parsedURI, uriErr := url.Parse(authorization.uri)
	validURI := uriErr == nil && parsedURI.Host != "" && parsedURI.User == nil && parsedURI.RawQuery == "" && parsedURI.Fragment == ""
	if validURI {
		validURI = parsedURI.Scheme == "https" || parsedURI.Scheme == "http"
	}
	validCode := strings.TrimSpace(authorization.code) != "" && strings.IndexFunc(authorization.code, unicode.IsControl) < 0
	if !ok || strings.TrimSpace(authorization.attempt) == "" || !validInterval || !validExpiry || !validURI || !validCode {
		return authorization, codexUnknown(i18n.T("Codex login returned an invalid device authorization and the sign-in result is unknown"))
	}
	authorization.interval = interval
	authorization.expires = time.Unix(0, int64(expiry))
	authorization.progress = map[string]any{
		"provider": provider, "status": "pending", "verification_uri": authorization.uri,
		"user_code": authorization.code, "expires_at": start["expires_at"], "interval": start["interval"],
	}
	return authorization, nil
}

func codexDuration(value any) (time.Duration, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	seconds, err := number.Float64()
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 || seconds >= float64(math.MaxInt64)/float64(time.Second) {
		return 0, false
	}
	duration := time.Duration(seconds * float64(time.Second))
	return duration, duration > 0
}

func codexRetryDelay(header string, interval time.Duration, now time.Time) time.Duration {
	if seconds, err := strconv.ParseInt(strings.TrimSpace(header), 10, 64); (err == nil || errors.Is(err, strconv.ErrRange)) && seconds >= 0 {
		if seconds >= math.MaxInt64/int64(time.Second) {
			return time.Duration(math.MaxInt64)
		}
		return max(interval, time.Duration(seconds)*time.Second)
	}
	if deadline, err := http.ParseTime(header); err == nil {
		return max(interval, deadline.Sub(now))
	}
	if interval > time.Duration(math.MaxInt64)-5*time.Second {
		return time.Duration(math.MaxInt64)
	}
	return max(interval+5*time.Second, 5*time.Second)
}

func waitCodexPoll(ctx context.Context, interval time.Duration, expires time.Time) error {
	timer := time.NewTimer(min(interval, time.Until(expires)))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		if !time.Now().Before(expires) {
			return codexExpired()
		}
		return nil
	}
}

func cancelCodexAttempt(parent context.Context, session *app.OperationSession, provider, attempt string, data map[string]any, timedOut bool) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
	defer cancel()
	data["status"] = "unknown"
	_, err := session.Client.OperationCall(ctx, session.Target, "provider.codex.cancel", map[string]any{"provider": provider, "authorization_id": attempt})
	if err != nil {
		data["cancellation"] = "unknown"
		return codexUnknown(i18n.T("Codex login wait ended with an unknown cancellation and sign-in result"))
	}
	data["cancellation"] = "confirmed"
	message := i18n.T("Codex login cancelled with current attempt cancellation acknowledged and sign-in result unknown")
	typeName := "cancelled"
	if timedOut {
		message = i18n.T("Codex login wait timed out with current attempt cancellation acknowledged and sign-in result unknown")
		typeName = "wait_timeout"
	}
	failure := result.New("network", message)
	failure.Type = typeName
	return failure
}

func codexSafeError(err error) error {
	failure := result.AsError(err)
	return &result.Error{Kind: failure.Kind, Type: failure.Type, Message: failure.Message, HTTPStatus: failure.HTTPStatus}
}

func codexOperationError(err error) error {
	failure := result.AsError(err)
	if failure.Kind == "network" || failure.HTTPStatus >= 500 || failure.Kind == "incompatible" {
		return codexUnknown(i18n.T("Codex authorization result is unknown. Inspect provider status before starting again"))
	}
	return codexSafeError(err)
}

func codexUnknown(message string) *result.Error {
	failure := result.New("result_unknown", message)
	failure.Type = "result_unknown"
	return failure
}

func codexExpired() *result.Error {
	failure := result.New("precondition", i18n.T("Codex device authorization expired. Start a new login"))
	failure.Type = "expired"
	return failure
}

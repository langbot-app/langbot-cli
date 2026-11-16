package telemetry

import (
	"context"
	"testing"
)

func TestIdentityRedaction(t *testing.T) {
	for _, test := range []struct {
		message, home, want string
		users               []string
	}{
		{"native pip install [redacted]", "/home/t", "native pip install [redacted]", []string{"t", "pi"}},
		{"alice malice alice2 [redacted]", "/home/alice", "[redacted] malice alice2 [redacted]", []string{"alice", "alice", "redacted"}},
		{"native /home/pi/app", "/home/pi", "native [redacted]/app", []string{"pi", "t"}},
		{"alice /custom/alice/file", "/custom/alice", "[redacted] [redacted]/file", []string{"alice", "ali"}},
	} {
		got := sanitizeIdentity(test.message, test.home, test.users)
		if got != test.want || sanitizeIdentity(got, test.home, test.users) != got {
			t.Fatalf("%q: got %q, want %q", test.message, got, test.want)
		}
	}
}

func TestStepTrackingWithoutTelemetry(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		ctx := context.Background()
		op := &Operation{}
		if enabled {
			ctx = context.WithValue(ctx, operationKey{}, op)
		}
		ctx = TrackSteps(ctx)
		child, cancel := context.WithCancel(ctx)
		for _, step := range []string{"download_uv", "install_package", "health_check"} {
			SetStep(child, step)
			if CurrentStep(ctx) != step || enabled && op.Event.Step != step {
				t.Fatalf("enabled=%v: cli=%s telemetry=%s", enabled, CurrentStep(ctx), op.Event.Step)
			}
		}
		cancel()
	}
}

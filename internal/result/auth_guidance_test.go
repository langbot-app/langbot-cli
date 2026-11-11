package result

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAuthGuidanceRejectsUnsafeVariableNames(t *testing.T) {
	for _, variable := range []string{"bad; touch file", "$SECRET", "bad\nname", ""} {
		err := AuthSetupError("environment_variable_unavailable", variable)
		if err.Guidance.EnvironmentVariable != "LANGBOT_API_KEY" || ExitCode(err) != 3 {
			t.Fatalf("unsafe variable accepted: %+v", err)
		}
		encoded, _ := json.Marshal(err)
		if variable != "" && strings.Contains(string(encoded), variable) {
			t.Fatal("unsafe variable leaked into instructions")
		}
	}
}

func TestAuthGuidanceDoesNotAlterPermissionErrors(t *testing.T) {
	err := New("permission", "denied")
	AddAuthGuidance(err, "authentication_rejected", "DEV_KEY")
	if err.Guidance != nil || err.Reason != "" || ExitCode(err) != 4 {
		t.Fatal("permission error was changed")
	}
}

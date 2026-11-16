package result

import "github.com/langbot-app/langbot-cli/internal/i18n"

import "regexp"

// AuthGuidance contains setup instructions, never a credential value.
type AuthGuidance struct {
	EnvironmentVariable string            `json:"environment_variable,omitempty" yaml:"environment_variable,omitempty"`
	Steps               []string          `json:"steps" yaml:"steps"`
	Examples            map[string]string `json:"examples,omitempty" yaml:"examples,omitempty"`
}

var environmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func AuthSetupError(reason, variable string) *Error {
	message := i18n.T("No API Key provided")
	switch reason {
	case "environment_variable_unavailable":
		message = i18n.T("API Key environment variable is unset or empty")
	case "empty_stdin":
		message = i18n.T("API Key from stdin is empty")
	case "endpoint_changed":
		message = i18n.T("endpoint overridden; explicitly provide a new API Key")
	}
	err := New("auth", message)
	AddAuthGuidance(err, reason, variable)
	return err
}

// AddAuthGuidance preserves the error type, status and exit-code contract.
func AddAuthGuidance(err *Error, reason, variable string) {
	if err == nil || err.Kind != "auth" {
		return
	}
	err.Reason = reason
	verify := i18n.T("After configuration, run lbctl whoami on the same target; preserve --config, --context, --endpoint and other connection options from the original command.")
	if reason == "authentication_rejected" {
		err.Guidance = &AuthGuidance{Steps: []string{
			i18n.T("Server rejected the supplied API Key; check the target URL and whether the Key is incorrect, expired or revoked."),
			i18n.T("To replace a Key, create a Workspace API Key in the target LangBot admin page and supply it through the original credential source; do not print or log the Key."),
			verify,
		}}
		if environmentName.MatchString(variable) {
			err.Guidance.EnvironmentVariable = variable
		}
		return
	}
	if !environmentName.MatchString(variable) {
		variable = "LANGBOT_API_KEY"
	}
	err.Guidance = &AuthGuidance{
		EnvironmentVariable: variable,
		Steps: []string{
			i18n.T("Open the target LangBot admin page, complete initialization and create a Workspace API Key in API Key management."),
			i18n.T("Set the environment variable in the terminal running lbctl: ") + variable + i18n.T("; the example below hides input and keeps the Key out of command history. You can also explicitly use --api-key-stdin."),
			verify,
		},
		Examples: map[string]string{
			"powershell": "$env:" + variable + " = [System.Net.NetworkCredential]::new('', (Read-Host 'LangBot API Key' -AsSecureString)).Password",
			"bash":       "read -r -s -p 'LangBot API Key: ' " + variable + "; echo; export " + variable,
		},
	}
}

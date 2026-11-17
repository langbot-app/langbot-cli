package config

import (
	"bytes"
	"io"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/langbot-app/langbot-cli/internal/endpoint"
	"github.com/langbot-app/langbot-cli/internal/i18n"
	"github.com/langbot-app/langbot-cli/internal/result"
)

func Resolve(file File, opts Options, lookup func(string) (string, bool)) (Connection, error) {
	if err := validateFile(file); err != nil {
		return Connection{}, err
	}
	getenv := envLookup(lookup)

	contextName, contextSet, err := selectContext(file, opts, getenv)
	if err != nil {
		return Connection{}, err
	}
	selected, hasContext := file.Contexts[contextName]
	if contextSet && !hasContext {
		return Connection{}, inputError(i18n.T("Specified context does not exist"))
	}

	endpointValue, endpointOverride, err := selectEndpoint(opts, getenv, selected, hasContext)
	if err != nil {
		return Connection{}, err
	}
	normalizedEndpoint, err := endpoint.Normalize(endpointValue)
	if err != nil {
		return Connection{}, inputError(i18n.T("Invalid endpoint"))
	}

	changedTarget := !hasContext || endpointOverride && normalizedEndpoint != mustNormalize(selected.Endpoint)
	connection := Connection{
		Context:               contextName,
		Endpoint:              normalizedEndpoint,
		Temporary:             changedTarget,
		Timeout:               defaultTimeout,
		credentialType:        "",
		credentialRef:         "",
		ExpectedWorkspaceUUID: "",
	}
	if hasContext && !changedTarget {
		connection.ExpectedWorkspaceUUID = selected.ExpectedWorkspaceUUID
	}

	if err := applyTimeout(&connection, opts, getenv, selected, hasContext); err != nil {
		return Connection{}, err
	}

	apiKeyExplicit := opts.APIKeyStdin
	if opts.APIKeyStdin {
		connection.CredentialSource = "stdin"
		connection.credentialType = "stdin"
	} else if _, present := getenv("LANGBOT_API_KEY"); present {
		apiKeyExplicit = true
		connection.CredentialSource = "env:LANGBOT_API_KEY"
		connection.credentialType = "env"
		connection.credentialRef = "LANGBOT_API_KEY"
	} else if hasContext && !changedTarget && selected.Credential != nil {
		connection.CredentialSource = selected.Credential.Type
		if selected.Credential.Type == "env" {
			connection.CredentialSource += ":" + selected.Credential.Name
		}
		connection.credentialType = selected.Credential.Type
		connection.credentialRef = selected.Credential.Name
	}

	if changedTarget && hasContext && selected.Credential != nil && !apiKeyExplicit {
		connection.requiresExplicitCredential = true
	}
	return connection, nil
}

func (c Connection) WithCredential(opts Options, lookup func(string) (string, bool), stdin io.Reader) (Connection, error) {
	copy := c
	copy.APIKey = ""
	if opts.APIKeyStdin {
		if stdin == nil {
			return copy, authError(i18n.T("Unable to read API Key from stdin"))
		}
		value, err := readSecret(stdin)
		if err != nil {
			return copy, err
		}
		copy.APIKey = value
		copy.CredentialSource = "stdin"
		return copy, nil
	}
	if copy.credentialType == "" {
		if copy.requiresExplicitCredential {
			return copy, result.AuthSetupError("endpoint_changed", "LANGBOT_API_KEY")
		}
		return copy, nil
	}
	if copy.credentialType == "local_deployment" {
		file, err := os.Open(copy.credentialRef)
		if err != nil {
			return copy, authError(i18n.T("Local API credential unavailable; run lbctl restart"))
		}
		defer file.Close()
		value, err := readSecret(file)
		if err != nil {
			return copy, authError(i18n.T("Local API credential unavailable; run lbctl restart"))
		}
		copy.APIKey = value
		return copy, nil
	}
	if copy.credentialType != "env" {
		return copy, authError(i18n.T("Unsupported credential source"))
	}
	getenv := envLookup(lookup)
	value, ok := getenv(copy.credentialRef)
	if !ok || strings.TrimSpace(value) == "" {
		return copy, result.AuthSetupError("environment_variable_unavailable", copy.credentialRef)
	}
	if len(value) > maxSecretBytes {
		return copy, authError(i18n.T("API Key is too long"))
	}
	if err := validateSecret(value); err != nil {
		return copy, err
	}
	copy.APIKey = value
	return copy, nil
}

func selectContext(file File, opts Options, getenv func(string) (string, bool)) (string, bool, error) {
	if opts.ContextSet {
		if strings.TrimSpace(opts.Context) == "" {
			return "", true, inputError(i18n.T("context must not be empty"))
		}
		return opts.Context, true, nil
	}
	if value, present := getenv("LANGBOT_CONTEXT"); present {
		if strings.TrimSpace(value) == "" {
			return "", true, inputError(i18n.T("LANGBOT_CONTEXT is empty"))
		}
		return value, true, nil
	}
	if file.CurrentContext != "" {
		return file.CurrentContext, true, nil
	}
	return "", false, nil
}

func selectEndpoint(opts Options, getenv func(string) (string, bool), selected Context, hasContext bool) (string, bool, error) {
	if opts.EndpointSet {
		if strings.TrimSpace(opts.Endpoint) == "" {
			return "", true, inputError(i18n.T("endpoint must not be empty"))
		}
		return opts.Endpoint, true, nil
	}
	if value, present := getenv("LANGBOT_ENDPOINT"); present {
		if strings.TrimSpace(value) == "" {
			return "", true, inputError(i18n.T("LANGBOT_ENDPOINT is empty"))
		}
		return value, true, nil
	}
	if !hasContext {
		return "", false, inputError(i18n.T("No context selected and no endpoint provided"))
	}
	return selected.Endpoint, false, nil
}

func applyTimeout(connection *Connection, opts Options, getenv func(string) (string, bool), selected Context, hasContext bool) error {
	value := ""
	present := false
	if opts.TimeoutSet {
		value, present = opts.Timeout, true
	} else if envValue, ok := getenv("LANGBOT_TIMEOUT"); ok {
		value, present = envValue, true
	} else if hasContext {
		value, present = selected.Timeout, selected.Timeout != ""
	}
	if !present {
		connection.Timeout = defaultTimeout
		return nil
	}
	if strings.TrimSpace(value) == "" {
		return inputError(i18n.T("timeout must not be empty"))
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return inputError(i18n.T("Invalid timeout"))
	}
	connection.Timeout = duration
	return nil
}

func envLookup(lookup func(string) (string, bool)) func(string) (string, bool) {
	if lookup != nil {
		return lookup
	}
	return os.LookupEnv
}

func mustNormalize(value string) string {
	normalized, err := endpoint.Normalize(value)
	if err != nil {
		return value
	}
	return normalized
}

func readSecret(reader io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxSecretBytes+1))
	if err != nil {
		return "", authError(i18n.T("Failed to read API Key"))
	}
	if len(data) > maxSecretBytes {
		return "", authError(i18n.T("API Key is too long"))
	}
	value := strings.TrimSpace(string(bytes.TrimPrefix(data, []byte("\ufeff"))))
	if value == "" {
		return "", result.AuthSetupError("empty_stdin", "")
	}
	if err := validateSecret(value); err != nil {
		return "", err
	}
	return value, nil
}

func validateSecret(value string) error {
	if strings.TrimSpace(value) == "" {
		return authError(i18n.T("API Key is empty"))
	}
	if strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return authError(i18n.T("API Key contains invalid characters"))
	}
	return nil
}

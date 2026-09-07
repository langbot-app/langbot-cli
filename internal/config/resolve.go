package config

import (
	"bytes"
	"io"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/langbot-app/langbot-cli/internal/endpoint"
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
		return Connection{}, inputError("指定的 context 不存在")
	}

	endpointValue, endpointOverride, err := selectEndpoint(opts, getenv, selected, hasContext)
	if err != nil {
		return Connection{}, err
	}
	normalizedEndpoint, err := endpoint.Normalize(endpointValue)
	if err != nil {
		return Connection{}, inputError("endpoint 无效")
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
		connection.CredentialSource = "env:" + selected.Credential.Name
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
			return copy, authError("无法读取 stdin 中的 API Key")
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
			return copy, authError("endpoint 已覆盖，必须显式提供新的 API Key")
		}
		return copy, nil
	}
	if copy.credentialType != "env" {
		return copy, authError("不支持的凭据来源")
	}
	getenv := envLookup(lookup)
	value, ok := getenv(copy.credentialRef)
	if !ok || strings.TrimSpace(value) == "" {
		return copy, authError("凭据来源不可用")
	}
	if len(value) > maxSecretBytes {
		return copy, authError("API Key 过长")
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
			return "", true, inputError("context 不能为空")
		}
		return opts.Context, true, nil
	}
	if value, present := getenv("LANGBOT_CONTEXT"); present {
		if strings.TrimSpace(value) == "" {
			return "", true, inputError("LANGBOT_CONTEXT 为空")
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
			return "", true, inputError("endpoint 不能为空")
		}
		return opts.Endpoint, true, nil
	}
	if value, present := getenv("LANGBOT_ENDPOINT"); present {
		if strings.TrimSpace(value) == "" {
			return "", true, inputError("LANGBOT_ENDPOINT 为空")
		}
		return value, true, nil
	}
	if !hasContext {
		return "", false, inputError("未选择 context 且未提供 endpoint")
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
		return inputError("timeout 不能为空")
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return inputError("timeout 无效")
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
		return "", authError("读取 API Key 失败")
	}
	if len(data) > maxSecretBytes {
		return "", authError("API Key 过长")
	}
	value := strings.TrimSpace(string(bytes.TrimPrefix(data, []byte("\ufeff"))))
	if value == "" {
		return "", authError("API Key 为空")
	}
	if err := validateSecret(value); err != nil {
		return "", err
	}
	return value, nil
}

func validateSecret(value string) error {
	if strings.TrimSpace(value) == "" {
		return authError("API Key 为空")
	}
	if strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return authError("API Key 包含非法字符")
	}
	return nil
}

// Package redact shares configuration masking between API projections and output.
package redact

import (
	"net/url"
	"strings"
)

const Mask = "[REDACTED]"

func IsSecretField(name string) bool {
	name = strings.ToLower(strings.ReplaceAll(name, "-", "_"))
	switch name {
	case "api_key_id", "credential_source", "token_count", "input_tokens", "output_tokens", "total_tokens", "max_tokens":
		return false
	case "key", "auth", "cookie", "set_cookie", "credentials", "database_url", "dsn", "header_value", "webhook_url":
		return true
	}
	for _, part := range []string{"apikey", "api_key", "token", "password", "passwd", "secret", "authorization", "credential", "private_key", "privatekey", "access_key", "accesskey", "signing_key", "signingkey", "client_key", "clientkey"} {
		if strings.Contains(name, part) {
			return true
		}
	}
	return false
}

func masked(value any) bool {
	switch v := value.(type) {
	case nil:
		return true
	case string:
		return v == "" || v == "***" || v == Mask
	case map[string]any:
		for _, item := range v {
			if !masked(item) {
				return false
			}
		}
		return true
	case []any:
		for _, item := range v {
			if !masked(item) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// Value returns a copy, preserving ordinary configuration and masking credentials.
func Value(value any, field string) any {
	return valueWithMask(value, field, Mask)
}

// Configuration uses Core's placeholder so supported update APIs can preserve secrets.
func Configuration(value any, field string) any {
	return valueWithMask(value, field, "***")
}

func valueWithMask(value any, field, mask string) any {
	secret := IsSecretField(field)
	if mask == Mask {
		// Result envelopes also contain protocol metadata (e.g. exit_codes.auth),
		// not just configuration. Keep the established output classifier there.
		secret = false
		name := strings.ToLower(field)
		switch name {
		case "api_key_id", "token_count", "input_tokens", "output_tokens", "total_tokens", "max_tokens":
		default:
			for _, part := range []string{"apikey", "api_key", "token", "password", "secret", "authorization"} {
				if strings.Contains(name, part) {
					secret = true
					break
				}
			}
		}
	}
	if secret {
		if masked(value) {
			return value
		}
		return mask
	}
	switch v := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(v))
		for key, item := range v {
			result[key] = valueWithMask(item, key, mask)
		}
		return result
	case []any:
		result := make([]any, len(v))
		for i, item := range v {
			result[i] = valueWithMask(item, field, mask)
		}
		return result
	case string:
		name := strings.ToLower(field)
		if name == "url" || strings.HasSuffix(name, "_url") {
			parsed, err := url.Parse(v)
			if err != nil {
				return mask
			}
			changed := false
			if parsed.User != nil {
				parsed.User = url.User("***")
				changed = true
			}
			query, err := url.ParseQuery(parsed.RawQuery)
			if err != nil {
				return mask
			}
			for key := range query {
				name := strings.ToLower(key)
				if IsSecretField(key) || name == "sig" || name == "signature" || name == "code" {
					for i := range query[key] {
						if query[key][i] != "" {
							query[key][i] = "***"
						}
					}
					changed = true
				}
			}
			if changed {
				parsed.RawQuery = query.Encode()
				return parsed.String()
			}
		}
		return v
	default:
		return value
	}
}

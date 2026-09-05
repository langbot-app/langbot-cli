package config

import "github.com/langbot-app/langbot-cli/internal/result"

func inputError(message string) error {
	return result.New("input", message)
}

func authError(message string) error {
	return result.New("auth", message)
}

func networkError(message string) error {
	return result.New("network", message)
}

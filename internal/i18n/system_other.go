//go:build !windows && !darwin

package i18n

func systemLanguage() string { return "" }

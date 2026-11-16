package telemetry

import (
	"net"
	"regexp"
	"strings"
	"unicode/utf8"
)

var cliPrivatePatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)https?://[^\s<>"']+`),
	regexp.MustCompile(`(?i)(?:[a-z]:\\|\\\\)[^\r\n"'<>]+`),
	regexp.MustCompile(`/(?:home|Users|root|tmp|var|opt|mnt|private|usr)/[^\r\n"'<>]+`),
	regexp.MustCompile(`(?i)\b(?:bearer|basic)\s+[a-z0-9._~+/=\-]+`),
	regexp.MustCompile(`(?i)["']?(?:[a-z0-9_\-]*(?:token|password|passwd|secret|api[_-]?key)|authorization|username|user)["']?\s*[:=]\s*(?:"[^"]*"|'[^']*'|[^\s,;]+)`),
	regexp.MustCompile(`\b(?:sk-|ghp_|github_pat_)[a-zA-Z0-9_\-]+`),
	regexp.MustCompile(`\beyJ[a-zA-Z0-9_\-]+\.[a-zA-Z0-9_\-]+\.[a-zA-Z0-9_\-]+`),
	regexp.MustCompile(`\b[0-9]{1,3}(?:\.[0-9]{1,3}){3}\b`),
}
var cliIPv6 = regexp.MustCompile(`[a-fA-F0-9:]*:[a-fA-F0-9:]+(?:%[a-zA-Z0-9]+)?`)

func SanitizeError(value string) string {
	for _, pattern := range cliPrivatePatterns {
		value = pattern.ReplaceAllString(value, "[redacted]")
	}
	value = cliIPv6.ReplaceAllStringFunc(value, func(candidate string) string {
		address, _, _ := strings.Cut(candidate, "%")
		if net.ParseIP(address) != nil {
			return "[redacted]"
		}
		return candidate
	})
	value = strings.ToValidUTF8(value, "")
	for len(value) > 1024 {
		if _, tail, found := strings.Cut(value, "\n"); found {
			value = tail
			continue
		}
		value = value[len(value)-1024:]
		for !utf8.ValidString(value) {
			value = value[1:]
		}
	}
	return value
}

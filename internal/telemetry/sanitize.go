package telemetry

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/langbot-app/langbot-cli/internal/diagnostic"
)

func sanitizeIdentity(value, home string, usernames []string) string {
	value = diagnostic.Text(value)
	if home != "" {
		prefix := home[:strings.LastIndexAny(home, `/\`)+1]
		value = strings.ReplaceAll(value, home, prefix+"<user>")
	}
	for _, username := range usernames {
		if utf8.RuneCountInString(username) >= 3 && username != "redacted" && username != "user" {
			value = regexp.MustCompile(`\b`+regexp.QuoteMeta(username)+`\b`).ReplaceAllString(value, "<user>")
		}
	}
	return value
}

func SanitizeError(value string) string {
	return diagnostic.Tail(diagnostic.Text(value), 0, diagnostic.FieldLimit)
}

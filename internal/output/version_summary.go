package output

import (
	"fmt"
	"strings"

	"github.com/langbot-app/langbot-cli/internal/deploy"
	"github.com/langbot-app/langbot-cli/internal/i18n"
)

func versionSummary(current, latest, command string) string {
	if current == "" || latest == "" {
		return current
	}
	comparison, err := deploy.CompareReleaseVersions("v"+strings.TrimPrefix(current, "v"), latest)
	if err != nil {
		return current
	}
	if comparison < 0 {
		return current + " · " + fmt.Sprintf(i18n.T("Update available: %s (%s)"), latest, command)
	}
	return current + i18n.T(" (up to date)")
}

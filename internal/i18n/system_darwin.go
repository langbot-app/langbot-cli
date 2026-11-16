package i18n

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

func systemLanguage() string {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "defaults", "read", "-g", "AppleLanguages").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(output), "\n") {
		value := strings.Trim(strings.TrimSpace(line), "(),\" ")
		if value != "" {
			return value
		}
	}
	return ""
}

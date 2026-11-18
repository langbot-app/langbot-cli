package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/langbot-app/langbot-cli/internal/i18n"
)

var instanceNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

func Home() (string, error) {
	if value := os.Getenv("LANGBOT_HOME"); value != "" {
		return filepath.Abs(value)
	}
	if runtime.GOOS == "windows" {
		base, err := os.UserCacheDir()
		return filepath.Join(base, "LangBot"), err
	}
	if currentUserIsRoot() {
		return "/opt/langbot", nil
	}
	base, err := os.UserHomeDir()
	return filepath.Join(base, ".langbot"), err
}

func InstanceDir(name string) (string, error) {
	if !instanceNamePattern.MatchString(name) {
		return "", fmt.Errorf(i18n.T("invalid instance name: %s"), name)
	}
	base, err := Home()
	return filepath.Join(base, "instances", name), err
}

func ResolveRegion(ctx context.Context, region string, client *http.Client) (string, error) {
	if region == "" || region == "auto" {
		region = os.Getenv("LANGBOT_REGION")
	}
	if region != "" && region != "auto" {
		if region != "cn" && region != "global" {
			return "", errors.New(i18n.T("region must be auto, cn or global"))
		}
		return region, nil
	}
	ctx, cancel := context.WithTimeout(ctx, regionProbeTimeout)
	defer cancel()
	if client == nil {
		client = http.DefaultClient
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, regionTraceURL, nil)
	resp, err := client.Do(req)
	if err != nil {
		return fallbackRegion(), nil
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if err != nil || resp.StatusCode != http.StatusOK {
		return fallbackRegion(), nil
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "loc=CN" {
			return "cn", nil
		}
	}
	return "global", nil
}

var (
	regionTraceURL     = "https://langbot.app/cdn-cgi/trace"
	regionProbeTimeout = 4 * time.Second
	regionSystemLocale = i18n.SystemLanguage
)

// fallbackRegion is used only when the location probe is unreachable. A slow or
// blocked probe is most common on mainland China networks, so a Simplified
// Chinese / mainland locale selects the cn mirrors instead of Docker Hub/PyPI.
func fallbackRegion() string {
	values := []string{}
	for _, key := range []string{"LBCTL_LANG", "LANGBOT_LANG", "LC_ALL", "LC_MESSAGES", "LANG"} {
		values = append(values, os.Getenv(key))
	}
	if regionSystemLocale != nil {
		values = append(values, regionSystemLocale())
	}
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		value, _, _ = strings.Cut(value, ".")
		value, _, _ = strings.Cut(value, "@")
		if value == "" || value == "c" || value == "posix" {
			continue
		}
		value = strings.ReplaceAll(value, "_", "-")
		if value == "zh" || value == "zh-cn" || value == "zh-hans" || value == "zh-sg" || strings.HasPrefix(value, "zh-hans-") || strings.HasPrefix(value, "zh-cn-") {
			return "cn"
		}
		return "global"
	}
	return "global"
}

func ApplyRegion(record *Record) {
	if record.PyPIIndex == "" {
		record.PyPIIndex = "https://pypi.org/simple"
		if record.Region == "cn" {
			record.PyPIIndex = "https://mirrors.aliyun.com/pypi/simple"
		}
	}
	if record.PythonMirror == "" && record.Region == "cn" {
		record.PythonMirror = "https://registry.npmmirror.com/-/binary/python-build-standalone"
	}
	if record.ImageRepository == "" {
		record.ImageRepository = DefaultImageRepository
		if record.Region == "cn" {
			record.ImageRepository = CanonicalImageRepository
		}
	}
}

func ImageRepository(record Record) string {
	if record.ImageRepository != "" {
		return record.ImageRepository
	}
	return DefaultImageRepository
}

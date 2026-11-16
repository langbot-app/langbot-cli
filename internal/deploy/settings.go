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
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if client == nil {
		client = http.DefaultClient
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://langbot.app/cdn-cgi/trace", nil)
	resp, err := client.Do(req)
	if err != nil {
		return "global", nil
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if err == nil && resp.StatusCode == http.StatusOK {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.TrimSpace(line) == "loc=CN" {
				return "cn", nil
			}
		}
	}
	return "global", nil
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
			record.ImageRepository = "docker.langbot.app/rockchin/langbot"
		}
	}
}

func ImageRepository(record Record) string {
	if record.ImageRepository != "" {
		return record.ImageRepository
	}
	return DefaultImageRepository
}

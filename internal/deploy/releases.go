package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/langbot-app/langbot-cli/internal/i18n"
	"github.com/langbot-app/langbot-cli/internal/progress"
	"github.com/langbot-app/langbot-cli/internal/telemetry"
	"io"
	"net/http"
	"strings"
	"time"
)

const CoreReleasesURL = "https://space.langbot.app/api/v1/dist/info/releases"
const GitHubReleaseURL = "https://api.github.com/repos/langbot-app/LangBot/releases/latest"

type release struct {
	Tag        string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

func LatestCore(ctx context.Context, client *http.Client) (string, error) {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	var lastErr error
	for _, address := range []string{CoreReleasesURL, GitHubReleaseURL} {
		var data []byte
		data, lastErr = Fetch(ctx, client, address, 8<<20)
		if lastErr != nil {
			continue
		}
		var releases []release
		if address == CoreReleasesURL {
			var payload struct {
				Code int       `json:"code"`
				Data []release `json:"data"`
			}
			lastErr = json.Unmarshal(data, &payload)
			if lastErr != nil || payload.Code != 0 {
				continue
			}
			releases = payload.Data
		} else {
			var item release
			lastErr = json.Unmarshal(data, &item)
			if lastErr != nil {
				continue
			}
			releases = []release{item}
		}
		latest := ""
		for _, item := range releases {
			tag := strings.TrimSpace(item.Tag)
			if item.Draft || item.Prerelease || !releaseVersionPattern.MatchString(tag) {
				continue
			}
			if latest == "" {
				latest = tag
			} else if comparison, _ := CompareReleaseVersions(latest, tag); comparison < 0 {
				latest = tag
			}
		}
		if latest != "" {
			return latest, nil
		}
		lastErr = errors.New(i18n.T("no stable Core release"))
	}
	return "", fmt.Errorf(i18n.T("cannot resolve latest Core release: %v"), lastErr)
}

func Fetch(ctx context.Context, client *http.Client, address string, limit int64) ([]byte, error) {
	telemetry.RecordSource(ctx, address)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "lbctl")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf(i18n.T("%s: HTTP %d"), address, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(progress.Download(ctx, resp.Body, resp.ContentLength), limit+1))
	if int64(len(data)) > limit {
		return nil, errors.New(i18n.T("download exceeds size limit"))
	}
	return data, err
}

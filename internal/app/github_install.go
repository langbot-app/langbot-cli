package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/langbot-app/langbot-cli/internal/clientidentity"
	"github.com/langbot-app/langbot-cli/internal/i18n"
	"github.com/langbot-app/langbot-cli/internal/result"
)

// Resolve URL input to the same release/asset identifiers used by the Web UI.
// This request never carries the Workspace API key to GitHub.
func resolveGitHubPluginAsset(ctx context.Context, transport http.RoundTripper, body map[string]any) (map[string]any, error) {
	assetURL, _ := body["asset_url"].(string)
	if assetURL == "" || body["asset_id"] != nil || body["release_id"] != nil {
		return body, nil
	}
	owner, _ := body["owner"].(string)
	repo, _ := body["repo"].(string)
	tag, _ := body["release_tag"].(string)
	u, err := url.Parse(assetURL)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || owner == "" || repo == "" || tag == "" || strings.ContainsAny(owner+repo, "/\\") || !strings.HasPrefix(u.Path, "/"+owner+"/"+repo+"/releases/download/"+tag+"/") {
		return nil, result.New("input", i18n.T("GitHub plugin URL must belong to the specified repository and release"))
	}
	requestURL := "https://api.github.com/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/releases/tags/" + url.PathEscape(tag)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, result.New("input", i18n.T("Invalid GitHub release parameters"))
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", clientidentity.UserAgent(ctx))
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return nil, result.New("network", i18n.T("Failed to read GitHub release information; installation not submitted"))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, result.New("server", i18n.T("GitHub release information unavailable; installation not submitted"))
	}
	var release struct {
		ID     int64  `json:"id"`
		Tag    string `json:"tag_name"`
		Assets []struct {
			ID  int64  `json:"id"`
			URL string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&release); err != nil || release.ID <= 0 || release.Tag != tag {
		return nil, result.New("server", i18n.T("GitHub release information does not match the request; installation not submitted"))
	}
	for _, asset := range release.Assets {
		if asset.URL == assetURL && asset.ID > 0 {
			resolved := make(map[string]any, len(body)+2)
			for k, v := range body {
				resolved[k] = v
			}
			delete(resolved, "asset_url")
			resolved["release_id"], resolved["asset_id"] = release.ID, asset.ID
			return resolved, nil
		}
	}
	return nil, result.New("input", i18n.T("Specified file does not belong to this GitHub release; installation not submitted"))
}

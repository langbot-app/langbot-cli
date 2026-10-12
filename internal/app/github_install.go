package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

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
		return nil, result.New("input", "GitHub 插件链接必须属于指定仓库和 release")
	}
	requestURL := "https://api.github.com/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/releases/tags/" + url.PathEscape(tag)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, result.New("input", "GitHub release 参数无效")
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "LangBot-CLI")
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return nil, result.New("network", "读取 GitHub release 信息失败；未提交安装")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, result.New("server", "GitHub release 信息不可用；未提交安装")
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
		return nil, result.New("server", "GitHub release 信息不符合请求；未提交安装")
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
	return nil, result.New("input", "指定文件不属于该 GitHub release；未提交安装")
}

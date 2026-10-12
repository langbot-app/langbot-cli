package app

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestResolveGitHubURLUsesReleaseIDsWithoutCredentials(t *testing.T) {
	link := "https://github.com/org/repo/releases/download/v1/test.lbpkg"
	body := map[string]any{"owner": "org", "repo": "repo", "release_tag": "v1", "asset_url": link, "release_id": nil, "asset_id": nil}
	transport := taskRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://api.github.com/repos/org/repo/releases/tags/v1" || r.Header.Get("Authorization") != "" {
			t.Fatalf("unsafe metadata request: %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"id":42,"tag_name":"v1","assets":[{"id":99,"browser_download_url":"` + link + `"}]}`)), Header: make(http.Header)}, nil
	})
	resolved, err := resolveGitHubPluginAsset(context.Background(), transport, body)
	if err != nil || resolved["release_id"] != int64(42) || resolved["asset_id"] != int64(99) || resolved["asset_url"] != nil {
		t.Fatalf("resolved=%#v err=%v", resolved, err)
	}
	if body["asset_url"] != link {
		t.Fatal("mutated caller body")
	}
}

func TestResolveGitHubURLRejectsUntrustedURLBeforeNetwork(t *testing.T) {
	body := map[string]any{"owner": "org", "repo": "repo", "release_tag": "v1", "asset_url": "http://127.0.0.1/internal"}
	transport := taskRoundTripFunc(func(*http.Request) (*http.Response, error) { t.Fatal("unexpected network"); return nil, nil })
	if _, err := resolveGitHubPluginAsset(context.Background(), transport, body); err == nil {
		t.Fatal("expected rejection")
	}
}

func TestResolveGitHubExplicitIDsNeedNoMetadataRequest(t *testing.T) {
	body := map[string]any{"release_id": 42, "asset_id": 99}
	transport := taskRoundTripFunc(func(*http.Request) (*http.Response, error) { t.Fatal("unexpected network"); return nil, nil })
	if _, err := resolveGitHubPluginAsset(context.Background(), transport, body); err != nil {
		t.Fatal(err)
	}
}

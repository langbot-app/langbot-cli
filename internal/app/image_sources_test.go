package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/langbot-app/langbot-cli/internal/deploy"
	"github.com/langbot-app/langbot-cli/internal/telemetry"
)

func upgradeManifest(version string) (string, string) {
	body := fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[],"annotations":{"version":%q}}`, version)
	sum := sha256.Sum256([]byte(body))
	return body, "sha256:" + hex.EncodeToString(sum[:])
}

type pinnedUpgradeRunner struct {
	*upgradeRunner
	failAll bool
}

func (runner *pinnedUpgradeRunner) Docker(ctx context.Context, arguments ...string) (deploy.CommandResult, error) {
	reference := arguments[len(arguments)-1]
	if arguments[0] == "pull" && (runner.failAll || strings.HasPrefix(reference, "rockchin/langbot@")) {
		runner.calls = append(runner.calls, "docker "+strings.Join(arguments, " "))
		return deploy.CommandResult{}, errors.New("image source unavailable")
	}
	if arguments[0] == "image" && strings.Contains(strings.Join(arguments, " "), "{{json .RepoDigests}}") {
		value, _ := json.Marshal([]string{reference})
		return deploy.CommandResult{Stdout: string(value)}, nil
	}
	return runner.upgradeRunner.Docker(ctx, arguments...)
}

type pinnedUpgradeTransport struct {
	registry    *httptest.Server
	runner      *upgradeRunner
	composeFile string
}

func (transport pinnedUpgradeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if strings.HasPrefix(request.URL.Path, "/v2/") {
		copy := request.Clone(request.Context())
		copy.Header.Set("X-Registry", request.URL.Host)
		copy.URL.Scheme = "http"
		copy.URL.Host = strings.TrimPrefix(transport.registry.URL, "http://")
		return http.DefaultTransport.RoundTrip(copy)
	}
	if !transport.runner.running {
		return nil, errors.New("stopped")
	}
	content, err := os.ReadFile(transport.composeFile)
	if err != nil {
		return nil, err
	}
	image, err := deploy.ComposeSourceImage(content)
	if err != nil {
		return nil, err
	}
	version := strings.TrimPrefix(image, "rockchin/langbot:")
	for _, candidate := range []string{"v4.10.10", "v4.10.11"} {
		_, digest := upgradeManifest(candidate)
		if strings.HasSuffix(image, "@"+digest) {
			version = candidate
		}
	}
	body := fmt.Sprintf(`{"code":0,"data":{"version":%q,"edition":"community"},"msg":"ok"}`, version)
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
}

func TestUpgradeReselectsThenPersistsAndReusesPinnedImage(t *testing.T) {
	service, baseRunner, record, dir := upgradeServiceFixture(t, false)
	record.ImageRepositoryExplicit = false
	record.ImageRepository = deploy.DefaultImageRepository
	record.Region = "cn"
	if err := (deploy.Store{Dir: dir}).Write(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	var probes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		host := request.Header.Get("X-Registry")
		if request.URL.Path == "/v2/" {
			probes.Add(1)
		}
		if host != "docker.langbot.app" && host != "docker.1ms.run" {
			writer.WriteHeader(503)
			return
		}
		if request.URL.Path == "/v2/" {
			writer.WriteHeader(200)
			return
		}
		parts := strings.Split(request.URL.Path, "/")
		body, digest := upgradeManifest(parts[len(parts)-1])
		writer.Header().Set("Docker-Content-Digest", digest)
		_, _ = writer.Write([]byte(body))
	}))
	defer server.Close()
	transport := pinnedUpgradeTransport{server, baseRunner, record.ComposeFile}
	runner := &pinnedUpgradeRunner{upgradeRunner: baseRunner}
	service.deps.LocalRunner = runner
	service.deps.LocalHTTP = &http.Client{Transport: transport}
	service.deps.Transport = transport
	for _, version := range []string{"v4.10.10", "v4.10.11"} {
		value, err := service.Upgrade(context.Background(), UpgradeOptions{CheckOptions: CheckOptions{Endpoint: record.Endpoint, EndpointSet: true}, Version: version, Yes: true})
		if err != nil {
			t.Fatalf("upgrade %s: %v (%+v)", version, err, value)
		}
		updated, err := (deploy.Store{Dir: dir}).Read()
		_, digest := upgradeManifest(version)
		if err != nil || updated.ImageRepository != "docker.1ms.run/rockchin/langbot" || updated.Asset.Digest != digest || updated.Asset.Name != updated.ImageRepository+"@"+digest {
			t.Fatalf("record=%+v err=%v", updated, err)
		}
		if err := deploy.ValidateOwnedLayout(updated, dir); err != nil {
			t.Fatal(err)
		}
		if probes.Load() != int32(len(deploy.PublicImageRepositories())) {
			t.Fatalf("unexpected reprobe: %d", probes.Load())
		}
	}
	runner.failAll = true
	ctx := telemetry.TrackSteps(context.Background())
	_, err := service.Upgrade(ctx, UpgradeOptions{CheckOptions: CheckOptions{Endpoint: record.Endpoint, EndpointSet: true}, Version: "v4.10.12", Yes: true})
	if err == nil || telemetry.CurrentStep(ctx) != "image_pull" || !baseRunner.running {
		t.Fatalf("failure=%v step=%s running=%v", err, telemetry.CurrentStep(ctx), baseRunner.running)
	}
}

package deploy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testImageManifest = `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[]}`

func testImageDigest() string {
	sum := sha256.Sum256([]byte(testImageManifest))
	return "sha256:" + hex.EncodeToString(sum[:])
}

type imageRegistryTransport struct {
	transport http.RoundTripper
	host      string
}

func (transport imageRegistryTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	copy := request.Clone(request.Context())
	copy.Header.Set("X-Registry", request.URL.Host)
	copy.URL.Host = transport.host
	response, err := transport.transport.RoundTrip(copy)
	if response != nil {
		response.Request = request
	}
	return response, err
}

func testImageSources(t *testing.T, handler http.HandlerFunc) ImageSources {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	return ImageSources{HTTP: &http.Client{Transport: imageRegistryTransport{server.Client().Transport, strings.TrimPrefix(server.URL, "https://")}}}
}

func registryResponse(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path == "/v2/" {
		writer.WriteHeader(http.StatusOK)
		return
	}
	writer.Header().Set("Docker-Content-Digest", testImageDigest())
	writer.Header().Set("Content-Type", "application/vnd.oci.image.index.v1+json")
	writer.WriteHeader(http.StatusOK)
	if request.Method != http.MethodHead {
		_, _ = writer.Write([]byte(testImageManifest))
	}
}

type imagePullRunner struct {
	calls          []string
	fail, mismatch map[string]bool
}

func (runner *imagePullRunner) Docker(_ context.Context, arguments ...string) (CommandResult, error) {
	reference := arguments[len(arguments)-1]
	if arguments[0] == "pull" {
		runner.calls = append(runner.calls, reference)
		if runner.fail[strings.Split(reference, "@")[0]] {
			return CommandResult{}, errors.New("pull failed")
		}
		return CommandResult{}, nil
	}
	if !strings.Contains(reference, "@") {
		reference = reference[:strings.LastIndex(reference, ":")] + "@" + testImageDigest()
	}
	if runner.mismatch[strings.Split(reference, "@")[0]] {
		reference = strings.Split(reference, "@")[0] + "@sha256:" + strings.Repeat("0", 64)
	}
	data, _ := json.Marshal([]string{reference})
	return CommandResult{Stdout: string(data)}, nil
}

func (*imagePullRunner) Compose(context.Context, Record, ...string) (CommandResult, error) {
	return CommandResult{}, nil
}

func TestImageSourceSelectionAndFallback(t *testing.T) {
	for _, test := range []struct {
		name                               string
		unavailable, mismatch, pullFailure bool
		expected                           string
	}{
		{name: "fastest", expected: "docker.1ms.run/rockchin/langbot"},
		{name: "partial failure", unavailable: true, expected: "docker.m.daocloud.io/rockchin/langbot"},
		{name: "digest mismatch", mismatch: true, expected: "docker.m.daocloud.io/rockchin/langbot"},
		{name: "pull failure", pullFailure: true, expected: "docker.m.daocloud.io/rockchin/langbot"},
	} {
		t.Run(test.name, func(t *testing.T) {
			sources := testImageSources(t, func(writer http.ResponseWriter, request *http.Request) {
				host := request.Header.Get("X-Registry")
				if host == "docker.m.daocloud.io" {
					time.Sleep(40 * time.Millisecond)
				}
				if host != "docker.langbot.app" && host != "docker.m.daocloud.io" && host != "docker.1ms.run" || (host == "docker.1ms.run" && test.unavailable) {
					writer.WriteHeader(503)
					return
				}
				registryResponse(writer, request)
			})
			runner := &imagePullRunner{mismatch: map[string]bool{"docker.1ms.run/rockchin/langbot": test.mismatch}, fail: map[string]bool{"docker.1ms.run/rockchin/langbot": test.pullFailure}}
			selection, err := sources.Pull(context.Background(), runner, Record{Region: "cn"}, "v4.10.10", false, nil)
			if err != nil || selection.Repository != test.expected || selection.Reference != test.expected+"@"+testImageDigest() {
				t.Fatalf("selection=%+v err=%v", selection, err)
			}
		})
	}
}

func TestImageSourcesAllUnavailableUseCanonical(t *testing.T) {
	sources := testImageSources(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-Registry") != "docker.langbot.app" {
			writer.WriteHeader(503)
			return
		}
		registryResponse(writer, request)
	})
	selection, err := sources.Pull(context.Background(), &imagePullRunner{}, Record{Region: "cn"}, "v4.10.10", false, nil)
	if err != nil || selection.Repository != CanonicalImageRepository {
		t.Fatalf("%+v %v", selection, err)
	}
}

func TestImageSourcesExplicitNeverProbeOrFallback(t *testing.T) {
	var requests atomic.Int32
	sources := testImageSources(t, func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		registryResponse(writer, request)
	})
	for _, repository := range []string{DefaultImageRepository, "custom.example/langbot"} {
		runner := &imagePullRunner{fail: map[string]bool{repository + ":v4.10.10": true}}
		_, err := sources.Pull(context.Background(), runner, Record{ImageRepository: repository, ImageRepositoryExplicit: true}, "v4.10.10", false, nil)
		if err == nil || len(runner.calls) != 1 || requests.Load() != 0 {
			t.Fatalf("err=%v pulls=%v requests=%d", err, runner.calls, requests.Load())
		}
	}
}

func TestExplicitRepositoryPinsInspectedDigest(t *testing.T) {
	var requests atomic.Int32
	sources := testImageSources(t, func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		registryResponse(writer, request)
	})
	repository := "custom.example/langbot"
	selection, err := sources.Pull(context.Background(), &imagePullRunner{}, Record{ImageRepository: repository, ImageRepositoryExplicit: true}, "v4.11.7", false, nil)
	if err != nil || selection.Reference != repository+"@"+testImageDigest() || requests.Load() != 0 {
		t.Fatalf("%+v err=%v requests=%d", selection, err, requests.Load())
	}
}

func TestImageSourcesReuseAndReselect(t *testing.T) {
	var probes atomic.Int32
	sources := testImageSources(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v2/" {
			probes.Add(1)
		}
		if request.Header.Get("X-Registry") == "docker.1ms.run" {
			registryResponse(writer, request)
			return
		}
		if request.Header.Get("X-Registry") == "docker.langbot.app" {
			registryResponse(writer, request)
			return
		}
		writer.WriteHeader(503)
	})
	record := Record{Region: "cn", ImageRepository: "docker.m.daocloud.io/rockchin/langbot"}
	runner := &imagePullRunner{}
	selection, err := sources.Pull(context.Background(), runner, record, "v4.10.10", true, nil)
	if err != nil || selection.Repository != record.ImageRepository || probes.Load() != 0 {
		t.Fatalf("%+v %v probes=%d", selection, err, probes.Load())
	}
	runner.fail = map[string]bool{record.ImageRepository: true}
	selection, err = sources.Pull(context.Background(), runner, record, "v4.10.11", true, nil)
	if err != nil || selection.Repository != "docker.1ms.run/rockchin/langbot" || probes.Load() != 5 {
		t.Fatalf("%+v %v probes=%d", selection, err, probes.Load())
	}
}

func TestImageSourcesGlobalHubAndFallback(t *testing.T) {
	for _, hubAvailable := range []bool{true, false} {
		sources := testImageSources(t, func(writer http.ResponseWriter, request *http.Request) {
			host := request.Header.Get("X-Registry")
			if host == "registry-1.docker.io" && !hubAvailable || host != "registry-1.docker.io" && host != "docker.langbot.app" && host != "docker.1ms.run" {
				writer.WriteHeader(503)
				return
			}
			registryResponse(writer, request)
		})
		selection, err := sources.Pull(context.Background(), &imagePullRunner{}, Record{Region: "global"}, "v4.10.10", false, nil)
		expected := "docker.1ms.run/rockchin/langbot"
		if hubAvailable {
			expected = DefaultImageRepository
		}
		if err != nil || selection.Repository != expected {
			t.Fatalf("%+v %v", selection, err)
		}
	}
}

func TestImageSourcesRequireTrustedDigest(t *testing.T) {
	sources := testImageSources(t, func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Docker-Content-Digest", "sha256:"+strings.Repeat("0", 64))
		_, _ = writer.Write([]byte(testImageManifest))
	})
	runner := &imagePullRunner{}
	_, err := sources.Pull(context.Background(), runner, Record{Region: "cn"}, "v4.10.10", false, nil)
	if err == nil || len(runner.calls) != 0 {
		t.Fatalf("err=%v pulls=%v", err, runner.calls)
	}
}

func TestExplicitPublicMirrorVerifiesDigestWithoutFallback(t *testing.T) {
	var probes atomic.Int32
	sources := testImageSources(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v2/" {
			probes.Add(1)
		}
		registryResponse(writer, request)
	})
	repository := "docker.1ms.run/rockchin/langbot"
	runner := &imagePullRunner{mismatch: map[string]bool{repository: true}}
	_, err := sources.Pull(context.Background(), runner, Record{ImageRepository: repository, ImageRepositoryExplicit: true}, "v4.11.7", false, nil)
	if err == nil || len(runner.calls) != 1 || runner.calls[0] != repository+"@"+testImageDigest() || probes.Load() != 0 {
		t.Fatalf("err=%v pulls=%v probes=%d", err, runner.calls, probes.Load())
	}
}

func TestImageProbeBearerAuthentication(t *testing.T) {
	sources := testImageSources(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/token" {
			if request.URL.Query().Get("scope") != "repository:rockchin/langbot:pull" {
				t.Error("incorrect token scope")
			}
			_, _ = writer.Write([]byte(`{"token":"public-token"}`))
			return
		}
		if request.Header.Get("Authorization") != "Bearer public-token" {
			writer.Header().Set("WWW-Authenticate", `Bearer realm="https://auth.docker.io/token",service="registry.docker.io"`)
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		registryResponse(writer, request)
	})
	if probe := sources.Probe(context.Background(), DefaultImageRepository, "v4.10.10"); !probe.Available {
		t.Fatal(probe)
	}
}

func TestImageProbeAuthenticatesRedirectDestination(t *testing.T) {
	sources := testImageSources(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-Registry") == "hub.rat.dev" {
			http.Redirect(writer, request, "https://docker.1ms.run"+request.URL.Path, http.StatusFound)
			return
		}
		if request.URL.Path == "/token" {
			_, _ = writer.Write([]byte(`{"access_token":"public-token"}`))
			return
		}
		if request.Header.Get("Authorization") != "Bearer public-token" {
			writer.Header().Set("WWW-Authenticate", `Bearer realm="https://docker.1ms.run/token",service="docker.1ms.run"`)
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		registryResponse(writer, request)
	})
	if probe := sources.Probe(context.Background(), "hub.rat.dev/rockchin/langbot", "v4.11.7"); !probe.Available {
		t.Fatal(probe)
	}
}

func TestImageProbeSharesThreeSecondDeadline(t *testing.T) {
	sources := testImageSources(t, func(writer http.ResponseWriter, request *http.Request) {
		select {
		case <-request.Context().Done():
			return
		case <-time.After(2 * time.Second):
			registryResponse(writer, request)
		}
	})
	started := time.Now()
	probes := sources.ProbeAll(context.Background(), PublicImageRepositories(), "v4.10.10")
	if elapsed := time.Since(started); elapsed > 3500*time.Millisecond {
		t.Fatalf("deadline exceeded: %s", elapsed)
	}
	for _, probe := range probes {
		if probe.Available {
			t.Fatal(probe)
		}
	}
}

func TestPinnedImageSupportsRepeatedUpgrade(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "deployment")
	assets, err := NewInstallAssets(dir, "v4.11.7", "basic", 15322, InstallNetworkOptions{})
	if err != nil {
		t.Fatal(err)
	}
	repository := "docker.1ms.run/rockchin/langbot"
	assets.SetImageReference(repository + "@" + testImageDigest())
	if err := assets.Write(); err != nil {
		t.Fatal(err)
	}
	record := assets.Record("pinned", testImageDigest())
	record.ImageRepository = repository
	for _, version := range []string{"v4.11.8", "v4.11.9"} {
		if err := ValidateOwnedLayout(record, dir); err != nil {
			t.Fatal(err)
		}
		compose, _, err := ComposeForUpgrade(record, dir, version)
		if err != nil {
			t.Fatal(err)
		}
		compose = PinComposeImage(compose, repository+":"+version, repository+"@"+testImageDigest())
		if strings.Count(string(compose), "image: "+repository+"@"+testImageDigest()) != 3 {
			t.Fatalf("not all services pinned: %s", compose)
		}
		if err := os.WriteFile(record.ComposeFile, compose, 0600); err != nil {
			t.Fatal(err)
		}
		record.CoreVersion = version
	}
}

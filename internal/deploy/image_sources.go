package deploy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/langbot-app/langbot-cli/internal/i18n"
	"github.com/langbot-app/langbot-cli/internal/telemetry"
)

const CanonicalImageRepository = "docker.langbot.app/langbot-public/rockchin/langbot"

const manifestAccept = "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json"

var digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
var authParameter = regexp.MustCompile(`([a-z]+)="([^"]*)"`)

func PublicImageRepositories() []string {
	return []string{
		"docker.m.daocloud.io/rockchin/langbot",
		"docker.1ms.run/rockchin/langbot",
		"dockerproxy.net/rockchin/langbot",
	}
}

type ImageProbe struct {
	Repository string `json:"repository"`
	Available  bool   `json:"available"`
	LatencyMS  int64  `json:"latency_ms"`
	Reason     string `json:"reason"`
}

type ImageSelection struct {
	Repository string `json:"repository"`
	Reference  string `json:"reference"`
	Digest     string `json:"digest"`
	Reason     string `json:"reason"`
	LatencyMS  int64  `json:"latency_ms"`
	Verified   bool   `json:"verified"`
}

type ImageSources struct {
	HTTP *http.Client
}

func imageRegistry(repository string) (string, string) {
	if repository == DefaultImageRepository {
		return "https://registry-1.docker.io", repository
	}
	host, path, _ := strings.Cut(repository, "/")
	return "https://" + host, path
}

func (sources ImageSources) request(ctx context.Context, method, repository, path string) (*http.Response, error) {
	base, repo := imageRegistry(repository)
	telemetry.RecordSource(ctx, base+"/v2/")
	client := sources.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	request, err := http.NewRequestWithContext(ctx, method, base+path, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", manifestAccept)
	response, err := client.Do(request)
	if err != nil || response.StatusCode != http.StatusUnauthorized {
		return response, err
	}
	challenge := response.Header.Get("WWW-Authenticate")
	request = response.Request.Clone(ctx)
	response.Body.Close()
	if !strings.HasPrefix(strings.ToLower(challenge), "bearer ") {
		return nil, errors.New(i18n.T("Image registry authentication failed"))
	}
	parameters := map[string]string{}
	for _, match := range authParameter.FindAllStringSubmatch(challenge, -1) {
		parameters[match[1]] = match[2]
	}
	realm, err := url.Parse(parameters["realm"])
	if err != nil || realm.Scheme != "https" || realm.Host == "" {
		return nil, errors.New(i18n.T("Image registry authentication failed"))
	}
	query := realm.Query()
	query.Set("service", parameters["service"])
	query.Set("scope", "repository:"+repo+":pull")
	realm.RawQuery = query.Encode()
	tokenRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, realm.String(), nil)
	if err != nil {
		return nil, err
	}
	tokenResponse, err := client.Do(tokenRequest)
	if err != nil {
		return nil, err
	}
	defer tokenResponse.Body.Close()
	var token struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if tokenResponse.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(tokenResponse.Body, 1<<20)).Decode(&token) != nil {
		return nil, errors.New(i18n.T("Image registry authentication failed"))
	}
	if token.Token == "" {
		token.Token = token.AccessToken
	}
	if token.Token == "" {
		return nil, errors.New(i18n.T("Image registry authentication failed"))
	}
	request.Header.Set("Authorization", "Bearer "+token.Token)
	return client.Do(request)
}

func (sources ImageSources) Probe(ctx context.Context, repository, tag string) (probe ImageProbe) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	started := time.Now()
	probe = ImageProbe{Repository: repository, Reason: i18n.T("Image registry unavailable")}
	defer func() { probe.LatencyMS = time.Since(started).Milliseconds() }()
	base, _ := imageRegistry(repository)
	telemetry.RecordSource(ctx, base+"/v2/")
	client := sources.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v2/", nil)
	response, err := client.Do(request)
	if err != nil {
		return probe
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusUnauthorized {
		return probe
	}
	_, repo := imageRegistry(repository)
	response, err = sources.request(ctx, http.MethodHead, repository, "/v2/"+repo+"/manifests/"+tag)
	if err != nil {
		return probe
	}
	defer response.Body.Close()
	probe.LatencyMS = time.Since(started).Milliseconds()
	probe.Available = response.StatusCode == http.StatusOK && digestPattern.MatchString(response.Header.Get("Docker-Content-Digest"))
	if probe.Available {
		probe.Reason = i18n.T("Image registry available")
	}
	return probe
}

func (sources ImageSources) ProbeAll(ctx context.Context, repositories []string, tag string) []ImageProbe {
	probes := make([]ImageProbe, len(repositories))
	var group sync.WaitGroup
	for index, repository := range repositories {
		group.Add(1)
		go func() { defer group.Done(); probes[index] = sources.Probe(ctx, repository, tag) }()
	}
	group.Wait()
	return probes
}

func (sources ImageSources) trustedDigest(ctx context.Context, tag string) (string, error) {
	for _, repository := range []string{CanonicalImageRepository, DefaultImageRepository} {
		digest, err := sources.manifestDigest(ctx, repository, tag)
		if err == nil {
			return digest, nil
		}
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
	}
	return "", errors.New(i18n.T("Unable to resolve a trusted image digest"))
}

func (sources ImageSources) manifestDigest(ctx context.Context, repository, tag string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, repo := imageRegistry(repository)
	response, err := sources.request(ctx, http.MethodGet, repository, "/v2/"+repo+"/manifests/"+tag)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", errors.New(i18n.T("Image registry unavailable"))
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return "", err
	}
	var manifest struct {
		SchemaVersion int    `json:"schemaVersion"`
		MediaType     string `json:"mediaType"`
	}
	if json.Unmarshal(content, &manifest) != nil || manifest.SchemaVersion != 2 || !validManifestType(manifest.MediaType) {
		return "", errors.New(i18n.T("Invalid image manifest"))
	}
	sum := sha256.Sum256(content)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	if declared := response.Header.Get("Docker-Content-Digest"); declared != "" && declared != digest {
		return "", errors.New(i18n.T("Image digest verification failed"))
	}
	return digest, nil
}

func validManifestType(mediaType string) bool {
	for _, allowed := range strings.Split(manifestAccept, ", ") {
		if mediaType == allowed {
			return true
		}
	}
	return false
}

func ExplicitImageRepository(record Record) bool {
	if record.ImageRepositoryExplicit {
		return true
	}
	if record.ImageRepository == "" || record.ImageRepository == DefaultImageRepository || record.ImageRepository == CanonicalImageRepository || record.ImageRepository == "docker.langbot.app/rockchin/langbot" {
		return false
	}
	for _, repository := range PublicImageRepositories() {
		if repository == record.ImageRepository {
			return false
		}
	}
	return true
}

func publicImageRepository(repository string) bool {
	for _, candidate := range PublicImageRepositories() {
		if repository == candidate {
			return true
		}
	}
	return false
}

func (sources ImageSources) Pull(ctx context.Context, runner Runner, record Record, tag string, reuse bool, notify func(ImageSelection)) (ImageSelection, error) {
	explicit := ExplicitImageRepository(record)
	if explicit && !publicImageRepository(record.ImageRepository) {
		selection := ImageSelection{Repository: ImageRepository(record), Reference: ImageRepository(record) + ":" + tag, Reason: i18n.T("Explicit image repository")}
		if _, err := runner.Docker(ctx, "pull", selection.Reference); err != nil {
			telemetry.ImagePullFailed(ctx)
			return selection, errors.New(i18n.T("Image pull failed"))
		}
		inspected, err := runner.Docker(ctx, "image", "inspect", "--format", "{{json .RepoDigests}}", selection.Reference)
		var digests []string
		if err == nil && json.Unmarshal([]byte(inspected.Stdout), &digests) == nil {
			for _, value := range digests {
				repository, digest, _ := strings.Cut(value, "@")
				if strings.TrimPrefix(repository, "docker.io/") == strings.TrimPrefix(selection.Repository, "docker.io/") && digestPattern.MatchString(digest) {
					selection.Digest = digest
					break
				}
			}
		}
		if err != nil || !digestPattern.MatchString(selection.Digest) {
			telemetry.ImagePullFailed(ctx)
			return selection, errors.New(i18n.T("Image digest verification failed"))
		}
		if !IsAdoptedLayout(record) {
			selection.Reference = selection.Repository + "@" + selection.Digest
		}
		if notify != nil {
			notify(selection)
		}
		return selection, nil
	}
	digest, err := sources.trustedDigest(ctx, tag)
	if err != nil {
		return ImageSelection{}, err
	}
	attempted := map[string]bool{}
	try := func(repository, reason string, latency int64) (ImageSelection, bool) {
		selection := ImageSelection{Repository: repository, Reference: repository + "@" + digest, Digest: digest, Reason: reason, LatencyMS: latency}
		if attempted[repository] || ctx.Err() != nil {
			return selection, false
		}
		attempted[repository] = true
		if _, err := runner.Docker(ctx, "pull", selection.Reference); err != nil {
			telemetry.ImagePullFailed(ctx)
			return selection, false
		}
		inspected, err := runner.Docker(ctx, "image", "inspect", "--format", "{{json .RepoDigests}}", selection.Reference)
		var digests []string
		if err != nil || json.Unmarshal([]byte(inspected.Stdout), &digests) != nil {
			telemetry.ImagePullFailed(ctx)
			return selection, false
		}
		for _, value := range digests {
			if value == selection.Reference {
				selection.Verified = true
				if notify != nil {
					notify(selection)
				}
				return selection, true
			}
		}
		telemetry.ImagePullFailed(ctx)
		return selection, false
	}
	if explicit {
		selection, ok := try(record.ImageRepository, i18n.T("Explicit image repository"), 0)
		if ok {
			return selection, nil
		}
		return selection, errors.New(i18n.T("Image digest verification or pull failed"))
	}
	if reuse && record.ImageRepository != "" {
		if selection, ok := try(record.ImageRepository, i18n.T("Saved image repository"), 0); ok {
			return selection, nil
		}
	}
	if record.Region != "cn" && !attempted[DefaultImageRepository] {
		probe := sources.Probe(ctx, DefaultImageRepository, tag)
		if probe.Available {
			if selection, ok := try(DefaultImageRepository, i18n.T("Docker Hub available"), probe.LatencyMS); ok {
				return selection, nil
			}
		}
	}
	probes := sources.ProbeAll(ctx, PublicImageRepositories(), tag)
	sort.SliceStable(probes, func(left, right int) bool { return probes[left].LatencyMS < probes[right].LatencyMS })
	for _, probe := range probes {
		if !probe.Available {
			continue
		}
		if selection, ok := try(probe.Repository, i18n.T("Automatic image repository"), probe.LatencyMS); ok {
			return selection, nil
		}
	}
	if selection, ok := try(CanonicalImageRepository, i18n.T("Official fallback image repository"), 0); ok {
		return selection, nil
	}
	return ImageSelection{}, errors.New(i18n.T("Image pull or digest verification failed for all repositories"))
}

func ImageSelectionMessage(selection ImageSelection) string {
	if !selection.Verified {
		return fmt.Sprintf(i18n.T("Image repository %s (%s)"), selection.Repository, selection.Reason)
	}
	if selection.LatencyMS == 0 {
		return fmt.Sprintf(i18n.T("Image repository %s (%s, digest verified)"), selection.Repository, selection.Reason)
	}
	return fmt.Sprintf(i18n.T("Image repository %s (%s, %d ms, digest verified)"), selection.Repository, selection.Reason, selection.LatencyMS)
}

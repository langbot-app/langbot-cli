package deploy

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
	"github.com/langbot-app/langbot-cli/internal/i18n"
	"github.com/langbot-app/langbot-cli/internal/telemetry"
)

const DownloadURL = "https://download.langbot.app"

type Updater struct {
	BaseURL    string
	Client     *http.Client
	Executable string
	Platform   string
}

type UpdateResult struct {
	Current   string `json:"current" yaml:"current"`
	Latest    string `json:"latest" yaml:"latest"`
	Available bool   `json:"available" yaml:"available"`
	Updated   bool   `json:"updated" yaml:"updated"`
	Command   string `json:"command,omitempty" yaml:"command,omitempty"`
}

func (updater Updater) client() *http.Client {
	if updater.Client != nil {
		return updater.Client
	}
	return &http.Client{Timeout: 2 * time.Minute}
}

func (updater Updater) base() string {
	if updater.BaseURL != "" {
		return strings.TrimRight(updater.BaseURL, "/")
	}
	return DownloadURL
}

func (updater Updater) Latest(ctx context.Context) (string, error) {
	data, err := Fetch(ctx, updater.client(), updater.base()+"/latest.json", 65536)
	if err != nil {
		return "", err
	}
	var payload struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", err
	}
	if !releaseVersionPattern.MatchString(payload.Version) {
		return "", errors.New(i18n.T("invalid lbctl release version"))
	}
	return payload.Version, nil
}

func (updater Updater) Update(ctx context.Context, current string, check bool) (UpdateResult, error) {
	telemetry.SetStep(ctx, "resolve_version")
	latest, err := updater.Latest(ctx)
	if err != nil {
		return UpdateResult{}, err
	}
	result := UpdateResult{Current: current, Latest: latest}
	comparison, err := CompareReleaseVersions("v"+strings.TrimPrefix(current, "v"), latest)
	result.Available = err != nil || comparison < 0
	if check || !result.Available {
		return result, nil
	}
	binary := updater.Executable
	if binary == "" {
		binary, err = os.Executable()
		if err != nil {
			return result, err
		}
	}
	if resolved, err := filepath.EvalSymlinks(binary); err == nil {
		binary = resolved
	}
	if command := packageUpgradeCommand(ctx, binary); command != "" {
		result.Command = command
		return result, nil
	}
	platform := updater.Platform
	if platform == "" {
		platform = CurrentPlatform()
	}
	if _, ok := UVAssets[platform]; !ok {
		return result, errors.New(i18n.T("unsupported update platform"))
	}
	asset := "lbctl_" + strings.ReplaceAll(platform, "/", "_")
	telemetry.SetStep(ctx, "download_update")
	if strings.HasPrefix(platform, "windows/") {
		asset += ".exe"
	}
	base := updater.base() + "/releases/" + latest + "/"
	manifest, err := Fetch(ctx, updater.client(), base+"checksums.txt", 1<<20)
	if err != nil {
		return result, err
	}
	checksums := make(map[string]string)
	for _, line := range strings.Split(string(manifest), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && len(fields[0]) == 64 {
			checksums[strings.TrimPrefix(fields[1], "*")] = strings.ToLower(fields[0])
		}
	}
	expected, ok := checksums[asset]
	if !ok {
		return result, fmt.Errorf(i18n.T("release checksum absent for %s"), asset)
	}
	download := asset
	if _, ok := checksums[asset+".gz"]; ok {
		download += ".gz"
	}
	const maxBinarySize = 200 << 20
	data, err := Fetch(ctx, updater.client(), base+download, maxBinarySize)
	if err != nil {
		return result, err
	}
	if download != asset {
		sum := sha256.Sum256(data)
		if hex.EncodeToString(sum[:]) != checksums[download] {
			return result, errors.New(i18n.T("lbctl checksum mismatch"))
		}
		reader, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return result, err
		}
		defer reader.Close()
		data, err = io.ReadAll(io.LimitReader(reader, maxBinarySize+1))
		if err != nil {
			return result, err
		}
		if len(data) > maxBinarySize {
			return result, errors.New(i18n.T("download exceeds size limit"))
		}
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != expected {
		return result, errors.New(i18n.T("lbctl checksum mismatch"))
	}
	telemetry.SetStep(ctx, "replace_binary")
	info, err := os.Stat(binary)
	if err != nil {
		return result, err
	}
	if runtime.GOOS == "windows" {
		staged := binary + ".new"
		if err := writeAtomic(staged, data, info.Mode()); err != nil {
			return result, err
		}
		defer os.Remove(staged)
		old := binary + ".old"
		if err := os.Remove(old); err != nil && !os.IsNotExist(err) {
			return result, err
		}
		if err := os.Rename(binary, old); err != nil {
			return result, err
		}
		if err := os.Rename(staged, binary); err != nil {
			_ = os.Rename(old, binary)
			return result, err
		}
	} else if err := writeAtomic(binary, data, info.Mode()); err != nil {
		return result, err
	}
	result.Updated = true
	return result, nil
}

func packageUpgradeCommand(ctx context.Context, binary string) string {
	normalized := filepath.ToSlash(binary)
	if strings.Contains(normalized, "/Cellar/") || strings.Contains(normalized, "/homebrew/") {
		return "brew upgrade lbctl"
	}
	if strings.Contains(strings.ToLower(normalized), "/scoop/") {
		return "scoop update lbctl"
	}
	if strings.Contains(strings.ToLower(normalized), "/chocolatey/") {
		return "choco upgrade lbctl"
	}
	if strings.HasPrefix(normalized, "/nix/store/") {
		return "nix profile upgrade lbctl"
	}
	for _, manager := range []struct {
		name    string
		args    []string
		command string
	}{
		{"dpkg-query", []string{"-S", binary}, "sudo apt upgrade lbctl"},
		{"rpm", []string{"-qf", binary}, "sudo dnf upgrade lbctl"},
		{"pacman", []string{"-Qo", binary}, "sudo pacman -Syu lbctl"},
	} {
		if exec.CommandContext(ctx, manager.name, manager.args...).Run() == nil {
			return manager.command
		}
	}
	return ""
}

type UpdateCache struct {
	LBCTLCheckedAt time.Time         `json:"lbctl_checked_at"`
	CoreCheckedAt  time.Time         `json:"core_checked_at"`
	LBCTL          string            `json:"lbctl"`
	Core           string            `json:"core"`
	Instances      map[string]string `json:"instances"`
}

func (cache UpdateCache) NeedsRefresh() bool {
	return cache.LBCTL == "" || cache.Core == "" || time.Since(cache.LBCTLCheckedAt) >= 24*time.Hour || time.Since(cache.CoreCheckedAt) >= 24*time.Hour
}

func UpdateCachePath() (string, error) {
	base, err := os.UserConfigDir()
	return filepath.Join(base, "langbot", "update-check.json"), err
}

func ReadUpdateCache() UpdateCache {
	var cache UpdateCache
	path, err := UpdateCachePath()
	if err != nil {
		return cache
	}
	data, err := os.ReadFile(path)
	if err == nil {
		_ = json.Unmarshal(data, &cache)
	}
	return cache
}

func RefreshUpdateCache(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return RefreshUpdateCacheWith(ctx, (Updater{}).Latest, func(ctx context.Context) (string, error) {
		return LatestCore(ctx, nil)
	})
}

func RefreshUpdateCacheWith(ctx context.Context, latestLBCTL, latestCore func(context.Context) (string, error)) error {
	path, err := UpdateCachePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	lock := flock.New(path + ".lock")
	locked, err := lock.TryLock()
	if err != nil {
		return err
	}
	if !locked {
		return nil
	}
	defer lock.Unlock()
	cache := ReadUpdateCache()
	if !cache.NeedsRefresh() {
		return nil
	}
	var lbctl, core string
	var lbctlErr, coreErr error
	var checks sync.WaitGroup
	checks.Add(2)
	go func() {
		defer checks.Done()
		if cache.LBCTL == "" || time.Since(cache.LBCTLCheckedAt) >= 24*time.Hour {
			lbctl, lbctlErr = latestLBCTL(ctx)
		}
	}()
	go func() {
		defer checks.Done()
		if cache.Core == "" || time.Since(cache.CoreCheckedAt) >= 24*time.Hour {
			core, coreErr = latestCore(ctx)
		}
	}()
	checks.Wait()
	if lbctlErr != nil && coreErr != nil {
		return errors.Join(lbctlErr, coreErr)
	}
	if lbctlErr == nil && lbctl != "" {
		cache.LBCTL = lbctl
		cache.LBCTLCheckedAt = time.Now().UTC()
	}
	if coreErr == nil && core != "" {
		cache.Core = core
		cache.CoreCheckedAt = time.Now().UTC()
	}
	cache.Instances = map[string]string{}
	if dirs, err := (Locator{}).Directories(); err == nil {
		for _, dir := range dirs {
			if record, err := (Store{Dir: dir}).Read(); err == nil {
				cache.Instances[dir] = record.CoreVersion
			}
		}
	}
	data, err := json.Marshal(cache)
	if err != nil {
		return err
	}
	return writeAtomic(path, data, 0o600)
}

func StartUpdateRefresh() error {
	if !ReadUpdateCache().NeedsRefresh() {
		return nil
	}
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	command := exec.Command(binary, "_update-check")
	detachNative(command)
	if err := command.Start(); err != nil {
		return err
	}
	return command.Process.Release()
}

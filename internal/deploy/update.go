package deploy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/gofrs/flock"
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
		return "", fmt.Errorf("invalid lbctl release version")
	}
	return payload.Version, nil
}

func (updater Updater) Update(ctx context.Context, current string, check bool) (UpdateResult, error) {
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
		return result, fmt.Errorf("unsupported update platform")
	}
	asset := "lbctl_" + strings.ReplaceAll(platform, "/", "_")
	if strings.HasPrefix(platform, "windows/") {
		asset += ".exe"
	}
	base := updater.base() + "/releases/" + latest + "/"
	manifest, err := Fetch(ctx, updater.client(), base+"checksums.txt", 1<<20)
	if err != nil {
		return result, err
	}
	expected := ""
	for _, line := range strings.Split(string(manifest), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == asset {
			expected = strings.ToLower(fields[0])
			break
		}
	}
	if len(expected) != 64 {
		return result, fmt.Errorf("release checksum absent for %s", asset)
	}
	data, err := Fetch(ctx, updater.client(), base+asset, 200<<20)
	if err != nil {
		return result, err
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != expected {
		return result, fmt.Errorf("lbctl checksum mismatch")
	}
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
	CheckedAt time.Time         `json:"checked_at"`
	LBCTL     string            `json:"lbctl"`
	Core      string            `json:"core"`
	Instances map[string]string `json:"instances"`
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
	if time.Since(cache.CheckedAt) < 24*time.Hour {
		return nil
	}
	cache.CheckedAt = time.Now().UTC()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if latest, err := (Updater{}).Latest(ctx); err == nil {
		cache.LBCTL = latest
	}
	if latest, err := LatestCore(ctx, nil); err == nil {
		cache.Core = latest
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
	if time.Since(ReadUpdateCache().CheckedAt) < 24*time.Hour {
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

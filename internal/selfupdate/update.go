package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/langbot-app/langbot-cli/internal/deploy"
	"github.com/langbot-app/langbot-cli/internal/result"
)

const repository = "langbot-app/langbot-cli"
const latestReleaseURL = "https://api.github.com/repos/" + repository + "/releases/latest"
const UpdateCommand = "lbctl self update"

var stableVersion = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(\+[0-9A-Za-z.-]+)?$`)

type Updater struct {
	HTTP       *http.Client
	Executable func() (string, error)
	CacheDir   string
}

type Info struct {
	CurrentVersion  string `json:"current_version"`
	LatestVersion   string `json:"latest_version"`
	UpdateAvailable bool   `json:"update_available"`
	Updated         bool   `json:"updated"`
	Executable      string `json:"executable,omitempty"`
	UpdateCommand   string `json:"update_command,omitempty"`
	BackupPath      string `json:"backup_path,omitempty"`
	asset           string
}

type release struct {
	Tag        string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Assets     []struct {
		Name  string `json:"name"`
		State string `json:"state"`
		Size  int64  `json:"size"`
	} `json:"assets"`
}

// Compare stable release cores using the existing deployment version parser.
func newer(current, latest string) (bool, error) {
	if !stableVersion.MatchString(latest) {
		return false, errors.New("invalid stable release tag")
	}
	latestCore := strings.SplitN(latest, "+", 2)[0]
	if _, err := deploy.CompareReleaseVersions(latestCore, latestCore); err != nil {
		return false, err
	}
	base := strings.SplitN(current, "+", 2)[0]
	base, suffix, prerelease := strings.Cut(base, "-")
	comparison, err := deploy.CompareReleaseVersions(base, latestCore)
	if err != nil {
		// Explicit self-update can replace a locally built development binary.
		return true, nil
	}
	return comparison < 0 || (comparison == 0 && prerelease && suffix != ""), nil
}

func (u Updater) Check(ctx context.Context, current string) (Info, error) {
	info := Info{CurrentVersion: current}
	response, err := u.get(ctx, latestReleaseURL)
	if err != nil {
		return info, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
	if err != nil {
		return info, result.New("network", "读取 lbctl 最新版本信息失败")
	}
	var published release
	if len(data) > 2<<20 || json.Unmarshal(data, &published) != nil || published.Draft || published.Prerelease {
		return info, result.New("server", "lbctl Release 信息无效")
	}
	available, err := newer(current, published.Tag)
	if err != nil {
		return info, result.New("server", "lbctl 最新稳定版本号无效")
	}
	info.LatestVersion, info.UpdateAvailable = published.Tag, available
	if !available {
		return info, nil
	}
	if (runtime.GOOS != "darwin" && runtime.GOOS != "linux" && runtime.GOOS != "windows") || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		return info, result.New("incompatible", "当前平台没有 lbctl 发行包")
	}
	info.asset = "lbctl_" + runtime.GOOS + "_" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		info.asset += ".exe"
	}
	ready := map[string]bool{}
	for _, asset := range published.Assets {
		if asset.State == "uploaded" && asset.Size > 0 {
			ready[asset.Name] = true
		}
	}
	if !ready[info.asset] || !ready["checksums.txt"] {
		return info, result.New("precondition", "lbctl 新版本的发行文件仍在准备，请稍后重试")
	}
	info.UpdateCommand = UpdateCommand
	return info, nil
}

func (u Updater) get(ctx context.Context, address string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, result.New("input", "lbctl 更新地址无效")
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "lbctl-self-update")
	client := http.Client{Timeout: 10 * time.Minute}
	if u.HTTP != nil {
		client.Transport = u.HTTP.Transport
	}
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if len(via) >= 10 || !trustedURL(next.URL) {
			return errors.New("untrusted update redirect")
		}
		return nil
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, result.New("network", "连接 lbctl 官方 Release 失败，请检查网络后重试")
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		if response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusTooManyRequests {
			return nil, result.New("network", "GitHub 版本检查受到限流，请稍后重试")
		}
		return nil, result.New("server", "lbctl 官方 Release 暂不可用，请稍后重试")
	}
	return response, nil
}

func trustedURL(address *url.URL) bool {
	if address.Scheme != "https" || address.User != nil || address.Port() != "" {
		return false
	}
	switch address.Hostname() {
	case "github.com", "api.github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com":
		return true
	}
	return false
}

func (u Updater) executable() (string, error) {
	resolve := u.Executable
	if resolve == nil {
		resolve = os.Executable
	}
	path, err := resolve()
	if err != nil {
		return "", result.New("precondition", "无法确定当前 lbctl 可执行文件的位置")
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", result.New("precondition", "无法定位当前 lbctl 可执行文件")
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", result.New("precondition", "lbctl 可执行文件路径无效")
	}
	return path, nil
}

func binaryVersion(ctx context.Context, path string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	data, err := exec.CommandContext(ctx, path, "version", "-o", "json").Output()
	var envelope struct {
		OK   bool `json:"ok"`
		Data struct {
			Version string `json:"version"`
		} `json:"data"`
	}
	if err != nil || json.Unmarshal(data, &envelope) != nil || !envelope.OK || envelope.Data.Version == "" {
		return "", result.New("precondition", "lbctl 二进制版本验证失败，请重新尝试升级")
	}
	return envelope.Data.Version, nil
}

func (u Updater) Update(ctx context.Context, current string) (Info, error) {
	info, err := u.Check(ctx, current)
	if err != nil || !info.UpdateAvailable {
		return info, err
	}
	path, err := u.executable()
	if err != nil {
		return info, err
	}
	info.Executable = path
	lock := flock.New(path + ".update.lock")
	lockCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	locked, err := lock.TryLockContext(lockCtx, 100*time.Millisecond)
	if err != nil || !locked {
		return info, result.New("precondition", "无法获取 lbctl 更新锁，请检查安装目录写权限或稍后重试")
	}
	defer lock.Unlock()
	// Another invocation may have replaced this file while the release was fetched.
	diskVersion, err := binaryVersion(ctx, path)
	if err != nil {
		return info, err
	}
	info.CurrentVersion = diskVersion
	info.UpdateAvailable, _ = newer(diskVersion, info.LatestVersion)
	if !info.UpdateAvailable {
		info.UpdateCommand = ""
		return info, nil
	}
	stat, err := os.Stat(path)
	if err != nil || !stat.Mode().IsRegular() {
		return info, result.New("precondition", "lbctl 安装路径必须指向普通可执行文件")
	}
	pattern := ".lbctl-update-*"
	if runtime.GOOS == "windows" {
		pattern += ".exe"
	}
	staged, err := os.CreateTemp(filepath.Dir(path), pattern)
	if err != nil {
		return info, result.New("permission", "无法写入 lbctl 安装目录，请检查目录权限后重试")
	}
	defer os.Remove(staged.Name())
	defer staged.Close()
	base := "https://github.com/" + repository + "/releases/download/" + info.LatestVersion + "/"
	expected, err := u.checksum(ctx, base, info.asset)
	if err != nil {
		return info, err
	}
	response, err := u.get(ctx, base+info.asset)
	if err != nil {
		return info, err
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(staged, hash), io.LimitReader(response.Body, (128<<20)+1))
	response.Body.Close()
	if copyErr != nil {
		return info, result.New("network", "下载 lbctl 发行文件失败，请重试")
	}
	if written > 128<<20 || hex.EncodeToString(hash.Sum(nil)) != expected {
		return info, result.New("server", "lbctl 发行文件 SHA-256 校验失败，请稍后重试")
	}
	if staged.Chmod(stat.Mode().Perm()) != nil || staged.Sync() != nil || staged.Close() != nil {
		return info, result.New("permission", "保存 lbctl 新版本失败，请检查磁盘空间和安装目录权限")
	}
	version, err := binaryVersion(ctx, staged.Name())
	if err != nil || version != info.LatestVersion {
		return info, result.New("server", "下载的 lbctl 无法运行或版本与 Release 不一致")
	}
	if ctx.Err() != nil {
		return info, result.New("network", "lbctl 更新已取消或超时")
	}
	if err := replaceExecutable(path, staged.Name()); err != nil {
		if result.AsError(err).Kind == "recovery_required" {
			info.BackupPath = backupPath(path)
		}
		return info, err
	}
	info.Updated, info.UpdateAvailable, info.UpdateCommand = true, false, ""
	return info, nil
}

func (u Updater) checksum(ctx context.Context, base, asset string) (string, error) {
	response, err := u.get(ctx, base+"checksums.txt")
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (128<<10)+1))
	if err != nil || len(data) > 128<<10 {
		return "", result.New("server", "lbctl 校验文件无效")
	}
	checksum := ""
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != asset {
			continue
		}
		decoded, err := hex.DecodeString(fields[0])
		if err != nil || len(decoded) != sha256.Size || checksum != "" {
			return "", result.New("server", "lbctl SHA-256 校验条目无效或重复")
		}
		checksum = strings.ToLower(fields[0])
	}
	if checksum == "" {
		return "", result.New("server", "lbctl 校验文件缺少当前平台的发行文件")
	}
	return checksum, nil
}

func backupPath(path string) string {
	return filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".self-update-old")
}

func replaceExecutable(path, staged string) error {
	if runtime.GOOS != "windows" {
		if os.Rename(staged, path) != nil {
			return result.New("permission", "替换 lbctl 失败，请检查安装目录权限后重试")
		}
		return nil
	}
	backup := backupPath(path)
	if stat, err := os.Lstat(backup); err == nil && !stat.Mode().IsRegular() {
		return result.New("precondition", "lbctl 升级备份路径已被其他文件类型占用")
	}
	if err := os.Remove(backup); err != nil && !errors.Is(err, os.ErrNotExist) {
		return result.New("precondition", "无法清理上次升级备份，请检查运行中的 lbctl 进程及目录权限")
	}
	// Windows permits renaming a running image, but keeps it locked for deletion.
	if os.Rename(path, backup) != nil {
		return result.New("permission", "无法备份运行中的 lbctl，请检查安装目录权限后重试")
	}
	if os.Rename(staged, path) != nil {
		if os.Rename(backup, path) != nil {
			return result.New("recovery_required", "lbctl 替换和恢复均失败，请使用 backup_path 中的原文件恢复安装")
		}
		return result.New("permission", "替换 lbctl 失败，原版本已恢复，请检查目录权限后重试")
	}
	_ = os.Remove(backup)
	return nil
}

// The previous Windows process releases its image after the updating command exits.
func (u Updater) Cleanup() {
	if runtime.GOOS != "windows" {
		return
	}
	path, err := u.executable()
	if err != nil {
		return
	}
	backup := backupPath(path)
	stat, err := os.Lstat(backup)
	if err != nil || !stat.Mode().IsRegular() {
		return
	}
	lock := flock.New(path + ".update.lock")
	locked, err := lock.TryLock()
	if err != nil || !locked {
		return
	}
	defer lock.Unlock()
	_ = os.Remove(backup)
}

type checkCache struct {
	CheckedAt time.Time `json:"checked_at"`
	Latest    string    `json:"latest_version"`
	Failed    bool      `json:"failed,omitempty"`
}

// Reminder is best-effort and bounds the first human-facing check to one second.
func (u Updater) Reminder(ctx context.Context, current string) Info {
	currentCore := strings.SplitN(current, "+", 2)[0]
	currentCore = strings.SplitN(currentCore, "-", 2)[0]
	if _, err := deploy.CompareReleaseVersions(currentCore, currentCore); err != nil {
		return Info{}
	}
	dir := u.CacheDir
	if dir == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			return Info{}
		}
		dir = filepath.Join(base, "lbctl")
	}
	if os.MkdirAll(dir, 0o700) != nil {
		return Info{}
	}
	path := filepath.Join(dir, "update-check.json")
	readCache := func() checkCache {
		var cached checkCache
		file, err := os.Open(path)
		if err == nil {
			defer file.Close()
			_ = json.NewDecoder(io.LimitReader(file, 4096)).Decode(&cached)
		}
		return cached
	}
	infoFromCache := func(cached checkCache) Info {
		available, err := newer(current, cached.Latest)
		if err != nil || !available {
			return Info{}
		}
		return Info{CurrentVersion: current, LatestVersion: cached.Latest, UpdateAvailable: true, UpdateCommand: UpdateCommand}
	}
	fresh := func(cached checkCache) bool {
		age := time.Since(cached.CheckedAt)
		ttl := 24 * time.Hour
		if cached.Failed {
			ttl = 10 * time.Minute
		}
		return age >= 0 && age < ttl
	}
	cached := readCache()
	if fresh(cached) {
		return infoFromCache(cached)
	}
	lock := flock.New(path + ".lock")
	locked, err := lock.TryLock()
	if err != nil || !locked {
		return infoFromCache(cached)
	}
	defer lock.Unlock()
	cached = readCache()
	if fresh(cached) {
		return infoFromCache(cached)
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	info, err := u.Check(ctx, current)
	cached.CheckedAt, cached.Failed = time.Now(), err != nil
	if err == nil {
		cached.Latest = info.LatestVersion
	}
	file, writeErr := os.CreateTemp(dir, ".update-check-*")
	if writeErr == nil {
		defer os.Remove(file.Name())
		encodeErr := json.NewEncoder(file).Encode(cached)
		closeErr := file.Close()
		if encodeErr == nil && closeErr == nil {
			_ = os.Rename(file.Name(), path)
		}
	}
	return infoFromCache(cached)
}

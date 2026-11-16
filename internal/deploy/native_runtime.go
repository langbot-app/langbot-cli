package deploy

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/langbot-app/langbot-cli/internal/telemetry"
)

const UVVersion = "0.9.11"

type UVAsset struct{ Name, SHA256 string }

var UVAssets = map[string]UVAsset{
	"linux/amd64":   {"uv-x86_64-unknown-linux-gnu.tar.gz", "817c0722b437b4b45b9a7e0231616a09db76bab1b8d178ba7a9680c690db19f0"},
	"linux/arm64":   {"uv-aarch64-unknown-linux-gnu.tar.gz", "b695e1796449ea85f967b749f87283678ce284e2c042b4b6fa51fa36ec06f47c"},
	"darwin/amd64":  {"uv-x86_64-apple-darwin.tar.gz", "14236594b4edbd90929d845766a41a1d4e51d530c9ebbedfb3d93688661f142c"},
	"darwin/arm64":  {"uv-aarch64-apple-darwin.tar.gz", "594d9f4cfbd21d5a2f34b0352bf423066a9dab1733c90b5d40e3e227506deb03"},
	"windows/amd64": {"uv-x86_64-pc-windows-msvc.zip", "45a3ff2a68c246ed9fd2d9df032496c1beebe480357f356ac25d2cb144884c30"},
	"windows/arm64": {"uv-aarch64-pc-windows-msvc.zip", "04964328f8e8a4e868153f65a29a927c5d3d8f16c343686bbab5147c72858903"},
}

func EnsureUV(ctx context.Context, tools, region string, out io.Writer) (string, error) {
	asset, ok := UVAssets[CurrentPlatform()]
	if !ok {
		return "", fmt.Errorf("unsupported platform %s", CurrentPlatform())
	}
	dir := filepath.Join(tools, "uv", UVVersion)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	lock := flock.New(filepath.Join(dir, ".lock"))
	locked, err := lock.TryLockContext(ctx, 100*time.Millisecond)
	if err != nil || !locked {
		return "", fmt.Errorf("uv lock: %v", err)
	}
	defer lock.Unlock()
	name := "uv"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	target := filepath.Join(dir, name)
	if regularFile(target) {
		return target, nil
	}
	client := &http.Client{Timeout: 3 * time.Minute}
	var archive []byte
	addresses := []string{
		"https://github.com/astral-sh/uv/releases/download/" + UVVersion + "/" + asset.Name,
		"https://download.langbot.app/tools/uv/" + UVVersion + "/" + asset.Name,
	}
	if region == "cn" {
		addresses[0], addresses[1] = addresses[1], addresses[0]
	}
	for _, address := range addresses {
		fmt.Fprintln(out, "Downloading", address)
		archive, err = Fetch(ctx, client, address, 100<<20)
		if err == nil {
			sum := sha256.Sum256(archive)
			if hex.EncodeToString(sum[:]) != asset.SHA256 {
				err = fmt.Errorf("uv checksum mismatch")
				continue
			}
			break
		}
	}
	if err != nil {
		return "", err
	}
	binary, err := extractUV(archive, asset.Name, name)
	if err != nil {
		return "", err
	}
	return target, writeAtomic(target, binary, 0o700)
}

func extractUV(data []byte, asset, name string) ([]byte, error) {
	if strings.HasSuffix(asset, ".zip") {
		archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return nil, err
		}
		for _, entry := range archive.File {
			if filepath.Base(entry.Name) != name {
				continue
			}
			reader, err := entry.Open()
			if err != nil {
				return nil, err
			}
			defer reader.Close()
			return io.ReadAll(io.LimitReader(reader, 100<<20))
		}
	} else {
		reader, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		defer reader.Close()
		archive := tar.NewReader(reader)
		for {
			entry, err := archive.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, err
			}
			if filepath.Base(entry.Name) == name && entry.Typeflag == tar.TypeReg {
				return io.ReadAll(io.LimitReader(archive, 100<<20))
			}
		}
	}
	return nil, fmt.Errorf("uv executable absent from archive")
}

func NativePython(dir, version string) string {
	venv := filepath.Join(dir, "venvs", version)
	if runtime.GOOS == "windows" {
		return filepath.Join(venv, "Scripts", "python.exe")
	}
	return filepath.Join(venv, "bin", "python")
}

func NativeEnvironment(record Record) []string {
	env := map[string]string{}
	for _, item := range os.Environ() {
		name, value, _ := strings.Cut(item, "=")
		if runtime.GOOS == "windows" {
			name = strings.ToUpper(name)
		}
		env[name] = value
	}
	env["UV_PYTHON_INSTALL_DIR"] = filepath.Join(record.ToolsDir, "python")
	env["UV_PYTHON_PREFERENCE"] = "only-managed"
	env["UV_CACHE_DIR"] = filepath.Join(record.ToolsDir, "cache")
	env["LANGBOT_DATA_ROOT"] = record.DataDir
	env["LANGBOT_PLUGIN_PYPI_INDEX_URL"] = record.PyPIIndex
	env["API__PORT"] = fmt.Sprint(record.Port)
	if _, err := os.Stat(filepath.Join(record.DataDir, "config.yaml")); os.IsNotExist(err) {
		env["API__WEBHOOK_PREFIX"] = record.Endpoint
	}
	venv := filepath.Join(filepath.Dir(record.DataDir), "venvs", record.CoreVersion)
	env["VIRTUAL_ENV"] = venv
	env["PATH"] = filepath.Dir(NativePython(filepath.Dir(record.DataDir), record.CoreVersion)) + string(os.PathListSeparator) + env["PATH"]
	env["PYTHONUNBUFFERED"] = "1"
	env["PYTHONIOENCODING"] = "utf-8"
	env["PYTHONUTF8"] = "1"
	if record.PythonMirror != "" {
		env["UV_PYTHON_INSTALL_MIRROR"] = record.PythonMirror
	}
	result := make([]string, 0, len(env))
	for name, value := range env {
		result = append(result, name+"="+value)
	}
	return result
}

// CheckNativePorts verifies that the HTTP, plugin debug and Box ports are all
// free. Each port is probed on both the loopback address and the wildcard
// address: on BSD-derived systems (macOS) a wildcard bind succeeds even when
// 127.0.0.1:<port> is already taken, so a wildcard-only probe misses loopback
// listeners such as a second native instance's runtime.
func CheckNativePorts(record Record) error {
	ports := []struct {
		name string
		port int
	}{{"HTTP", record.Port}, {"Plugin Runtime 调试", 5401}, {"Box", 5410}}
	seen := map[int]string{}
	for _, item := range ports {
		if item.port != 0 {
			if other, exists := seen[item.port]; exists {
				return fmt.Errorf("%s 端口 %d 与 %s 端口冲突；Core 4.11.x 原生部署需要独占 5401 和 5410，请更换端口，或使用 Docker 部署多实例", item.name, item.port, other)
			}
			seen[item.port] = item.name
		}
		if err := probeNativePort(item.port); err != nil {
			return fmt.Errorf("%s 端口 %d 不可用：%w；Core 4.11.x 原生部署需要独占 5401 和 5410，请停止冲突服务，或使用 Docker 部署多实例", item.name, item.port, err)
		}
	}
	return nil
}

func probeNativePort(port int) error {
	for _, host := range []string{"127.0.0.1", ""} {
		listener, err := net.Listen("tcp", net.JoinHostPort(host, fmt.Sprint(port)))
		if err != nil {
			return err
		}
		listener.Close()
	}
	return nil
}

func PrepareNative(ctx context.Context, dir string, record Record, out io.Writer) error {
	if !releaseVersionPattern.MatchString(record.CoreVersion) {
		return fmt.Errorf("invalid stable version")
	}
	for _, extra := range record.Extras {
		if extra != "seekdb" && extra != "milvus" && extra != "valkey" {
			return fmt.Errorf("unsupported extra %s", extra)
		}
	}
	telemetry.SetStep(ctx, "download_uv")
	uv, err := EnsureUV(ctx, record.ToolsDir, record.Region, out)
	if err != nil {
		return err
	}
	var lastOutput string
	run := func(args ...string) error {
		command := exec.CommandContext(ctx, uv, args...)
		command.Env = NativeEnvironment(record)
		var err error
		lastOutput, err = runNativeCommand(command, out)
		return err
	}
	telemetry.SetStep(ctx, "install_python")
	if err := run("python", "install", "3.12"); err != nil {
		return err
	}
	venv := filepath.Join(dir, "venvs", record.CoreVersion)
	telemetry.SetStep(ctx, "create_venv")
	if err := run("venv", "--allow-existing", "--python", "3.12", venv); err != nil {
		return err
	}
	packageName := "langbot"
	if len(record.Extras) > 0 {
		packageName += "[" + strings.Join(record.Extras, ",") + "]"
	}
	packageName += "==" + strings.TrimPrefix(record.CoreVersion, "v")
	indexes := []string{record.PyPIIndex}
	if record.Region == "cn" && record.PyPIIndex == "https://mirrors.aliyun.com/pypi/simple" {
		indexes = append(indexes, "https://pypi.tuna.tsinghua.edu.cn/simple")
	}
	for _, index := range indexes {
		telemetry.SetStep(ctx, "install_package")
		fmt.Fprintln(out, "Installing", packageName, "from", index)
		err = run("pip", "install", "--python", NativePython(dir, record.CoreVersion), "--index-url", index, packageName)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return err
		}
	}
	if record.Region != "cn" || !missingLangBotVersion(lastOutput) {
		return err
	}
	metadata, fetchErr := Fetch(ctx, &http.Client{Timeout: 15 * time.Second}, "https://pypi.org/pypi/langbot/"+strings.TrimPrefix(record.CoreVersion, "v")+"/json", 2<<20)
	if fetchErr != nil {
		return err
	}
	wheel, wheelErr := langbotWheel(metadata)
	if wheelErr != nil {
		return wheelErr
	}
	if len(record.Extras) > 0 {
		wheel += "[" + strings.Join(record.Extras, ",") + "]"
	}
	fmt.Fprintln(out, "Retrying LangBot wheel from PyPI; dependencies use", record.PyPIIndex)
	return run("pip", "install", "--python", NativePython(dir, record.CoreVersion), "--index-url", record.PyPIIndex, wheel)
}

func missingLangBotVersion(output string) bool {
	return strings.Contains(output, "there is no version of langbot") || strings.Contains(output, "langbot was not found in the package registry")
}

func langbotWheel(data []byte) (string, error) {
	var payload struct {
		URLs []struct {
			URL  string `json:"url"`
			Type string `json:"packagetype"`
		} `json:"urls"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", err
	}
	for _, file := range payload.URLs {
		if file.Type == "bdist_wheel" && strings.HasPrefix(file.URL, "https://files.pythonhosted.org/") {
			return file.URL, nil
		}
	}
	return "", fmt.Errorf("LangBot wheel not found on PyPI")
}

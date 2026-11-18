package command

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/langbot-app/langbot-cli/internal/deploy"
	"github.com/langbot-app/langbot-cli/internal/i18n"
	"golang.org/x/term"
)

func inputTTY(deps Dependencies) bool {
	file, ok := deps.In.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

func installSelection(ctx context.Context, deps Dependencies, driver string, port int, yes bool) (string, int, error) {
	if driver != "" && driver != "native" && driver != "docker" && driver != "docker_compose" {
		return "", 0, errors.New(i18n.T("driver must be native or docker"))
	}
	if port < 1 || port > 65535 {
		return "", 0, errors.New(i18n.T("port must be 1-65535"))
	}
	interactive := inputTTY(deps) && !yes
	reader := bufio.NewReader(deps.In)
	if driver == "" {
		driver = "native"
		if interactive {
			runner := deps.LocalRunner
			if runner == nil {
				runner = deploy.DockerRunner{}
			}
			probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			compose, composeErr := runner.Docker(probeCtx, "compose", "version", "--short")
			_, daemonErr := runner.Docker(probeCtx, "info", "--format", "{{.ServerVersion}}")
			cancel()
			version := strings.TrimPrefix(strings.TrimSpace(compose.Stdout), "v")
			major, _ := strconv.Atoi(strings.Split(version, ".")[0])
			if composeErr == nil && daemonErr == nil && major >= 2 {
				fmt.Fprintln(deps.Err, i18n.Human("1) Native (recommended)\n2) Docker\nSelect [1]:"))
				line, err := reader.ReadString('\n')
				if err != nil {
					return "", 0, err
				}
				switch strings.TrimSpace(line) {
				case "", "1":
				case "2":
					driver = "docker"
				default:
					return "", 0, errors.New(i18n.T("select 1 or 2"))
				}
			} else {
				fmt.Fprintln(deps.Err, i18n.Human("Docker Compose v2/daemon unavailable; using native deployment."))
			}
		}
	}
	if interactive && !installPortFree(port) {
		next := port + 1
		for next <= 65535 && !installPortFree(next) {
			next++
		}
		if next > 65535 {
			return "", 0, errors.New(i18n.T("no free port"))
		}
		fmt.Fprintf(deps.Err, i18n.Human("Port %d is in use; use %d instead? [Y/n]:"), port, next)
		line, err := reader.ReadString('\n')
		if err != nil {
			return "", 0, err
		}
		if answer := strings.ToLower(strings.TrimSpace(line)); answer != "" && answer != "y" && answer != "yes" {
			return "", 0, errors.New(i18n.T("choose another --port"))
		}
		port = next
	}
	if driver == "docker" {
		driver = deploy.DriverDockerCompose
	}
	return driver, port, nil
}

func installPortFree(port int) bool {
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	listener.Close()
	return true
}

// installBoxSelection picks the Box backend for a fresh native installation.
// Interactive installs explain the host risk before first start and preselect
// host; non-interactive installs default to host. Existing installations,
// explicit --box-backend values and Windows Cores without host support keep
// their current configuration.
func installBoxSelection(deps Dependencies, driver, dir, version, backend string, yes, dryRun bool) (string, error) {
	if driver != "native" || backend != "" || !hostBoxDefaultAllowed(runtime.GOOS, version) {
		return backend, nil
	}
	resolved, err := installTargetDir(deps, dir)
	if err != nil {
		return "", nil
	}
	if _, err := (deploy.Store{Dir: resolved}).Read(); err == nil {
		return "", nil
	}
	if !inputTTY(deps) || yes || dryRun {
		return "host", nil
	}
	return promptBoxBackend(deps.In, deps.Err)
}

func promptBoxBackend(in io.Reader, out io.Writer) (string, error) {
	fmt.Fprintln(out, i18n.Human(installBoxPrompt))
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && (err != io.EOF || line == "") {
		return "", err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "", "1", "host":
		return "host", nil
	case "2", "local":
		return "local", nil
	case "3", "docker":
		return "docker", nil
	case "4", "off":
		return "off", nil
	}
	return "", errors.New(i18n.T("select 1, 2, 3 or 4"))
}

const installBoxPrompt = "Box runs Agent commands, skills and stdio MCP.\n" +
	"host runs them directly as your system user, with no file, network or process isolation. If the bot joins a group chat, untrusted users can use prompt injection to run commands on this computer.\n" +
	"1) host (no isolation; for personal use on this computer)\n" +
	"2) local (Docker or nsjail sandbox; must already be installed)\n" +
	"3) docker (Docker sandbox; Docker must already be installed)\n" +
	"4) off (disable Box; command tools are unavailable)\n" +
	"Select Box backend [1]:"

func hostBoxDefaultAllowed(platform, version string) bool {
	if platform != "windows" {
		return true
	}
	if version == "" {
		return deploy.WindowsHostBoxMinVersion != ""
	}
	return deploy.WindowsHostBoxSupported(platform, version)
}

func installTargetDir(deps Dependencies, dir string) (string, error) {
	if strings.TrimSpace(dir) == "" && strings.TrimSpace(deps.LocalDir) != "" {
		return deploy.ResolveDir(deps.LocalDir)
	}
	return (deploy.Locator{Path: deps.LocalLocatorPath}).Resolve(dir)
}

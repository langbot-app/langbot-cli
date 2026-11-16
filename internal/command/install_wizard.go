package command

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
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

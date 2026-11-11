package app

import (
	"context"
	"encoding/json"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/langbot-app/langbot-cli/internal/deploy"
	"github.com/langbot-app/langbot-cli/internal/result"
)

type discoveredDeployment struct {
	Project     string `json:"project" yaml:"project"`
	ComposeFile string `json:"compose_file" yaml:"compose_file"`
	Endpoint    string `json:"endpoint,omitempty" yaml:"endpoint,omitempty"`
	Binding     string `json:"binding" yaml:"binding"`
}

// DeploymentDiscover only inspects local Compose projects. Discovery never
// registers a project or grants it lifecycle access; adopt verifies that binding.
func (s *Service) DeploymentDiscover(ctx context.Context) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	runner := s.localRunner()
	command, err := runner.Docker(ctx, "compose", "ls", "--all", "--format", "json")
	if err != nil {
		return Result{}, mapDockerCommandError(command, err, "无法发现本机 Compose 项目")
	}
	var projects []struct {
		Name        string
		ConfigFiles string
	}
	if err := json.Unmarshal([]byte(command.Stdout), &projects); err != nil {
		return Result{}, result.New("server", "Compose 项目列表格式无效")
	}
	dirs, err := s.deploymentDirs()
	if err != nil {
		return Result{}, result.New("precondition", "本机部署定位记录不可用")
	}
	registered := map[string]string{}
	for _, dir := range dirs {
		if record, err := (deploy.Store{Dir: dir}).Read(); err == nil {
			registered[record.ComposeProject] = record.ComposeFile
		}
	}
	entries := []discoveredDeployment{}
	skipped := []string{}
	for _, project := range projects {
		if ctx.Err() != nil {
			return Result{}, result.New("network", "发现本机部署超时")
		}
		if !validComposeProject(project.Name) {
			continue
		}
		// adopt currently accepts one Compose file. Don't silently drop override
		// files and inspect a different effective configuration.
		if strings.Contains(project.ConfigFiles, ",") {
			skipped = append(skipped, project.Name)
			continue
		}
		file, err := resolveComposeFile(project.ConfigFiles)
		if err != nil {
			skipped = append(skipped, project.Name)
			continue
		}
		record := deploy.Record{ComposeProject: project.Name, ComposeFile: file, Profile: "basic"}
		services, err := runner.Compose(ctx, record, "config", "--services")
		if err != nil {
			skipped = append(skipped, project.Name)
			continue
		}
		if len(missingRequiredServices(services.Stdout, "basic")) != 0 {
			continue
		}
		entry := discoveredDeployment{Project: project.Name, ComposeFile: file, Binding: "unmanaged"}
		if registered[project.Name] == file {
			entry.Binding = "registered"
		}
		published, err := runner.Compose(ctx, record, "port", "langbot", "5300")
		if err == nil {
			for _, line := range strings.Split(strings.TrimSpace(published.Stdout), "\n") {
				host, port, err := net.SplitHostPort(strings.TrimSpace(line))
				number, numberErr := strconv.Atoi(port)
				if err == nil && numberErr == nil && number > 0 && number <= 65535 {
					switch host {
					case "", "0.0.0.0":
						host = "127.0.0.1"
					case "::":
						host = "::1"
					}
					entry.Endpoint = "http://" + net.JoinHostPort(host, port)
					break
				}
			}
		}
		entries = append(entries, entry)
	}
	if ctx.Err() != nil {
		return Result{}, result.New("network", "发现本机部署超时")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Project < entries[j].Project })
	sort.Strings(skipped)
	return Result{Data: map[string]any{"deployments": entries, "skipped_projects": skipped}}, nil
}

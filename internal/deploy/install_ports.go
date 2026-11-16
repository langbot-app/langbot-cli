package deploy

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/langbot-app/langbot-cli/internal/i18n"
)

type InstallNetworkOptions struct {
	Project         string
	PluginDebugPort *int
	AdapterPorts    []string
}

// InstallPorts records host publications; container-internal ports stay fixed.
// A zero debug port and an empty adapter list disable those publications.
type InstallPorts struct {
	PluginDebugPort int      `json:"plugin_debug_port" yaml:"plugin_debug_port"`
	AdapterPorts    []string `json:"adapter_ports" yaml:"adapter_ports"`
}

func defaultInstallPorts() InstallPorts {
	return InstallPorts{PluginDebugPort: 5401, AdapterPorts: []string{"2280-2285:2280-2285"}}
}

func resolveInstallNetwork(httpPort int, options InstallNetworkOptions) (string, InstallPorts, error) {
	project := options.Project
	if project == "" {
		project = DefaultComposeProject
	}
	if !ValidComposeProject(project) {
		return "", InstallPorts{}, errors.New(i18n.T("Invalid Compose project name"))
	}
	ports := defaultInstallPorts()
	if options.PluginDebugPort != nil {
		ports.PluginDebugPort = *options.PluginDebugPort
	}
	if options.AdapterPorts != nil {
		ports.AdapterPorts = append([]string{}, options.AdapterPorts...)
		if len(ports.AdapterPorts) == 1 && strings.TrimSpace(ports.AdapterPorts[0]) == "none" {
			ports.AdapterPorts = []string{}
		}
	}
	for i, mapping := range ports.AdapterPorts {
		host, target, err := parseAdapterMapping(mapping)
		if err != nil {
			return "", InstallPorts{}, err
		}
		ports.AdapterPorts[i] = host.String() + ":" + target.String()
	}
	if _, err := ports.hostPorts(httpPort); err != nil {
		return "", InstallPorts{}, err
	}
	return project, ports, nil
}

func ValidComposeProject(value string) bool {
	if value == "" || len(value) > 63 {
		return false
	}
	for i, char := range value {
		if i == 0 && !((char >= 'a' && char <= 'z') || (char >= '0' && char <= '9')) {
			return false
		}
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '-' || char == '_' {
			continue
		}
		return false
	}
	return true
}

func (p InstallPorts) hostPorts(httpPort int) ([]int, error) {
	if httpPort < 1 || httpPort > 65535 || p.PluginDebugPort < 0 || p.PluginDebugPort > 65535 {
		return nil, errors.New(i18n.T("Host ports must be 1-65535; plugin-debug-port accepts 0 to disable publishing"))
	}
	ports := []int{httpPort}
	seen := map[int]bool{httpPort: true}
	add := func(port int) error {
		if seen[port] {
			return fmt.Errorf(i18n.T("Host port %d is mapped more than once"), port)
		}
		seen[port] = true
		ports = append(ports, port)
		return nil
	}
	if p.PluginDebugPort != 0 {
		if err := add(p.PluginDebugPort); err != nil {
			return nil, err
		}
	}
	targets := map[int]bool{}
	for _, mapping := range p.AdapterPorts {
		host, target, err := parseAdapterMapping(mapping)
		if err != nil {
			return nil, err
		}
		for offset := 0; offset <= host.end-host.start; offset++ {
			if targets[target.start+offset] {
				return nil, errors.New(i18n.T("Adapter container port mapped more than once"))
			}
			targets[target.start+offset] = true
			if err := add(host.start + offset); err != nil {
				return nil, err
			}
		}
	}
	return ports, nil
}

type portRange struct{ start, end int }

func (p portRange) String() string {
	if p.start == p.end {
		return strconv.Itoa(p.start)
	}
	return fmt.Sprintf("%d-%d", p.start, p.end)
}

func parseAdapterMapping(value string) (portRange, portRange, error) {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) == 2 {
		host, hostErr := parsePortRange(parts[0])
		target, targetErr := parsePortRange(parts[1])
		if hostErr == nil && targetErr == nil && target.start >= 2280 && target.end <= 2285 && host.end-host.start == target.end-target.start {
			return host, target, nil
		}
	}
	return portRange{}, portRange{}, errors.New(i18n.T("adapter-ports requires HOST:CONTAINER or equal-length port ranges; container ports are limited to 2280-2285; use none to disable publishing"))
}

func parsePortRange(value string) (portRange, error) {
	parts := strings.Split(value, "-")
	if len(parts) < 1 || len(parts) > 2 {
		return portRange{}, errors.New(i18n.T("Invalid port range"))
	}
	values := make([]int, len(parts))
	for i, part := range parts {
		if part == "" || strings.IndexFunc(part, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
			return portRange{}, errors.New(i18n.T("Invalid port"))
		}
		value, err := strconv.Atoi(part)
		if err != nil || value < 1 || value > 65535 {
			return portRange{}, errors.New(i18n.T("Invalid port"))
		}
		values[i] = value
	}
	result := portRange{start: values[0], end: values[len(values)-1]}
	if result.end < result.start {
		return portRange{}, errors.New(i18n.T("Invalid port range"))
	}
	return result, nil
}

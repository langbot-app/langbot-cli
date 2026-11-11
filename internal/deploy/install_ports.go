package deploy

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
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
		return "", InstallPorts{}, errors.New("Compose project 名称无效")
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
		return nil, errors.New("宿主机端口必须在 1-65535 之间；plugin-debug-port 可用 0 关闭发布")
	}
	ports := []int{httpPort}
	seen := map[int]bool{httpPort: true}
	add := func(port int) error {
		if seen[port] {
			return fmt.Errorf("宿主机端口 %d 被重复映射", port)
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
				return nil, errors.New("适配器容器端口被重复映射")
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
	return portRange{}, portRange{}, errors.New("adapter-ports 需要 HOST:CONTAINER 或等长端口范围，容器端口仅限 2280-2285；使用 none 关闭发布")
}

func parsePortRange(value string) (portRange, error) {
	parts := strings.Split(value, "-")
	if len(parts) < 1 || len(parts) > 2 {
		return portRange{}, errors.New("端口范围无效")
	}
	values := make([]int, len(parts))
	for i, part := range parts {
		if part == "" || strings.IndexFunc(part, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
			return portRange{}, errors.New("端口无效")
		}
		value, err := strconv.Atoi(part)
		if err != nil || value < 1 || value > 65535 {
			return portRange{}, errors.New("端口无效")
		}
		values[i] = value
	}
	result := portRange{start: values[0], end: values[len(values)-1]}
	if result.end < result.start {
		return portRange{}, errors.New("端口范围无效")
	}
	return result, nil
}

package command

import "github.com/spf13/cobra"

func lifecycleTelemetryCommand(root, selected *cobra.Command) string {
	if selected.Parent() == root {
		switch selected.Name() {
		case "install", "upgrade", "uninstall", "start", "stop", "restart", "update":
			return selected.Name()
		}
	}
	parent := selected.Parent()
	if parent != nil && parent.Parent() == root && parent.Name() == "autostart" {
		switch selected.Name() {
		case "status", "enable", "disable":
			return "autostart." + selected.Name()
		}
	}
	return ""
}

func hasHelpFlag(args []string) bool {
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			return true
		}
		if arg == "--" {
			break
		}
	}
	return false
}

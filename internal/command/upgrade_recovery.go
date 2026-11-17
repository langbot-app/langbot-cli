package command

func upgradeRecovery(step string) (string, string) {
	switch step {
	case "", "resolve_version", "validate_target":
		return "Check the upgrade version and local instance arguments", "upgrade"
	case "image_pull":
		return "Check Docker connectivity and image repositories before retrying", "doctor"
	case "download_uv", "install_python", "create_venv", "install_package":
		return "Check package sources, network access and available disk space before retrying", "doctor"
	case "switching", "compose_changed", "compose_switch", "environment_switch", "compose_switched":
		return "Compare Compose and environment files with the saved backup before recovery", "status"
	case "record_write", "write_record", "image_verified":
		return "Check deployment record permissions and confirm the running version", "status"
	case "backup_preflight", "backup", "backup_data", "backup_verified":
		return "Check backup space and Docker access; preserve the backup and inspect upgrade state before restarting", "status"
	case "stopping", "stopped", "stop", "stop_readback", "stop_service":
		return "Confirm all services stopped before continuing the upgrade", "status"
	case "starting", "verifying", "compose_up", "http_version", "service_health", "start_service", "health_check":
		return "Inspect service logs; preserve the backup before recovering the previous version", "logs"
	default:
		return "Inspect upgrade state and the saved backup before changing deployment files", "status"
	}
}

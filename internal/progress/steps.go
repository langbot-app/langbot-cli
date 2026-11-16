package progress

import "context"

func StepID(ctx context.Context, step string) {
	title, ok := stepTitles[step]
	if !ok {
		title = step
	}
	Step(ctx, title)
}

var stepTitles = map[string]string{
	"stopping": "Stop service", "stopped": "Verify stopped services", "backup_verified": "Verify backup",
	"switching": "Write deployment assets", "compose_switched": "Write deployment assets", "starting": "Start service",
	"verifying": "Check service health", "completed": "Completed", "stop_readback": "Verify stopped services",
	"backup_preflight": "Check backup prerequisites", "compose_changed": "Verify deployment assets",
	"compose_switch": "Write deployment assets", "environment_switch": "Write deployment assets",
	"http_version": "Check service health", "service_health": "Check service health", "record_write": "Save deployment record",
	"resolve_version": "Resolve version", "validate_target": "Validate target",
	"check_ports": "Check ports", "write_record": "Save deployment record",
	"download_uv": "Download uv", "install_python": "Install Python 3.12",
	"create_venv": "Create virtual environment", "install_package": "Install LangBot packages",
	"install_service": "Register startup", "start_service": "Start service",
	"stop_service": "Stop service", "health_check": "Check service health",
	"remove_service": "Remove startup", "purge_data": "Remove data",
	"remove_venvs": "Remove virtual environments", "compose_down": "Remove containers",
	"preflight": "Check prerequisites", "pull_image": "Pull images", "compose_up": "Start containers",
	"compose_start": "Start containers", "compose_stop": "Stop containers", "compose_restart": "Restart containers",
	"resolve_target": "Resolve deployment", "autostart_enable": "Enable startup", "autostart_disable": "Disable startup",
	"autostart_status": "Inspect startup", "backup_data": "Back up data", "download_update": "Download update",
	"replace_binary": "Replace executable", "pull": "Pull images", "stop": "Stop service", "backup": "Back up data",
	"start": "Start service", "health": "Check service health", "write_assets": "Write deployment assets",
}

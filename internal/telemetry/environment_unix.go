//go:build linux || darwin

package telemetry

import "os/exec"

func configureDiagnosticCommand(command *exec.Cmd) {}

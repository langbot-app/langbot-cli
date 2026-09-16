package api

import (
	"net/http"
)

func resolveSandboxOperation(path string) (Operation, bool) {
	id, permission := "", "resource.view"
	switch path {
	case "/api/v1/box/status":
		id = "sandbox.status"
	case "/api/v1/box/sessions":
		id, permission = "sandbox.sessions", "audit.view"
	case "/api/v1/box/errors":
		id, permission = "sandbox.errors", "audit.view"
	}
	return Operation{ID: id, Method: http.MethodGet, Path: path, ReadOnly: true, Permission: permission}, id != ""
}

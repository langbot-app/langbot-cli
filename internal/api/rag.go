package api

import (
	"net/http"
)

func resolveRAGOperation(path string) (Operation, bool) {
	id := ""
	switch path {
	case "/api/v1/knowledge/engines":
		id = "knowledge_engine.list"
	case "/api/v1/knowledge/parsers":
		id = "knowledge_parser.list"
	default:
		for _, kind := range []string{"creation", "retrieval"} {
			if nestedPath(path, "/api/v1/knowledge/engines", 2, kind+"-schema") {
				id = "knowledge_engine." + kind + "_schema"
			}
		}
	}
	return Operation{ID: id, Method: http.MethodGet, Path: path, ReadOnly: true, Permission: "resource.view"}, id != ""
}

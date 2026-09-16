package api

import (
	"net/http"
	"testing"
)

func TestRAGPathsRejectMalformedPluginIDs(t *testing.T) {
	for _, path := range []string{"%zz", "/api/v1/knowledge/engines/author/../creation-schema", "/api/v1/knowledge/engines/author%2Fname/creation-schema", "/api/v1/knowledge/parsers?unsupported=true"} {
		if _, ok := ResolveOperation(http.MethodGet, path); ok {
			t.Errorf("accepted %s", path)
		}
	}
}

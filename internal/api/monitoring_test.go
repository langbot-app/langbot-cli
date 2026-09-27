package api

import (
	"net/http"
	"testing"
)

func TestMonitoringFiltersAreBoundedAndAllowlisted(t *testing.T) {
	for _, path := range []string{"/api/v1/monitoring/messages?limit=0", "/api/v1/monitoring/messages?offset=-1", "/api/v1/monitoring/messages?limit=1&limit=2", "/api/v1/monitoring/embedding-calls?pipelineId=p", "/api/v1/monitoring/sessions?isActive=yes", "/api/v1/monitoring/messages/m/details?limit=1"} {
		if _, ok := ResolveOperation(http.MethodGet, path); ok {
			t.Errorf("accepted %s", path)
		}
	}
	if _, ok := ResolveOperation(http.MethodGet, "/api/v1/monitoring/messages?botId=a&botId=b&limit=500"); !ok {
		t.Fatal("repeated filters rejected")
	}
}

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/langbot-app/langbot-cli/internal/result"
)

func TestLoginTOTPChallenge(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls++
		var body map[string]string
		json.NewDecoder(request.Body).Decode(&body)
		if calls == 1 {
			if request.URL.Path != "/api/v1/user/auth" || body["user"] != "local@example.test" || body["password"] != "password" {
				t.Error("invalid password request")
			}
			writer.WriteHeader(401)
			fmt.Fprint(writer, `{"code":"totp_required","data":{"challenge_token":"challenge-secret"}}`)
		} else {
			if request.URL.Path != "/api/v1/user/totp/verify" || body["challenge_token"] != "challenge-secret" || body["code"] != "123456" || body["password"] != "" {
				t.Error("invalid challenge request")
			}
			fmt.Fprint(writer, `{"code":0,"data":{"token":"session-secret"}}`)
		}
	}))
	defer server.Close()
	token, err := (Client{}).Login(context.Background(), Target{Endpoint: server.URL}, "local@example.test", "password", func() (string, error) { return "123456", nil })
	if err != nil || token != "session-secret" || calls != 2 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestLoginCloudPasswordDisabled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(403)
		fmt.Fprint(writer, `{"code":"password_login_disabled"}`)
	}))
	defer server.Close()
	_, err := (Client{}).Login(context.Background(), Target{Endpoint: server.URL}, "local@example.test", "password", nil)
	if err == nil || result.AsError(err).Type != "password_login_disabled" {
		t.Fatalf("%v", err)
	}
}

func TestMemberContextPreservesOptionalInstanceIdentity(t *testing.T) {
	for _, test := range []struct {
		name  string
		value any
		want  string
	}{
		{"current response", "instance-a", "instance-a"},
		{"legacy response", nil, ""},
		{"blank", " ", ""},
		{"wrong type", 12, ""},
		{"invalid segment", "instance/other", ""},
		{"redacted identity", "instance-member-token", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				calls++
				if request.URL.Path != "/api/v1/workspaces/current" || request.Header.Get("Authorization") != "Bearer member-token" || request.Header.Get("X-API-Key") != "" {
					t.Errorf("member identity request path=%s headers=%v", request.URL.Path, request.Header)
				}
				workspace := map[string]any{"uuid": "workspace-a"}
				if test.value != nil {
					workspace["instance_uuid"] = test.value
				}
				_ = json.NewEncoder(writer).Encode(map[string]any{"code": 0, "data": map[string]any{"workspace": workspace, "permissions": []string{"resource.view"}}})
			}))
			defer server.Close()
			identity, err := (Client{}).MemberContext(context.Background(), Target{Endpoint: server.URL, UserToken: "member-token"})
			if err != nil || identity.InstanceUUID != test.want || identity.WorkspaceUUID != "workspace-a" || len(identity.Permissions) != 1 || calls != 1 {
				t.Fatalf("identity=%+v err=%v calls=%d", identity, err, calls)
			}
		})
	}
}

func TestMemberCapabilitiesRecognizesKnowledgeCatalogNames(t *testing.T) {
	for _, canonical := range []bool{true, false} {
		t.Run(fmt.Sprintf("canonical_%t", canonical), func(t *testing.T) {
			identifiers := []string{"knowledge_base.get", "knowledge_base.retrieve", "knowledge_base.file.list", "knowledge_base.file.store", "file.document.upload"}
			if canonical {
				identifiers = []string{"knowledge.base.get", "knowledge.base.retrieve", "knowledge.file.list", "knowledge.file.store", "file.document.upload"}
			}
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				calls++
				if request.URL.Path != "/api/v1/operations" || request.Header.Get("Authorization") != "Bearer member-token" || request.Header.Get("X-API-Key") != "" {
					t.Error("invalid member catalog request")
				}
				operations := make([]map[string]string, len(identifiers))
				for index, identifier := range identifiers {
					operations[index] = map[string]string{"id": identifier}
				}
				_ = json.NewEncoder(writer).Encode(map[string]any{"operations": operations})
			}))
			defer server.Close()
			capabilities, err := (Client{}).MemberCapabilities(context.Background(), Target{Endpoint: server.URL, UserToken: "member-token"})
			if err != nil || calls != 1 {
				t.Fatalf("catalog error=%v calls=%d", err, calls)
			}
			for _, identifier := range append(identifiers, "knowledge_base.get", "knowledge_base.retrieve", "knowledge_base.file.list", "knowledge_base.file.store") {
				if !capabilities.Operations[identifier] {
					t.Errorf("catalog operation missing %s", identifier)
				}
			}
			if capabilities.Operations["knowledge_base.create"] || capabilities.Operations["knowledge_base.file.delete"] {
				t.Fatal("catalog inferred unadvertised knowledge operations")
			}
		})
	}
}

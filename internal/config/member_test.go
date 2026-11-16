package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMemberTokenScopeAndSerialization(t *testing.T) {
	file := File{Version: 1, CurrentContext: "test", Contexts: map[string]Context{"test": {Endpoint: "https://example.test", UserToken: "saved-secret"}}}
	lookup := func(string) (string, bool) { return "", false }
	conn, err := Resolve(file, Options{}, lookup)
	if err != nil {
		t.Fatal(err)
	}
	conn, err = conn.WithMember(file, lookup)
	if err != nil || conn.UserToken != "saved-secret" || conn.APIKey != "" {
		t.Fatal("saved token unavailable")
	}
	encoded, err := json.Marshal(conn)
	if err != nil || strings.Contains(string(encoded), "saved-secret") {
		t.Fatal("serialized secret")
	}
	conn, err = Resolve(file, Options{Endpoint: "https://other.test", EndpointSet: true}, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = conn.WithMember(file, lookup); err == nil {
		t.Fatal("saved member token reused on another target")
	}
	conn, err = conn.WithMember(file, func(name string) (string, bool) { return "explicit-secret", name == "LANGBOT_USER_TOKEN" })
	if err != nil || conn.UserToken != "explicit-secret" {
		t.Fatal("explicit token unavailable")
	}
}

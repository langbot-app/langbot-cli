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

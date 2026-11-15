package clientidentity

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"runtime"
)

type contextKey struct{}

type identity struct {
	version string
	command string
}

var commandPattern = regexp.MustCompile(`^[a-z0-9-]+(\.[a-z0-9-]+){0,5}$`)

func Version(version string) string {
	if version == "" {
		return "dev"
	}
	return version
}

func WithContext(ctx context.Context, version, command string) context.Context {
	if len(command) > 64 || !commandPattern.MatchString(command) {
		command = ""
	}
	return context.WithValue(ctx, contextKey{}, identity{Version(version), command})
}

func UserAgent(ctx context.Context) string {
	value, _ := ctx.Value(contextKey{}).(identity)
	return fmt.Sprintf("lbctl/%s (%s/%s)", Version(value.version), runtime.GOOS, runtime.GOARCH)
}

func Apply(request *http.Request) {
	value, _ := request.Context().Value(contextKey{}).(identity)
	request.Header.Set("User-Agent", UserAgent(request.Context()))
	if value.command != "" {
		request.Header.Set("X-LangBot-Client-Command", value.command)
	}
}

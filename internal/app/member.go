package app

import (
	"context"
	"io"

	"github.com/langbot-app/langbot-cli/internal/api"
	"github.com/langbot-app/langbot-cli/internal/config"
	"github.com/langbot-app/langbot-cli/internal/endpoint"
	"github.com/langbot-app/langbot-cli/internal/i18n"
	"github.com/langbot-app/langbot-cli/internal/result"
)

func (s *Service) AuthConnection(options CheckOptions) (config.Connection, error) {
	file, err := s.load()
	if err != nil {
		return config.Connection{}, err
	}
	conn, err := config.Resolve(file, config.Options{Context: options.Context, ContextSet: options.ContextSet, Endpoint: options.Endpoint, EndpointSet: options.EndpointSet, Timeout: options.Timeout, TimeoutSet: options.TimeoutSet}, s.deps.LookupEnv)
	if err != nil {
		return conn, err
	}
	if conn.Temporary || conn.Context == "" {
		return conn, result.New("precondition", i18n.T("Login requires a saved context; run lbctl context add first"))
	}
	return conn, nil
}

func (s *Service) SaveMemberToken(ctx context.Context, conn config.Connection, token string) error {
	return s.WithContext(ctx).update(func(file *config.File) error {
		selected, ok := file.Contexts[conn.Context]
		normalized, err := endpoint.Normalize(selected.Endpoint)
		if !ok || err != nil || normalized != conn.Endpoint {
			return result.New("precondition", i18n.T("context changed; log in again"))
		}
		selected.UserToken = token
		file.Contexts[conn.Context] = selected
		return nil
	})
}

func (s *Service) ImageDownload(ctx context.Context, options CheckOptions, key string, destination io.Writer) error {
	conn, err := s.connection(ctx, config.Options{Context: options.Context, ContextSet: options.ContextSet, Endpoint: options.Endpoint, EndpointSet: options.EndpointSet, Timeout: options.Timeout, TimeoutSet: options.TimeoutSet, APIKeyStdin: options.APIKeyStdin})
	if err != nil {
		return err
	}
	if conn.APIKey == "" && conn.UserToken == "" {
		return result.AuthSetupError("missing_api_key", "LANGBOT_API_KEY")
	}
	return (api.Client{Transport: s.deps.Transport}).ImageDownload(ctx, api.Target{Endpoint: conn.Endpoint, APIKey: conn.APIKey, UserToken: conn.UserToken, Timeout: conn.Timeout}, key, destination)
}

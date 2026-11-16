package config

import (
	"context"
	"strings"

	"github.com/langbot-app/langbot-cli/internal/result"
)

type memberKey struct{}

func AsMember(ctx context.Context) context.Context {
	return context.WithValue(ctx, memberKey{}, true)
}

func IsMember(ctx context.Context) bool {
	value, _ := ctx.Value(memberKey{}).(bool)
	return value
}

func (c Connection) WithMember(file File, lookup func(string) (string, bool)) (Connection, error) {
	value, present := envLookup(lookup)("LANGBOT_USER_TOKEN")
	if !present && !c.Temporary {
		value = file.Contexts[c.Context].UserToken
	}
	if strings.TrimSpace(value) == "" {
		return c, result.RequiresMemberSession()
	}
	if len(value) > maxSecretBytes || validateSecret(value) != nil {
		return c, result.New("input", "成员 token 无效")
	}
	c.APIKey, c.UserToken, c.CredentialSource = "", value, "member_session"
	return c, nil
}

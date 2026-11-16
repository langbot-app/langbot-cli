package command

import "context"

func executeChinese(ctx context.Context, args []string, deps Dependencies) int {
	return Execute(ctx, append([]string{"--lang", "zh-Hans"}, args...), deps)
}

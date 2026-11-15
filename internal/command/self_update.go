package command

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/langbot-app/langbot-cli/internal/app"
	"github.com/langbot-app/langbot-cli/internal/result"
	"github.com/langbot-app/langbot-cli/internal/selfupdate"
	"github.com/spf13/cobra"
)

func newSelfCommand(deps Dependencies, flags *globalFlags) *cobra.Command {
	self := &cobra.Command{Use: "self", Short: "管理 lbctl 工具自身", Args: cobra.NoArgs}
	var check bool
	update := &cobra.Command{
		Use: "update", Short: "将 lbctl 升级到最新稳定版本", Args: cobra.NoArgs,
		Long:    "从官方 Release 下载当前平台的最新稳定版本，校验 SHA-256 和版本后完成替换。\n使用 --check 查看可用更新，--timeout 调整等待上限（检查默认 15 秒，升级默认 10 分钟）。",
		Example: "  lbctl self update --check\n  lbctl self update",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if flags.contextSet || flags.endpointSet || flags.apiKeyStdin {
				return emitCommand(deps, flags, app.Result{}, result.New("input", "self update 不接受实例连接参数"))
			}
			timeout := 10 * time.Minute
			if check {
				timeout = 15 * time.Second
			}
			if flags.timeoutSet {
				var err error
				timeout, err = time.ParseDuration(flags.timeout)
				if err != nil || timeout <= 0 {
					return emitCommand(deps, flags, app.Result{}, result.New("input", "timeout 必须是正的时间长度"))
				}
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
			defer cancel()
			return emitCall(deps, flags, func() (app.Result, error) {
				var info selfupdate.Info
				var err error
				if check {
					info, err = deps.SelfUpdate.Check(ctx, deps.Version)
				} else {
					info, err = deps.SelfUpdate.Update(ctx, deps.Version)
				}
				return app.Result{Data: info}, err
			})
		},
	}
	update.Flags().BoolVar(&check, "check", false, "只检查最新版本")
	self.AddCommand(update)
	return self
}

func terminalWriter(writer io.Writer) bool {
	file, ok := writer.(*os.File)
	if !ok {
		return false
	}
	stat, err := file.Stat()
	return err == nil && stat.Mode()&os.ModeCharDevice != 0
}

func startUpdateNotice(ctx context.Context, cmd *cobra.Command, deps Dependencies, flags *globalFlags) func() {
	path := commandPath(cmd)
	if flags.outputSet || !terminalWriter(deps.Out) || !terminalWriter(deps.Err) {
		return func() {}
	}
	for _, name := range []string{"CI", "GITHUB_ACTIONS", "LBCTL_NO_UPDATE_CHECK"} {
		value, _ := deps.LookupEnv(name)
		if value != "" && value != "0" && !strings.EqualFold(value, "false") {
			return func() {}
		}
	}
	family := strings.Split(path, ".")[0]
	switch family {
	case "lbctl", "version", "schema", "completion", "help", "context", "self":
		return func() {}
	}
	done := make(chan selfupdate.Info, 1)
	go func() { done <- deps.SelfUpdate.Reminder(ctx, deps.Version) }()
	return func() {
		info := <-done
		if flags.exitCode == 0 && info.UpdateAvailable {
			fmt.Fprintf(deps.Err, "发现 lbctl 新版本 %s → %s，运行 %s 升级\n", info.CurrentVersion, info.LatestVersion, selfupdate.UpdateCommand)
		}
	}
}

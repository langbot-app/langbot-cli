package command

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

	"github.com/langbot-app/langbot-cli/internal/api"
	"github.com/langbot-app/langbot-cli/internal/app"
	"github.com/langbot-app/langbot-cli/internal/result"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func memberOperation(identifier string) bool {
	for _, prefix := range []string{"workspace.member.", "workspace.invitation.", "provider.codex.", "assistant.", "pipeline.migration.", "knowledge.migration.", "knowledge-base.migration."} {
		if strings.HasPrefix(identifier, prefix) {
			return true
		}
	}
	return false
}

func promptSecret(deps Dependencies, label string) (string, error) {
	input, ok := deps.In.(*os.File)
	if !ok {
		return "", result.New("input", "需要终端输入"+label)
	}
	if !term.IsTerminal(int(input.Fd())) {
		path := "/dev/tty"
		if runtime.GOOS == "windows" {
			path = "CONIN$"
		}
		terminal, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			return "", result.New("input", "需要终端输入"+label)
		}
		defer terminal.Close()
		input = terminal
	}
	fmt.Fprint(deps.Err, label+": ")
	value, err := term.ReadPassword(int(input.Fd()))
	fmt.Fprintln(deps.Err)
	if err != nil {
		return "", result.New("input", "读取"+label+"失败")
	}
	return string(value), nil
}

func stdinSecret(reader io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(reader, (64<<10)+1))
	if err != nil || len(data) > 64<<10 || strings.TrimSpace(string(data)) == "" {
		return "", result.New("input", "stdin 凭据为空、过大或读取失败")
	}
	return strings.TrimRight(string(data), "\r\n"), nil
}

func newAuthCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	root := &cobra.Command{Use: "auth", Short: "管理成员会话"}
	var email string
	var passwordStdin, totp, tokenStdin bool
	login := &cobra.Command{Use: "login", Short: "登录本地账号并保存成员 token", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return emitCall(deps, flags, func() (app.Result, error) {
			if flags.apiKeyStdin {
				return app.Result{}, result.New("input", "auth login 不使用 --api-key-stdin")
			}
			conn, err := service.AuthConnection(connectionOptions(flags))
			if err != nil {
				return app.Result{}, err
			}
			var password string
			if passwordStdin {
				password, err = stdinSecret(deps.In)
			} else {
				password, err = promptSecret(deps, "Password")
			}
			if err != nil {
				return app.Result{}, err
			}
			prompt := func() (string, error) {
				if !totp {
					return "", result.New("auth", "需要 TOTP；请在终端重新运行 auth login --totp")
				}
				return promptSecret(deps, "TOTP / recovery code")
			}
			token, err := (api.Client{Transport: deps.Transport}).Login(cmd.Context(), api.Target{Endpoint: conn.Endpoint, Timeout: conn.Timeout}, email, password, prompt)
			if err == nil {
				err = service.SaveMemberToken(cmd.Context(), conn, token)
			}
			return app.Result{Data: map[string]any{"context": conn.Context, "authenticated": err == nil}}, err
		})
	}}
	login.Flags().StringVar(&email, "email", "", "账号邮箱")
	login.Flags().BoolVar(&passwordStdin, "password-stdin", false, "从 stdin 读取密码")
	login.Flags().BoolVar(&totp, "totp", false, "遇到二次验证时提示输入 TOTP / 恢复码")
	_ = login.MarkFlagRequired("email")
	root.AddCommand(login)
	for _, action := range []string{"set-token", "logout"} {
		cmd := &cobra.Command{Use: action, Short: "保存或清除当前 context 的成员 token", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			return emitCall(deps, flags, func() (app.Result, error) {
				conn, err := service.AuthConnection(connectionOptions(flags))
				if err != nil {
					return app.Result{}, err
				}
				token := ""
				if action == "set-token" {
					if !tokenStdin || flags.apiKeyStdin {
						return app.Result{}, result.New("input", "请使用 auth set-token --stdin，不可同时使用 --api-key-stdin")
					}
					token, err = stdinSecret(deps.In)
					if err != nil {
						return app.Result{}, err
					}
				}
				err = service.SaveMemberToken(cmd.Context(), conn, token)
				return app.Result{Data: map[string]any{"context": conn.Context, "authenticated": action != "logout", "environment_override": "LANGBOT_USER_TOKEN 由调用者管理；logout 后请同时 unset"}}, err
			})
		}}
		if action == "set-token" {
			cmd.Flags().BoolVar(&tokenStdin, "stdin", false, "从 stdin 读取 token")
			_ = cmd.MarkFlagRequired("stdin")
		}
		root.AddCommand(cmd)
	}
	return root
}

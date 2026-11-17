package command

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

	"github.com/langbot-app/langbot-cli/internal/api"
	"github.com/langbot-app/langbot-cli/internal/app"
	"github.com/langbot-app/langbot-cli/internal/i18n"
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
		return "", result.New("input", i18n.T("Terminal input required")+label)
	}
	if !term.IsTerminal(int(input.Fd())) {
		path := "/dev/tty"
		if runtime.GOOS == "windows" {
			path = "CONIN$"
		}
		terminal, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			return "", result.New("input", i18n.T("Terminal input required")+label)
		}
		defer terminal.Close()
		input = terminal
	}
	fmt.Fprint(deps.Err, label+": ")
	value, err := term.ReadPassword(int(input.Fd()))
	fmt.Fprintln(deps.Err)
	if err != nil {
		return "", result.New("input", i18n.T("Read")+label+i18n.T("Failed"))
	}
	return string(value), nil
}

func stdinSecret(reader io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(reader, (64<<10)+1))
	if err != nil || len(data) > 64<<10 || strings.TrimSpace(string(data)) == "" {
		return "", result.New("input", i18n.T("stdin credential is empty, too large or unreadable"))
	}
	return strings.TrimRight(string(data), "\r\n"), nil
}

func newAuthCommand(service *app.Service, deps Dependencies, flags *globalFlags) *cobra.Command {
	root := &cobra.Command{Use: "auth", Short: i18n.T("Manage member sessions")}
	var showYes bool
	show := &cobra.Command{Use: "show-key", Short: i18n.T("Print the selected API key explicitly"), Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if !showYes {
			return emitCommand(deps, flags, app.Result{}, result.New("input", i18n.T("Printing an API key requires --yes")))
		}
		key, err := service.ShowAPIKey(cmd.Context(), connectionOptions(flags))
		if err != nil {
			return emitCommand(deps, flags, app.Result{}, err)
		}
		_, err = fmt.Fprintln(deps.Out, key)
		return err
	}}
	show.Flags().BoolVar(&showYes, "yes", false, i18n.T("Confirm printing the API key"))
	root.AddCommand(show)
	var email string
	var passwordStdin, totp, tokenStdin bool
	login := &cobra.Command{Use: "login", Short: i18n.T("Log in to a local account and save member token"), Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return emitCall(deps, flags, func() (app.Result, error) {
			if flags.apiKeyStdin {
				return app.Result{}, result.New("input", i18n.T("auth login does not use --api-key-stdin"))
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
					return "", result.New("auth", i18n.T("TOTP required; rerun auth login --totp in a terminal"))
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
	login.Flags().StringVar(&email, "email", "", i18n.T("Account email"))
	login.Flags().BoolVar(&passwordStdin, "password-stdin", false, i18n.T("Read password from stdin"))
	login.Flags().BoolVar(&totp, "totp", false, i18n.T("Prompt for TOTP / recovery code when second-factor authentication is required"))
	_ = login.MarkFlagRequired("email")
	root.AddCommand(login)
	for _, action := range []string{"set-token", "logout"} {
		cmd := &cobra.Command{Use: action, Short: i18n.T("Save or clear the current context member token"), Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			return emitCall(deps, flags, func() (app.Result, error) {
				conn, err := service.AuthConnection(connectionOptions(flags))
				if err != nil {
					return app.Result{}, err
				}
				token := ""
				if action == "set-token" {
					if !tokenStdin || flags.apiKeyStdin {
						return app.Result{}, result.New("input", i18n.T("Use auth set-token --stdin; cannot combine with --api-key-stdin"))
					}
					token, err = stdinSecret(deps.In)
					if err != nil {
						return app.Result{}, err
					}
				}
				err = service.SaveMemberToken(cmd.Context(), conn, token)
				return app.Result{Data: map[string]any{"context": conn.Context, "authenticated": action != "logout", "environment_override": i18n.T("LANGBOT_USER_TOKEN is caller-managed; unset it after logout")}}, err
			})
		}}
		if action == "set-token" {
			cmd.Flags().BoolVar(&tokenStdin, "stdin", false, i18n.T("Read token from stdin"))
			_ = cmd.MarkFlagRequired("stdin")
		}
		root.AddCommand(cmd)
	}
	return root
}

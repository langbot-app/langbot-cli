package command

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/langbot-app/langbot-cli/internal/app"
	"github.com/langbot-app/langbot-cli/internal/result"
	"github.com/spf13/cobra"
)

var uploadOperations = map[string]string{"plugin config-file upload": "plugin.config_file.upload", "file image upload": "file.image.upload", "skill zip preview": "skill.zip.preview"}

func addOperationUploads(root *cobra.Command, service *app.Service, deps Dependencies, flags *globalFlags) {
	for path, identifier := range uploadOperations {
		parts := strings.Fields(path)
		var file string
		var yes bool
		cmd := &cobra.Command{Use: parts[len(parts)-1], Short: "上传本地文件：" + identifier, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			return emitCall(deps, flags, func() (app.Result, error) {
				if file == "" || file == "-" {
					return app.Result{}, result.New("input", "请使用 --file 指定本地文件")
				}
				reader, err := os.Open(file)
				if err != nil {
					return app.Result{}, result.New("input", "无法读取上传文件")
				}
				defer reader.Close()
				info, err := reader.Stat()
				if err != nil || !info.Mode().IsRegular() {
					return app.Result{}, result.New("input", "上传文件必须是普通文件")
				}
				session, err := service.OpenOperations(cmd.Context(), connectionOptions(flags), false)
				if err != nil {
					return app.Result{}, err
				}
				description := map[string]any{"permission": "resource.manage", "side_effect": identifier != "skill.zip.preview"}
				if err := session.Authorize(description); err != nil {
					return session.Result(nil), err
				}
				if app.OperationWrites(description) {
					if err := confirmOperation(deps, yes, flags.apiKeyStdin); err != nil {
						return session.Result(nil), err
					}
				}
				value, err := session.Client.OperationUpload(cmd.Context(), session.Target, identifier, filepath.Base(file), reader)
				return session.Result(value), err
			})
		}}
		cmd.Flags().StringVar(&file, "file", "", "本地文件")
		cmd.Flags().BoolVar(&yes, "yes", false, "确认上传")
		_ = cmd.MarkFlagRequired("file")
		commandGroup(root, parts[:len(parts)-1]).AddCommand(cmd)
	}
	for _, action := range []string{"preview", "execute"} {
		var file string
		var yes bool
		cmd := &cobra.Command{Use: action, Short: "旧版知识库迁移（Core 要求成员身份）", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			return emitCall(deps, flags, func() (app.Result, error) {
				body := map[string]any{}
				if file != "" {
					var err error
					body, err = loadRequestBody(cmd.Context(), file, flags.apiKeyStdin, deps.In)
					if err != nil {
						return app.Result{}, err
					}
				}
				session, err := service.OpenOperations(cmd.Context(), connectionOptions(flags), false)
				if err != nil {
					return app.Result{}, err
				}
				if action == "execute" {
					if err := session.Authorize(map[string]any{"side_effect": true, "permission": "resource.manage"}); err != nil {
						return session.Result(nil), err
					}
					if err := confirmOperation(deps, yes, file == "-" || flags.apiKeyStdin); err != nil {
						return session.Result(nil), err
					}
				}
				value, err := session.Client.KnowledgeMigration(cmd.Context(), session.Target, action == "execute", body)
				return session.Result(value), err
			})
		}}
		cmd.Flags().StringVar(&file, "file", "", "迁移输入 JSON/YAML 文件")
		cmd.Flags().BoolVar(&yes, "yes", false, "确认迁移")
		commandGroup(root, []string{"knowledge-base", "migration"}).AddCommand(cmd)
	}
}

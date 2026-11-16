package command

import (
	"os"
	"path/filepath"

	"github.com/langbot-app/langbot-cli/internal/app"
	"github.com/langbot-app/langbot-cli/internal/result"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func addImageDownload(root *cobra.Command, service *app.Service, deps Dependencies, flags *globalFlags) {
	var destination string
	cmd := &cobra.Command{Use: "get <key>", Short: "下载图片原始字节", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		writer := deps.Out
		if destination == "" {
			if output, ok := deps.Out.(*os.File); ok && term.IsTerminal(int(output.Fd())) {
				return result.New("input", "不能将图片输出到终端；请使用 -O <file>")
			}
		} else {
			file, err := os.CreateTemp(filepath.Dir(destination), ".lbctl-image-*")
			if err != nil {
				return result.New("input", "无法创建图片文件")
			}
			defer os.Remove(file.Name())
			defer file.Close()
			if err := service.ImageDownload(cmd.Context(), connectionOptions(flags), args[0], file); err != nil {
				return err
			}
			if file.Close() != nil || os.Rename(file.Name(), destination) != nil {
				return result.New("input", "无法保存图片文件")
			}
			return nil
		}
		return service.ImageDownload(cmd.Context(), connectionOptions(flags), args[0], writer)
	}}
	cmd.Flags().StringVarP(&destination, "out-file", "O", "", "保存图片到文件；默认写入重定向 stdout")
	commandGroup(root, []string{"file", "image"}).AddCommand(cmd)
}

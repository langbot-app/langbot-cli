package command

import (
	"os"
	"path/filepath"

	"github.com/langbot-app/langbot-cli/internal/app"
	"github.com/langbot-app/langbot-cli/internal/i18n"
	"github.com/langbot-app/langbot-cli/internal/result"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func addImageDownload(root *cobra.Command, service *app.Service, deps Dependencies, flags *globalFlags) {
	var destination string
	cmd := &cobra.Command{Use: "get <key>", Short: i18n.T("Download raw image bytes"), Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		writer := deps.Out
		if destination == "" {
			if output, ok := deps.Out.(*os.File); ok && term.IsTerminal(int(output.Fd())) {
				return result.New("input", i18n.T("Cannot output images to a terminal; use -O <file>"))
			}
		} else {
			file, err := os.CreateTemp(filepath.Dir(destination), ".lbctl-image-*")
			if err != nil {
				return result.New("input", i18n.T("Unable to create image file"))
			}
			defer os.Remove(file.Name())
			defer file.Close()
			if err := service.ImageDownload(cmd.Context(), connectionOptions(flags), args[0], file); err != nil {
				return err
			}
			if file.Close() != nil || os.Rename(file.Name(), destination) != nil {
				return result.New("input", i18n.T("Unable to save image file"))
			}
			return nil
		}
		return service.ImageDownload(cmd.Context(), connectionOptions(flags), args[0], writer)
	}}
	cmd.Flags().StringVarP(&destination, "out-file", "O", "", i18n.T("Save image to a file; defaults to redirected stdout"))
	commandGroup(root, []string{"file", "image"}).AddCommand(cmd)
}

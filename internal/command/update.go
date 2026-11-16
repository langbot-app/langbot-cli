package command

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/langbot-app/langbot-cli/internal/app"
	"github.com/langbot-app/langbot-cli/internal/deploy"
	"github.com/langbot-app/langbot-cli/internal/result"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func newUpdateCommand(deps Dependencies, flags *globalFlags) *cobra.Command {
	var check bool
	cmd := &cobra.Command{Use: "update", Aliases: []string{"self-update"}, Short: "Update lbctl", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 3*time.Minute)
			defer cancel()
			value, err := (deploy.Updater{Client: deps.LocalHTTP}).Update(ctx, deps.Version, check)
			if err != nil {
				err = result.New("server", err.Error())
			}
			return emitCommand(deps, flags, app.Result{Data: value}, err)
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "Check without installing")
	return cmd
}

func passiveUpdateNotice(deps Dependencies, flags *globalFlags) {
	file, ok := deps.Err.(*os.File)
	if !ok || !term.IsTerminal(int(file.Fd())) {
		return
	}
	if value, _ := deps.LookupEnv("LBCTL_NO_UPDATE_CHECK"); value == "1" {
		return
	}
	if value, _ := deps.LookupEnv("CI"); value != "" {
		return
	}
	if flags.output == "json" || flags.output == "yaml" {
		return
	}
	cache := deploy.ReadUpdateCache()
	notices := []string{}
	if comparison, err := deploy.CompareReleaseVersions("v"+strings.TrimPrefix(deps.Version, "v"), cache.LBCTL); err == nil && comparison < 0 {
		notices = append(notices, "lbctl "+deps.Version+" → "+cache.LBCTL+" (lbctl update)")
	}
	for _, version := range cache.Instances {
		if comparison, err := deploy.CompareReleaseVersions(version, cache.Core); err == nil && comparison < 0 {
			notices = append(notices, "Core "+version+" → "+cache.Core+" (lbctl upgrade --yes)")
			break
		}
	}
	if len(notices) > 0 {
		fmt.Fprintln(deps.Err, strings.Join(notices, "; "))
	}
	_ = deploy.StartUpdateRefresh()
}

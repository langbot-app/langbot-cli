package app

import (
	"context"
	"fmt"
	"os"

	"github.com/langbot-app/langbot-cli/internal/deploy"
	"github.com/langbot-app/langbot-cli/internal/i18n"
	"github.com/langbot-app/langbot-cli/internal/progress"
)

func (s *Service) deploymentContext(ctx context.Context, record deploy.Record, dir, requestedRegion string) {
	source := i18n.Human("Cloudflare exit-IP detection; global fallback")
	if requestedRegion == "stored" {
		source = i18n.Human("Saved deployment settings")
	} else if requestedRegion != "" && requestedRegion != "auto" {
		source = "--region"
	} else if value := os.Getenv("LANGBOT_REGION"); value != "" && value != "auto" {
		source = "LANGBOT_REGION"
	}
	region := record.Region
	if region == "" {
		region = "global"
		source = i18n.Human("Default deployment settings")
	}
	driver := record.Driver
	if driver == "native" {
		driver = i18n.Human("Native")
	} else if driver == "docker_compose" {
		driver = "Docker"
	}
	progress.Info(ctx, []string{fmt.Sprintf("lbctl %s · LangBot %s · %s · ", s.deps.Version, record.CoreVersion, driver) + fmt.Sprintf(i18n.Human("Region %s (%s)"), region, source), fmt.Sprintf(i18n.Human("Instance %s · %s · Port %d"), record.Name, dir, record.Port)})
}

package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/langbot-app/langbot-cli/internal/api"
	"github.com/langbot-app/langbot-cli/internal/i18n"
	"github.com/langbot-app/langbot-cli/internal/result"
)

// SkillPush publishes one complete directory and verifies the resulting revision.
func (s *Service) SkillPush(ctx context.Context, name, directory, revision string, dryRun bool, options CheckOptions) (Result, error) {
	revision = strings.TrimSpace(revision)
	if err := api.ValidateResourceID(name); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(revision) == "" {
		return Result{}, result.New("input", i18n.T("Skill base_revision must be a non-empty string"))
	}
	return withSkillDirectory(ctx, directory, func(ctx context.Context, filename string) (Result, error) {
		operation := "skill.package.publish"
		var preflight WritePreflight
		var err error
		if dryRun {
			operation = "skill.package.preview"
			preflight, err = s.readPreflight(ctx, operation, "resource.view", options)
		} else {
			preflight, err = s.WritePreflight(ctx, operation, options)
		}
		if err != nil {
			return Result{Meta: preflight.Meta()}, err
		}
		if !dryRun {
			if err := requireCapability(preflight, "skill.get"); err != nil {
				return Result{Meta: preflight.Meta()}, err
			}
		}
		file, err := os.Open(filename)
		if err != nil {
			return Result{Meta: preflight.Meta()}, result.New("input", i18n.T("Unable to read upload file"))
		}
		defer file.Close()
		client, target := s.writeClient(preflight)
		data, err := client.SkillPackageUpdate(ctx, target, name, revision, filepath.Base(filename), file, dryRun)
		if err != nil {
			return Result{Meta: preflight.Meta()}, err
		}
		data["operation"] = operation
		data["server_write"] = !dryRun
		value := Result{Data: data, Meta: preflight.Meta()}
		if dryRun {
			return value, nil
		}
		current, err := client.SkillGet(ctx, target, name)
		if err != nil {
			return value, readbackError(err)
		}
		if current["revision"] != data["proposed_revision"] {
			return value, verificationError(i18n.T("Skill revision differs after package publication"))
		}
		data["verified"] = true
		data["skill"] = current
		return value, nil
	})
}

package command

import (
	"context"
	"maps"
	"reflect"

	"github.com/langbot-app/langbot-cli/internal/api"
	"github.com/langbot-app/langbot-cli/internal/app"
	"github.com/langbot-app/langbot-cli/internal/i18n"
	"github.com/langbot-app/langbot-cli/internal/result"
	"github.com/spf13/cobra"
)

func readBotBinding(ctx context.Context, session *app.OperationSession, botID, bindingID string) (map[string]any, error) {
	raw, err := session.Client.OperationCall(ctx, session.Target, "bot.get", map[string]any{"bot_uuid": botID})
	if err != nil {
		return nil, err
	}
	bot, _ := raw.(map[string]any)
	bindings, ok := bot["event_bindings"].([]any)
	if bot["uuid"] != botID || !ok {
		return nil, result.New("incompatible", i18n.T("Invalid Bot route response"))
	}
	if bindingID == "" && len(bindings) != 1 {
		return nil, result.New("input", i18n.T("Select an existing route with --binding. Find its ID with bot get"))
	}
	var match map[string]any
	for _, value := range bindings {
		binding, _ := value.(map[string]any)
		id, _ := binding["id"].(string)
		if id != "" && (bindingID == "" || id == bindingID) {
			if match != nil {
				return nil, result.New("precondition", i18n.T("Bot route ID is ambiguous"))
			}
			match = binding
		}
	}
	if match == nil {
		return nil, result.New("not_found", i18n.T("Bot route was not found"))
	}
	return match, nil
}

func addBotRouteBind(root *cobra.Command, service *app.Service, deps Dependencies, flags *globalFlags) {
	var agent, pipeline, bindingID string
	var dryRun, yes bool
	cmd := &cobra.Command{Use: "bind <bot_uuid>", Short: i18n.T("Change the target of one Bot route"), Args: cobra.ExactArgs(1)}
	cmd.Flags().StringVar(&agent, "agent", "", i18n.T("Target Agent UUID"))
	cmd.Flags().StringVar(&pipeline, "pipeline", "", i18n.T("Target Pipeline UUID"))
	cmd.Flags().StringVar(&bindingID, "binding", "", i18n.T("Binding ID from bot get, optional for a single route"))
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, i18n.T("Preview changes"))
	cmd.Flags().BoolVar(&yes, "yes", false, i18n.T("Confirm write"))
	cmd.Example = "  lbctl bot routes bind <bot> --binding <route> --agent <agent> --dry-run\n  lbctl bot routes bind <bot> --binding <route> --agent <agent> --yes"
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		return emitCall(deps, flags, func() (app.Result, error) {
			if (agent == "") == (pipeline == "") || (cmd.Flags().Changed("binding") && bindingID == "") {
				return app.Result{}, result.New("input", i18n.T("Choose one --agent or --pipeline and a valid binding ID"))
			}
			kind, target := "agent", agent
			if pipeline != "" {
				kind, target = "pipeline", pipeline
			}
			for _, id := range []string{args[0], target} {
				if err := api.ValidateResourceID(id); err != nil {
					return app.Result{}, err
				}
			}
			ctx := cmd.Context()
			session, err := service.OpenOperations(ctx, connectionOptions(flags), false)
			if err != nil {
				return app.Result{}, err
			}
			if err := session.Authorize(map[string]any{"permission": "resource.view"}); err != nil {
				return session.Result(nil), err
			}
			if err := session.Authorize(friendlyOperationDetails["bot.routes.bind"].description()); err != nil {
				return session.Result(nil), err
			}
			before, err := readBotBinding(ctx, session, args[0], bindingID)
			if err != nil {
				return session.Result(nil), err
			}
			if !dryRun {
				if err := confirmOperation(deps, yes, flags.apiKeyStdin); err != nil {
					return session.Result(nil), err
				}
			}
			body := map[string]any{"bot_uuid": args[0], "expected_binding": before, "target_type": kind, "target_uuid": target, "dry_run": dryRun}
			raw, err := session.Client.OperationCall(ctx, session.Target, "bot.routes.bind", body)
			data := map[string]any{"uuid": args[0], "binding_id": before["id"]}
			if err != nil {
				if failure := result.AsError(err); failure.HTTPStatus == 409 && failure.ServerCode == "binding_conflict" {
					failure := result.New("precondition", i18n.T("Bot routes changed. Read the current binding before retrying"))
					failure.HTTPStatus, failure.ServerCode = 409, "binding_conflict"
					return session.Result(data), failure
				}
				return session.Result(data), err
			}
			expected := maps.Clone(before)
			expected["target_type"], expected["target_uuid"] = kind, target
			ack, _ := raw.(map[string]any)
			if ack["bot_uuid"] != args[0] || ack["dry_run"] != dryRun || ack["changed"] != !reflect.DeepEqual(before, expected) ||
				!reflect.DeepEqual(ack["before"], before) || !reflect.DeepEqual(ack["binding"], expected) {
				return session.Result(data), result.New("incompatible", i18n.T("Invalid Bot route response"))
			}
			if !dryRun {
				actual, err := readBotBinding(ctx, session, args[0], before["id"].(string))
				if err != nil {
					return session.Result(data), err
				}
				if !reflect.DeepEqual(actual, expected) {
					return session.Result(data), result.New("precondition", i18n.T("Bot route readback differs from the saved binding"))
				}
			}
			return session.Result(ack), nil
		})
	}
	commandGroup(root, []string{"bot", "routes"}).AddCommand(cmd)
}

package command

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/langbot-app/langbot-cli/internal/api"
	"github.com/langbot-app/langbot-cli/internal/app"
	"github.com/langbot-app/langbot-cli/internal/i18n"
	"github.com/langbot-app/langbot-cli/internal/redact"
	"github.com/langbot-app/langbot-cli/internal/result"
	"github.com/spf13/cobra"
)

type agentWatchEvent struct {
	sequence int64
	value    map[string]any
}

type agentWatchPage struct {
	run      map[string]any
	status   string
	terminal bool
	events   []agentWatchEvent
	more     bool
}

func addAgentRunWatch(root *cobra.Command, service *app.Service, deps Dependencies, flags *globalFlags) {
	var after int64
	var pollInterval time.Duration
	cmd := &cobra.Command{Use: "watch <processor_id> <run_id>", Short: i18n.T("Watch an existing Agent run"), Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		return emitCall(deps, flags, func() (app.Result, error) {
			data := map[string]any{"processor_id": args[0], "run_id": args[1], "last_sequence": after, "status": "unknown", "completed": false}
			for _, identifier := range args {
				if err := api.ValidateResourceID(identifier); err != nil {
					return app.Result{Data: data}, err
				}
			}
			if after < 0 {
				return app.Result{Data: data}, result.New("input", i18n.T("after-sequence must not be negative"))
			}
			if pollInterval <= 0 {
				return app.Result{Data: data}, result.New("input", i18n.T("Polling interval must be positive"))
			}
			session, err := service.OpenOperations(cmd.Context(), connectionOptions(flags), false)
			if err != nil {
				return app.Result{Data: data}, err
			}
			if err := session.Authorize(friendlyOperationDetails["agent.runs.events"].description()); err != nil {
				return session.Result(data), err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), session.Target.Timeout)
			defer cancel()
			lastSequence := after
			terminalStatus := ""
			for {
				if ctx.Err() != nil {
					err = agentWatchContextError(ctx)
					break
				}
				var value any
				value, err = session.Client.OperationCall(ctx, session.Target, "agent.runs.events", map[string]any{"processor_id": args[0], "run_id": args[1], "after_sequence": lastSequence})
				if err != nil {
					if ctx.Err() != nil {
						err = agentWatchContextError(ctx)
					}
					break
				}
				var page agentWatchPage
				page, err = parseAgentWatchPage(value, args[1])
				if err != nil {
					break
				}
				data["run"], data["status"] = page.run, page.status
				previous := lastSequence
				for _, event := range page.events {
					if ctx.Err() != nil {
						err = agentWatchContextError(ctx)
						break
					}
					if event.sequence <= lastSequence {
						continue
					}
					err = emitAgentWatchEvent(deps, flags, session, args[0], args[1], event)
					if err != nil {
						break
					}
					lastSequence = event.sequence
					data["last_sequence"] = lastSequence
				}
				if err != nil {
					break
				}
				if ctx.Err() != nil {
					err = agentWatchContextError(ctx)
					break
				}
				if page.more {
					if lastSequence == previous {
						err = invalidAgentWatchPage()
						break
					}
					continue
				}
				if page.terminal {
					if terminalStatus == page.status && lastSequence == previous {
						switch page.status {
						case "completed":
							data["completed"] = true
						case "failed":
							err = result.New("server", i18n.T("Agent run failed"))
						case "cancelled":
							err = result.New("precondition", i18n.T("Agent run cancelled"))
						case "timeout":
							err = result.New("network", i18n.T("Agent run timed out"))
						}
						break
					}
					terminalStatus = page.status
					continue
				}
				terminalStatus = ""
				timer := time.NewTimer(pollInterval)
				select {
				case <-ctx.Done():
					timer.Stop()
					err = agentWatchContextError(ctx)
				case <-timer.C:
				}
				if err != nil {
					break
				}
			}
			if flags.output == "yaml" || flags.output == "yml" {
				fmt.Fprintln(deps.Out, "---")
			}
			return session.Result(data), err
		})
	}}
	cmd.Flags().Int64Var(&after, "after-sequence", 0, i18n.T("Resume after the last emitted event sequence"))
	cmd.Flags().DurationVar(&pollInterval, "poll-interval", time.Second, i18n.T("Agent run polling interval"))
	commandGroup(root, []string{"agent", "runs"}).AddCommand(cmd)
}

func parseAgentWatchPage(value any, runID string) (agentWatchPage, error) {
	page := agentWatchPage{}
	object, ok := value.(map[string]any)
	if !ok {
		return page, invalidAgentWatchPage()
	}
	page.run, ok = object["run"].(map[string]any)
	if !ok || page.run["run_id"] != runID {
		return page, invalidAgentWatchPage()
	}
	page.status, _ = page.run["status"].(string)
	switch page.status {
	case "created", "queued", "claimed", "running":
	case "completed", "failed", "cancelled", "timeout":
		page.terminal = true
	default:
		return page, invalidAgentWatchPage()
	}
	items, ok := object["items"].([]any)
	if !ok {
		return page, invalidAgentWatchPage()
	}
	for _, item := range items {
		event, ok := item.(map[string]any)
		if !ok || event["run_id"] != runID {
			return page, invalidAgentWatchPage()
		}
		sequence, ok := agentWatchSequence(event["sequence"])
		kind, _ := event["type"].(string)
		if !ok || sequence == 0 || strings.TrimSpace(kind) == "" || strings.ContainsFunc(kind, unicode.IsControl) {
			return page, invalidAgentWatchPage()
		}
		page.events = append(page.events, agentWatchEvent{sequence: sequence, value: event})
	}
	sort.SliceStable(page.events, func(i, j int) bool { return page.events[i].sequence < page.events[j].sequence })
	page.more, ok = object["has_more"].(bool)
	if !ok {
		return page, invalidAgentWatchPage()
	}
	cursor, exists := object["next_cursor"]
	if !exists {
		return page, invalidAgentWatchPage()
	}
	sequence, valid := agentWatchSequence(cursor)
	if cursor != nil && !valid {
		return page, invalidAgentWatchPage()
	}
	if page.more && (!valid || len(page.events) == 0 || sequence != page.events[len(page.events)-1].sequence) {
		return page, invalidAgentWatchPage()
	}
	return page, nil
}

func agentWatchSequence(value any) (int64, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	sequence, err := number.Int64()
	return sequence, err == nil && sequence >= 0
}

func invalidAgentWatchPage() error {
	return result.New("incompatible", i18n.T("Invalid Agent run event response"))
}

func agentWatchContextError(ctx context.Context) error {
	if ctx.Err() == context.DeadlineExceeded {
		return result.New("network", i18n.T("Agent run watch timed out"))
	}
	return result.New("network", i18n.T("Agent run watch cancelled"))
}

func emitAgentWatchEvent(deps Dependencies, flags *globalFlags, session *app.OperationSession, processorID, runID string, event agentWatchEvent) error {
	if flags.output == "" {
		encoded, err := json.Marshal(redact.Value(event.value["data"], ""))
		if err == nil {
			_, err = fmt.Fprintf(deps.Out, "[%d] %s %s\n", event.sequence, event.value["type"], encoded)
		}
		if err != nil {
			return result.New("server", i18n.T("Output failed"))
		}
		return nil
	}
	if flags.output == "yaml" || flags.output == "yml" {
		if _, err := fmt.Fprintln(deps.Out, "---"); err != nil {
			return result.New("server", i18n.T("Output failed"))
		}
	}
	if err := emitCommand(deps, flags, session.Result(map[string]any{"processor_id": processorID, "run_id": runID, "event": event.value, "last_sequence": event.sequence}), nil); err != nil {
		return err
	}
	if flags.exitCode != 0 {
		return result.New("server", i18n.T("Output failed"))
	}
	return nil
}

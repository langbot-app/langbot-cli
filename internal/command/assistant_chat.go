package command

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/langbot-app/langbot-cli/internal/app"
	"github.com/langbot-app/langbot-cli/internal/i18n"
	"github.com/langbot-app/langbot-cli/internal/redact"
	"github.com/langbot-app/langbot-cli/internal/result"
	"github.com/spf13/cobra"
)

type chatView struct {
	id       string
	revision int64
	status   string
	data     map[string]any
}

func parseChatView(raw any, id string) (chatView, error) {
	data, _ := raw.(map[string]any)
	actual, _ := data["uuid"].(string)
	number, ok := data["revision"].(json.Number)
	rev, err := number.Int64()
	status, _ := data["status"].(string)
	pending, pendingOK := data["pending"].([]any)
	if actual == "" || (id != "" && actual != id) || !ok || err != nil || rev < 0 ||
		(status != "ready" && status != "approval" && status != "running" && status != "failed") ||
		(status == "approval" && (!pendingOK || len(pending) == 0)) {
		return chatView{}, result.New("incompatible", i18n.T("Invalid Assistant conversation response"))
	}
	if status == "approval" {
		for _, item := range pending {
			call, _ := item.(map[string]any)
			name, _ := call["name"].(string)
			if _, ok := call["arguments"].(map[string]any); name == "" || !ok {
				return chatView{}, result.New("incompatible", i18n.T("Invalid Assistant conversation response"))
			}
		}
	}
	return chatView{actual, rev, status, data}, nil
}

func readChat(ctx context.Context, session *app.OperationSession, id string) (chatView, error) {
	op, body := "assistant.get", map[string]any{"conversation_id": id}
	if id == "" {
		op, body = "assistant.create", nil
	}
	if err := session.Authorize(friendlyOperationDetails[op].description()); err != nil {
		return chatView{}, err
	}
	raw, err := session.Client.OperationCall(ctx, session.Target, op, body)
	if err != nil {
		return chatView{}, err
	}
	return parseChatView(raw, id)
}

func chatTurn(ctx context.Context, session *app.OperationSession, view chatView, body map[string]any, progress io.Writer) (chatView, error) {
	if err := session.Authorize(friendlyOperationDetails["assistant.stream"].description()); err != nil {
		return view, err
	}
	body["conversation_id"], body["revision"] = view.id, view.revision
	var completed chatView
	err := session.Client.OperationStream(ctx, session.Target, "assistant.stream", body, func(raw any) error {
		frame, _ := raw.(map[string]any)
		switch frame["kind"] {
		case "phase":
			phase, _ := frame["data"].(map[string]any)
			if progress != nil {
				switch phase["phase"] {
				case "thinking":
					round, _ := phase["round"].(json.Number)
					n, err := round.Int64()
					if err == nil && n > 0 {
						_, err = fmt.Fprintf(progress, i18n.T("Thinking (round %d)\n"), n)
						return err
					}
				case "tool":
					_, err := fmt.Fprintln(progress, i18n.T("Running a tool…"))
					return err
				}
			}
			return nil
		case "snapshot", "text":
			return nil
		case "completed":
			var err error
			completed, err = parseChatView(frame["data"], view.id)
			if err == nil && (completed.revision != view.revision+1 || completed.status == "running") {
				return result.New("incompatible", i18n.T("Invalid Assistant conversation response"))
			}
			return err
		case "error":
			if frame["code"] == "stale_turn" {
				return result.New("precondition", i18n.T("Conversation changed. Review the latest turn"))
			}
			return result.New("server", i18n.T("Assistant turn failed"))
		default:
			return result.New("incompatible", i18n.T("Invalid Assistant conversation response"))
		}
	})
	if err == nil && completed.id != "" {
		return completed, nil
	}
	if err == nil {
		err = result.New("result_unknown", i18n.T("Turn outcome is unknown. Inspect the conversation"))
	}
	// A disconnected subscriber does not stop the Core run. Only read progress.
	if ctx.Err() == nil {
		if latest, readErr := readChat(ctx, session, view.id); readErr == nil {
			return latest, err
		}
	}
	return chatView{id: view.id}, err
}

func chatResult(session *app.OperationSession, view chatView, err error) (app.Result, error) {
	data := view.data
	if data == nil {
		data = map[string]any{}
	}
	if view.id != "" {
		data["conversation_id"] = view.id
	}
	if view.status == "failed" && err == nil {
		err = result.New("server", i18n.T("Assistant turn failed"))
	}
	return session.Result(data), err
}

func chatInput(ctx context.Context, reader *bufio.Reader, prompt string, out io.Writer) (string, error) {
	fmt.Fprint(out, prompt)
	line, err := readTerminalLine(ctx, reader)
	if len(line) > 0 && err == io.EOF {
		err = nil
	}
	return strings.TrimSpace(line), err
}

// Each decision applies only to the exact batch printed in this iteration.
func chatPrompt(ctx context.Context, reader *bufio.Reader, out io.Writer, view chatView) (map[string]any, error) {
	if view.status == "approval" {
		fmt.Fprintln(out, i18n.T("Pending tool calls"), view.revision)
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(redact.Value(view.data["pending"], "")); err != nil {
			return nil, err
		}
		answer, err := chatInput(ctx, reader, i18n.T("Approve this entire batch? [yes/no/enter to leave] "), out)
		switch strings.ToLower(answer) {
		case "yes":
			return map[string]any{"approved": true}, err
		case "no":
			return map[string]any{"approved": false}, err
		default:
			return nil, err
		}
	}
	if view.status != "ready" {
		return nil, nil
	}
	messages, _ := view.data["messages"].([]any)
	if len(messages) > 0 {
		last, _ := messages[len(messages)-1].(map[string]any)
		if text, ok := last["content"].(string); last["role"] == "assistant" && ok {
			fmt.Fprintln(out, strings.Map(func(r rune) rune {
				if (r < 32 && r != '\n' && r != '\t') || (r >= 127 && r < 160) {
					return -1
				}
				return r
			}, text))
		}
	}
	line, err := chatInput(ctx, reader, i18n.T("Message (enter to leave)> "), out)
	if line == "" || err != nil {
		return nil, err
	}
	return map[string]any{"text": line}, nil
}

func addAssistantChat(root *cobra.Command, service *app.Service, deps Dependencies, flags *globalFlags) {
	var message string
	var revision int64
	var approve, reject, yes bool
	cmd := &cobra.Command{Use: "chat [conversation_id]", Short: i18n.T("Start or resume an Assistant conversation"), Args: cobra.MaximumNArgs(1)}
	cmd.Flags().StringVar(&message, "message", "", i18n.T("Message for the Assistant"))
	cmd.Flags().Int64Var(&revision, "revision", -1, i18n.T("Expected conversation revision, required for approval decisions"))
	cmd.Flags().BoolVar(&approve, "approve", false, i18n.T("Approve the pending batch at this revision"))
	cmd.Flags().BoolVar(&reject, "reject", false, i18n.T("Reject the pending batch at this revision"))
	cmd.Flags().BoolVar(&yes, "yes", false, i18n.T("Confirm write"))
	cmd.Example = "  lbctl assistant chat --message 'List my bots' --yes\n  lbctl assistant chat <id> -o json\n  lbctl assistant chat <id> --approve --revision 1 --yes"
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		return emitCall(deps, flags, func() (app.Result, error) {
			ctx := cmd.Context()
			hasMessage := cmd.Flags().Changed("message")
			hasRevision := cmd.Flags().Changed("revision")
			decision := approve || reject
			var id string
			if len(args) > 0 {
				id = strings.TrimSpace(args[0])
			}
			interactive := !flags.outputSet && inputTTY(deps) && !hasMessage && !decision
			if (len(args) > 0 && id == "") || (hasMessage && (strings.TrimSpace(message) == "" || utf8.RuneCountInString(message) > 8000)) ||
				(approve && reject) || (decision && (hasMessage || id == "" || !hasRevision)) ||
				(hasRevision && (revision < 0 || id == "" || (!hasMessage && !decision))) ||
				(id == "" && !hasMessage && !interactive) {
				return app.Result{}, result.New("input", i18n.T("Supply a message or resume a conversation. Approval requires its ID and revision"))
			}
			session, err := service.OpenOperations(ctx, connectionOptions(flags), true)
			if err != nil {
				return app.Result{}, err
			}
			write := id == "" || hasMessage || decision
			if write {
				if err := session.Authorize(friendlyOperationDetails["assistant.stream"].description()); err != nil {
					return session.Result(nil), err
				}
				if err := confirmOperationContext(ctx, deps, yes, flags.apiKeyStdin); err != nil {
					return session.Result(nil), err
				}
			}
			view, err := readChat(ctx, session, id)
			if err != nil {
				if id == "" {
					return session.Result(map[string]any{"action": "create"}), err
				}
				return chatResult(session, chatView{id: id}, err)
			}
			if (hasRevision && revision != view.revision) || (hasMessage && view.status != "ready") || (decision && view.status != "approval") {
				return chatResult(session, view, result.New("precondition", i18n.T("Conversation changed. Review the latest turn")))
			}
			reader := bufio.NewReader(deps.In)
			var progress io.Writer
			if interactive {
				progress = deps.Err
			}
			for {
				var body map[string]any
				switch {
				case hasMessage:
					body = map[string]any{"text": message}
				case decision:
					body = map[string]any{"approved": approve}
				case interactive:
					body, err = chatPrompt(ctx, reader, deps.Err, view)
				}
				if body == nil || err != nil {
					if err == io.EOF {
						err = nil
					}
					return chatResult(session, view, err)
				}
				view, err = chatTurn(ctx, session, view, body, progress)
				if !interactive || err != nil {
					return chatResult(session, view, err)
				}
			}
		})
	}
	commandGroup(root, []string{"assistant"}).AddCommand(cmd)
}

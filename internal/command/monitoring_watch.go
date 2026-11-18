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
	"github.com/langbot-app/langbot-cli/internal/result"
	"github.com/spf13/cobra"
)

const (
	monitoringDeliveryRefreshWindow = 15 * time.Minute
	monitoringWatchPageSize         = 100
	monitoringWatchRetentionLimit   = 1000
)

type monitoringWatchState struct {
	ref         monitoringTraceRef
	row         map[string]any
	deliveries  map[string]map[string]any
	offset      int64
	pending     bool
	legacy      bool
	truncated   bool
	lastChecked int
	emitted     string
}

type monitoringWatchRound struct {
	end                     time.Time
	start                   time.Time
	pages, scanned, details int
	nextOffset, total       int64
	deferred                int
	evicted, dropped        int
	expired                 int
	partial, truncated      bool
	listComplete            bool
	issues                  []map[string]any
	changed                 map[monitoringTraceRef]bool
}

type monitoringWatch struct {
	ctx                  context.Context
	session              *app.OperationSession
	bot                  string
	start                time.Time
	maxPages, maxDetails int
	states               map[monitoringTraceRef]*monitoringWatchState
	round, emitted       int
	lastComplete         time.Time
	lastCompleteStart    time.Time
	partial              bool
	evicted, dropped     int
	expired              int
	last                 *monitoringWatchRound
}

func addMonitoringWatch(root *cobra.Command, service *app.Service, deps Dependencies, flags *globalFlags) {
	var bot, since, startTime string
	var interval time.Duration
	var maxPages, maxDetails int
	cmd := &cobra.Command{Use: "watch", Short: i18n.T("Watch Bot execution states and recorded deliveries"), Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return emitCall(deps, flags, func() (app.Result, error) {
			if err := api.ValidateResourceID(bot); err != nil {
				return app.Result{}, err
			}
			if interval <= 0 || maxPages <= 0 || maxDetails <= 0 {
				return app.Result{}, result.New("input", i18n.T("Polling interval and request budgets must be positive"))
			}
			if cmd.Flags().Changed("since") && cmd.Flags().Changed("start-time") {
				return app.Result{}, result.New("input", i18n.T("since and start-time are mutually exclusive"))
			}
			now := time.Now().UTC()
			start := time.Time{}
			if cmd.Flags().Changed("start-time") {
				var err error
				start, err = time.Parse(time.RFC3339Nano, startTime)
				if err != nil || start.After(now) {
					return app.Result{}, result.New("input", i18n.T("start-time must be an ISO 8601 timestamp at or before now"))
				}
				start = start.UTC()
			} else {
				duration, err := time.ParseDuration(since)
				if err != nil || duration <= 0 {
					return app.Result{}, result.New("input", i18n.T("since must be a positive duration"))
				}
				start = now.Add(-duration)
			}
			session, err := service.OpenOperations(cmd.Context(), connectionOptions(flags), false)
			if err != nil {
				return app.Result{}, err
			}
			if err := session.Authorize(friendlyOperationDetails["monitoring.executions"].description()); err != nil {
				return session.Result(nil), err
			}
			ctx := cmd.Context()
			cancel := func() {}
			if flags.timeoutSet {
				ctx, cancel = context.WithTimeout(ctx, session.Target.Timeout)
			}
			defer cancel()
			watch := monitoringWatch{ctx: ctx, session: session, bot: bot, start: start, maxPages: maxPages, maxDetails: maxDetails, states: map[monitoringTraceRef]*monitoringWatchState{}}
			for {
				watch.round++
				round := &monitoringWatchRound{start: start, end: time.Now().UTC(), issues: []map[string]any{}, changed: map[monitoringTraceRef]bool{}}
				if watch.last != nil && !watch.last.listComplete {
					round.start = watch.last.start
				} else if watch.round > 1 && round.end.Add(-monitoringDeliveryRefreshWindow).After(round.start) {
					round.start = round.end.Add(-monitoringDeliveryRefreshWindow)
				}
				watch.last = round
				for ref, state := range watch.states {
					stamp, timed := monitoringTraceTime(state.row)
					until := state.deliveryUntil()
					if !monitoringWatchActive(state.row) && !state.pending && timed && stamp < round.start.UnixMilli() && (until.IsZero() || round.end.After(until)) {
						delete(watch.states, ref)
						round.evicted++
						round.expired++
					}
				}
				err = watch.readList(round)
				if err == nil {
					err = watch.readDetails(round)
				}
				if err != nil {
					round.issue("source_unavailable", "monitoring", err)
				}
				watch.partial = watch.partial || round.partial
				watch.evicted += round.evicted
				watch.dropped += round.dropped
				watch.expired += round.expired
				if !round.partial && err == nil {
					watch.lastComplete = round.end
					watch.lastCompleteStart = round.start
				}
				if outputErr := watch.emitRound(deps, flags); outputErr != nil {
					err = outputErr
					break
				}
				if ctx.Err() != nil {
					err = monitoringWatchContextError(ctx)
					break
				}
				if err != nil && !monitoringWatchReconnect(err) {
					break
				}
				timer := time.NewTimer(interval)
				select {
				case <-ctx.Done():
					timer.Stop()
					err = monitoringWatchContextError(ctx)
				case <-timer.C:
					err = nil
				}
				if err != nil {
					break
				}
			}
			if flags.output == "yaml" || flags.output == "yml" {
				fmt.Fprintln(deps.Out, "---")
			}
			data := watch.summary()
			data["poll_interval"], data["max_pages"], data["max_details"] = interval.String(), maxPages, maxDetails
			return session.Result(data), err
		})
	}}
	cmd.Flags().StringVar(&bot, "bot", "", i18n.T("Bot ID"))
	cmd.Flags().StringVar(&since, "since", "15m", i18n.T("Look back by a duration such as 2h"))
	cmd.Flags().StringVar(&startTime, "start-time", "", i18n.T("Resume observing from this ISO 8601 window start"))
	cmd.Flags().DurationVar(&interval, "poll-interval", 5*time.Second, i18n.T("Execution polling interval"))
	cmd.Flags().IntVar(&maxPages, "max-pages", 10, i18n.T("Maximum execution list pages per round"))
	cmd.Flags().IntVar(&maxDetails, "max-details", 20, i18n.T("Maximum delivery detail requests per round"))
	cmd.Example = "  lbctl monitoring watch --bot BOT_UUID --since 15m\n  lbctl monitoring watch --bot BOT_UUID --start-time 2026-11-18T00:00:00Z --timeout 1h -o json"
	commandGroup(root, []string{"monitoring"}).AddCommand(cmd)
}

func (round *monitoringWatchRound) issue(reason, source string, err error) {
	round.partial = true
	for _, entry := range round.issues {
		if entry["reason"] == reason && entry["source"] == source {
			return
		}
	}
	entry := map[string]any{"reason": reason, "source": source}
	if err != nil {
		entry["error"] = result.AsError(err)
	}
	round.issues = append(round.issues, entry)
}

func (watch *monitoringWatch) readList(round *monitoringWatchRound) error {
	seen := map[monitoringTraceRef]bool{}
	initialTotal := int64(-1)
	for offset := int64(0); round.pages < watch.maxPages; {
		value, err := watch.session.Client.OperationCall(watch.ctx, watch.session.Target, "monitoring.executions", map[string]any{"bot_ids": []string{watch.bot}, "include_summary": false, "limit": monitoringWatchPageSize, "offset": offset})
		if err != nil {
			return err
		}
		round.pages++
		page, ok := value.(map[string]any)
		items, itemsOK := page["items"].([]any)
		total, totalOK := monitoringTraceNumber(page["total"])
		more, moreOK := page["has_more"].(bool)
		if !ok || !itemsOK || len(items) > monitoringWatchPageSize || !totalOK || total < 0 || !moreOK {
			return invalidMonitoringTrace()
		}
		if initialTotal == -1 {
			initialTotal = total
			round.total = total
		} else if initialTotal != total {
			round.issue("list_changed_during_round", "monitoring.executions", nil)
		}
		if reported, exists := monitoringTraceNumber(page["offset"]); exists && reported != offset {
			round.truncated = true
			round.issue("list_offset_truncated", "monitoring.executions", nil)
			return nil
		}
		older, progress := len(items) > 0, false
		round.nextOffset = offset + int64(len(items))
		for _, raw := range items {
			round.scanned++
			row, ok := raw.(map[string]any)
			if !ok {
				return invalidMonitoringTrace()
			}
			ref := monitoringTraceRef{monitoringTraceID(row["source"]), monitoringTraceID(row["id"])}
			status, statusOK := row["status"].(string)
			if api.ValidateResourceID(ref.id) != nil || ref.source != "agent" && ref.source != "pipeline" && ref.source != "event" || !statusOK || strings.TrimSpace(status) == "" || strings.ContainsFunc(status, unicode.IsControl) {
				return invalidMonitoringTrace()
			}
			if reason := row["status_reason"]; reason != nil {
				if _, ok := reason.(string); !ok {
					return invalidMonitoringTrace()
				}
			}
			stamp, timed := monitoringTraceTime(row)
			older = older && timed && stamp < round.start.UnixMilli()
			if seen[ref] {
				round.issue("list_overlap", ref.source+"/"+ref.id, nil)
				continue
			}
			seen[ref], progress = true, true
			if row["bot_id"] != watch.bot {
				round.issue("execution_scope_unconfirmed", "monitoring.executions", nil)
				continue
			}
			if timed && (stamp < round.start.UnixMilli() || stamp > round.end.UnixMilli()) {
				continue
			}
			if !timed {
				round.issue("execution_time_missing", ref.source+"/"+ref.id, nil)
			}
			state := watch.states[ref]
			if state == nil {
				limit := min(watch.maxPages, monitoringWatchRetentionLimit/monitoringWatchPageSize) * monitoringWatchPageSize
				if len(watch.states) == limit {
					round.truncated = true
					var oldest *monitoringWatchState
					for _, candidate := range watch.states {
						if !monitoringWatchActive(candidate.row) && (oldest == nil || monitoringWatchEarlier(candidate.row, oldest.row)) {
							oldest = candidate
						}
					}
					if oldest == nil || !monitoringWatchActive(row) && !monitoringWatchEarlier(oldest.row, row) {
						round.dropped++
						round.issue("execution_cache_limit", "cache", nil)
						continue
					}
					delete(watch.states, oldest.ref)
					round.evicted++
					round.issue("execution_cache_limit", "cache", nil)
				}
				state = &monitoringWatchState{ref: ref, deliveries: map[string]map[string]any{}, pending: true}
				watch.states[ref] = state
			}
			if state.row == nil || state.row["status"] != status || state.row["status_reason"] != row["status_reason"] {
				round.changed[ref] = true
				state.pending = true
				state.offset = 0
			}
			state.row = map[string]any{}
			for _, key := range []string{"status", "status_reason", "status_group", "session_id", "event_id", "created_at_ms", "finished_at_ms", "progress_event"} {
				switch value := row[key].(type) {
				case string, json.Number:
					state.row[key] = value
				}
			}
		}
		if older || !more {
			if !more && offset+int64(len(items)) < total {
				round.truncated = true
				round.issue("list_page_missing", "monitoring.executions", nil)
			}
			round.listComplete = !round.partial
			return nil
		}
		offset += int64(len(items))
		if !progress || offset >= initialTotal || round.pages == watch.maxPages {
			round.truncated = true
			round.issue("list_scan_boundary", "monitoring.executions", nil)
			return nil
		}
	}
	return nil
}

func (watch *monitoringWatch) readDetails(round *monitoringWatchRound) error {
	candidates := []*monitoringWatchState{}
	for _, state := range watch.states {
		if state.truncated {
			round.truncated = true
			round.issue("delivery_cache_limit", state.ref.source+"/"+state.ref.id, nil)
		}
		until := state.deliveryUntil()
		if state.pending || monitoringWatchActive(state.row) || !until.IsZero() && !round.end.After(until) {
			candidates = append(candidates, state)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if round.changed[a.ref] != round.changed[b.ref] {
			return round.changed[a.ref]
		}
		if a.lastChecked != b.lastChecked {
			return a.lastChecked < b.lastChecked
		}
		return a.ref.source+"/"+a.ref.id < b.ref.source+"/"+b.ref.id
	})
	for index, state := range candidates {
		if round.details == watch.maxDetails {
			round.deferred = len(candidates) - index
			round.truncated = true
			round.issue("detail_budget_exhausted", "monitoring.execution.detail", nil)
			return nil
		}
		round.details++
		state.lastChecked = watch.round
		if err := watch.readDeliveryPage(round, state); err != nil {
			round.issue("delivery_source_unavailable", state.ref.source+"/"+state.ref.id, err)
			if result.AsError(err).Kind != "not_found" {
				return err
			}
		}
		if state.pending {
			// Continue long delivery histories fairly in the next round.
			round.truncated = true
			round.issue("delivery_page_pending", state.ref.source+"/"+state.ref.id, nil)
		}
	}
	return nil
}

func (watch *monitoringWatch) readDeliveryPage(round *monitoringWatchRound, state *monitoringWatchState) error {
	body := map[string]any{"source": state.ref.source, "execution_id": state.ref.id, "section": "deliveries", "limit": monitoringWatchPageSize, "offset": state.offset}
	value, err := watch.session.Client.OperationCall(watch.ctx, watch.session.Target, "monitoring.execution.detail", body)
	if err != nil {
		return err
	}
	detail, ok := value.(map[string]any)
	row, rowOK := detail["row"].(map[string]any)
	if !ok || !rowOK || row["source"] != state.ref.source || monitoringTraceID(row["id"]) != state.ref.id || row["bot_id"] != watch.bot {
		return invalidMonitoringTrace()
	}
	if status, ok := row["status"].(string); !ok || strings.ContainsFunc(status, unicode.IsControl) || status == "" {
		return invalidMonitoringTrace()
	} else {
		state.row["status"] = status
	}
	for _, key := range []string{"status_reason", "finished_at_ms"} {
		switch value := row[key].(type) {
		case string, json.Number:
			state.row[key] = value
		}
	}
	pages, _ := detail["pages"].(map[string]any)
	page, pageOK := pages["deliveries"].(map[string]any)
	items, itemsOK := page["items"].([]any)
	more, moreOK := page["has_more"].(bool)
	next, nextOK := monitoringTraceNumber(page["next_offset"])
	if !pageOK || !itemsOK || len(items) > monitoringWatchPageSize || !moreOK || more && (!nextOK || next <= state.offset || len(items) == 0) {
		return invalidMonitoringTrace()
	}
	if detail["legacy_context"] == true {
		state.legacy = true
		round.issue("legacy_context", state.ref.source+"/"+state.ref.id, nil)
	}
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok || monitoringTraceID(item["id"]) == "" {
			return invalidMonitoringTrace()
		}
		if bot, exists := item["bot_id"]; exists && bot != watch.bot {
			return invalidMonitoringTrace()
		}
		id := monitoringTraceID(item["id"])
		record := map[string]any{"id": id, "status": "unknown", "locator": map[string]any{"operation": "monitoring.execution.detail", "input": body, "record_id": id}}
		if status, ok := item["status"].(string); ok {
			record["status"] = status
		}
		if stamp, ok := monitoringTraceTime(item); ok {
			record["timestamp_ms"] = stamp
		}
		if failure := monitoringTraceFailure("deliveries", item); failure != "" {
			record["error"] = failure
		}
		previous := state.deliveries[id]
		if previous == nil && len(state.deliveries) == monitoringWatchRetentionLimit {
			state.truncated = true
			round.issue("delivery_cache_limit", state.ref.source+"/"+state.ref.id, nil)
			continue
		}
		if previous != nil && state.offset > 0 {
			round.issue("delivery_page_overlap", state.ref.source+"/"+state.ref.id, nil)
		}
		if previous == nil || previous["status"] != record["status"] || previous["error"] != record["error"] || previous["timestamp_ms"] != record["timestamp_ms"] {
			state.deliveries[id] = record
		}
	}
	state.pending = more && !state.truncated
	state.offset = 0
	if state.pending {
		state.offset = next
	}
	if more && len(state.deliveries) == monitoringWatchRetentionLimit {
		state.truncated, state.pending, state.offset = true, false, 0
		round.issue("delivery_cache_limit", state.ref.source+"/"+state.ref.id, nil)
	}
	if state.truncated {
		round.truncated = true
		round.issue("delivery_cache_limit", state.ref.source+"/"+state.ref.id, nil)
	}
	return nil
}

func monitoringWatchActive(row map[string]any) bool {
	switch row["status"] {
	case "created", "queued", "claimed", "running", "pending":
		return true
	}
	return false
}

func monitoringWatchEarlier(left, right map[string]any) bool {
	a, aOK := monitoringTraceTime(left)
	b, bOK := monitoringTraceTime(right)
	return aOK && (!bOK || a < b)
}

func (state *monitoringWatchState) deliveryUntil() time.Time {
	if monitoringWatchActive(state.row) {
		return time.Time{}
	}
	stamp, timed := monitoringTraceTime(state.row)
	if finished, ok := monitoringTraceNumber(state.row["finished_at_ms"]); ok {
		stamp, timed = finished, true
	}
	if !timed {
		return time.Time{}
	}
	return time.UnixMilli(stamp).UTC().Add(monitoringDeliveryRefreshWindow)
}

func (state *monitoringWatchState) data(end time.Time) map[string]any {
	ids := make([]string, 0, len(state.deliveries))
	for id := range state.deliveries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	records := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		records = append(records, state.deliveries[id])
	}
	evidence, ledger, deliveryStatus := "unknown", "unknown", "unknown"
	success, failed := false, false
	for _, record := range records {
		success = success || record["status"] == "success"
		failed = failed || record["error"] != nil
	}
	switch {
	case success && failed:
		deliveryStatus = "mixed"
	case failed:
		deliveryStatus = "recorded_failure"
	case success:
		deliveryStatus = "recorded_success"
	}
	if len(records) > 0 {
		evidence = "observed"
	}
	if state.ref.source == "agent" {
		ledger, _ = state.row["status"].(string)
	}
	var deadline any
	until := state.deliveryUntil()
	if !until.IsZero() {
		deadline = until.Format(time.RFC3339Nano)
	}
	return map[string]any{"source": state.ref.source, "id": state.ref.id, "execution": state.row, "ledger_status": ledger, "delivery_evidence": evidence, "delivery_status": deliveryStatus, "delivery_records": records, "delivery_scan_complete": !state.pending && !state.truncated, "delivery_truncated": state.truncated, "delivery_watch_until": deadline, "delivery_watch_expired": !until.IsZero() && end.After(until), "legacy_context": state.legacy}
}

func (watch *monitoringWatch) emitRound(deps Dependencies, flags *globalFlags) error {
	refs := make([]monitoringTraceRef, 0, len(watch.states))
	for ref := range watch.states {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].source+"/"+refs[i].id < refs[j].source+"/"+refs[j].id })
	for _, ref := range refs {
		state := watch.states[ref]
		data := state.data(watch.last.end)
		encoded, err := json.Marshal(data)
		if err != nil {
			return invalidMonitoringTrace()
		}
		if state.emitted == string(encoded) {
			continue
		}
		data["kind"], data["round"], data["observed_at"], data["bot_id"] = "execution", watch.round, time.Now().UTC().Format(time.RFC3339Nano), watch.bot
		data["window_end"] = watch.last.end.Format(time.RFC3339Nano)
		if err := emitMonitoringWatch(deps, flags, watch.session, data); err != nil {
			return err
		}
		state.emitted = string(encoded)
		watch.emitted++
	}
	data := watch.summary()
	data["kind"] = "round"
	return emitMonitoringWatch(deps, flags, watch.session, data)
}

func (watch *monitoringWatch) summary() map[string]any {
	var lastComplete any
	var lastCompleteStart any
	if !watch.lastComplete.IsZero() {
		lastComplete = watch.lastComplete.Format(time.RFC3339Nano)
		lastCompleteStart = watch.lastCompleteStart.Format(time.RFC3339Nano)
	}
	expired, truncated := 0, 0
	for _, state := range watch.states {
		if until := state.deliveryUntil(); !until.IsZero() && watch.last.end.After(until) {
			expired++
		}
		if state.truncated {
			truncated++
		}
	}
	return map[string]any{
		"kind": "summary", "bot_id": watch.bot, "start_time": watch.start.Format(time.RFC3339Nano),
		"scan_start_time": watch.last.start.Format(time.RFC3339Nano), "delivery_refresh_window": monitoringDeliveryRefreshWindow.String(),
		"cache_limit": min(watch.maxPages, monitoringWatchRetentionLimit/monitoringWatchPageSize) * monitoringWatchPageSize, "delivery_record_limit": monitoringWatchRetentionLimit,
		"cache_evicted": watch.evicted, "cache_dropped": watch.dropped, "cache_expired": watch.expired, "delivery_expired": expired, "delivery_truncated": truncated,
		"round": watch.round, "observed_at": watch.last.end.Format(time.RFC3339Nano), "last_complete_end": lastComplete, "last_complete_start": lastCompleteStart,
		"executions_retained": len(watch.states), "changes_emitted": watch.emitted, "partial": watch.partial,
		"last_round": map[string]any{"pages": watch.last.pages, "records_scanned": watch.last.scanned, "initial_total": watch.last.total, "next_offset": watch.last.nextOffset, "list_complete": watch.last.listComplete, "detail_requests": watch.last.details, "details_deferred": watch.last.deferred, "cache_evicted": watch.last.evicted, "cache_dropped": watch.last.dropped, "cache_expired": watch.last.expired, "partial": watch.last.partial, "truncated": watch.last.truncated, "issues": watch.last.issues},
	}
}

func monitoringWatchReconnect(err error) bool {
	failure := result.AsError(err)
	return failure.Kind == "network" || failure.HTTPStatus >= 500
}

func monitoringWatchContextError(ctx context.Context) error {
	if ctx.Err() == context.DeadlineExceeded {
		return result.New("network", i18n.T("Execution watch timed out"))
	}
	return result.New("network", i18n.T("Execution watch cancelled"))
}

func emitMonitoringWatch(deps Dependencies, flags *globalFlags, session *app.OperationSession, data map[string]any) error {
	if flags.output == "" {
		var err error
		if data["kind"] == "execution" {
			row := data["execution"].(map[string]any)
			until := "unknown"
			if monitoringWatchActive(row) {
				until = "active"
			}
			if deadline, ok := data["delivery_watch_until"].(string); ok {
				until = deadline
			}
			_, err = fmt.Fprintf(deps.Out, i18n.T("Execution %s/%s status=%s ledger=%s delivery=%s delivery_until=%s")+"\n", data["source"], data["id"], row["status"], data["ledger_status"], data["delivery_status"], until)
		} else {
			round := data["last_round"].(map[string]any)
			_, err = fmt.Fprintf(deps.Out, i18n.T("Round %d retained=%d pages=%d details=%d partial=%t delivery_refresh=%s expired=%d truncated=%d")+"\n", data["round"], data["executions_retained"], round["pages"], round["detail_requests"], round["partial"], data["delivery_refresh_window"], data["delivery_expired"], data["delivery_truncated"])
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
	if err := emitCommand(deps, flags, session.Result(data), nil); err != nil {
		return err
	}
	if flags.exitCode != 0 {
		return result.New("server", i18n.T("Output failed"))
	}
	return nil
}

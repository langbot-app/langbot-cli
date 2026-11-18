package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"os"
	"sort"
	"strings"

	"github.com/langbot-app/langbot-cli/internal/api"
	"github.com/langbot-app/langbot-cli/internal/i18n"
	"github.com/langbot-app/langbot-cli/internal/redact"
	"github.com/langbot-app/langbot-cli/internal/result"
)

type knowledgeExpectation struct {
	TextContains string         `json:"text_contains,omitempty"`
	ID           string         `json:"id,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

type knowledgeEvaluationCase struct {
	CaseID string               `json:"case_id"`
	Query  string               `json:"query"`
	TopK   *int                 `json:"top_k"`
	Expect knowledgeExpectation `json:"expect"`
}

type knowledgeEvaluationRow struct {
	knowledgeEvaluationCase
	Status            string        `json:"status"`
	ResultCount       int           `json:"result_count"`
	EmptyResult       bool          `json:"empty_result"`
	ScoreMissingCount int           `json:"score_missing_count"`
	ObservedIDs       []string      `json:"observed_ids"`
	MatchedRank       int           `json:"matched_rank,omitempty"`
	MatchedID         string        `json:"matched_id,omitempty"`
	Error             *result.Error `json:"error,omitempty"`
}

type knowledgeEvaluationTarget struct {
	InstanceUUID    string `json:"instance_uuid"`
	WorkspaceUUID   string `json:"workspace_uuid"`
	KnowledgeBaseID string `json:"knowledge_base_id"`
}

type knowledgeEvaluationSummary struct {
	Total      int      `json:"total"`
	Evaluated  int      `json:"evaluated"`
	Passed     int      `json:"passed"`
	Failed     int      `json:"failed"`
	Errors     int      `json:"errors"`
	Cancelled  int      `json:"cancelled"`
	NotStarted int      `json:"not_started"`
	HitRate    *float64 `json:"hit_rate"`
}

type knowledgeEvaluationComparison struct {
	BaselineHitRate *float64 `json:"baseline_hit_rate"`
	HitRateDelta    *float64 `json:"hit_rate_delta"`
	Improved        []string `json:"improved"`
	Regressed       []string `json:"regressed"`
	Stable          []string `json:"stable"`
	NotComparable   []string `json:"not_comparable"`
}

type knowledgeEvaluationReport struct {
	SchemaVersion int                            `json:"schema_version"`
	Operation     string                         `json:"operation"`
	Target        knowledgeEvaluationTarget      `json:"target"`
	CasesDigest   string                         `json:"cases_digest"`
	Cases         []knowledgeEvaluationRow       `json:"cases"`
	Summary       knowledgeEvaluationSummary     `json:"summary"`
	Comparison    *knowledgeEvaluationComparison `json:"comparison,omitempty"`
}

// KnowledgeBaseEvaluate runs each paid retrieval once and retains partial outcomes.
func (s *Service) KnowledgeBaseEvaluate(ctx context.Context, identifier string, input map[string]any, baselinePath string, options CheckOptions) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := api.ValidateResourceID(identifier); err != nil {
		return Result{}, err
	}
	cases, err := parseKnowledgeEvaluationCases(input)
	if err != nil {
		return Result{}, err
	}
	report := knowledgeEvaluationReport{
		SchemaVersion: 1, Operation: "knowledge_base.evaluate", CasesDigest: knowledgeCasesDigest(cases),
		Target: knowledgeEvaluationTarget{KnowledgeBaseID: identifier},
		Cases:  make([]knowledgeEvaluationRow, len(cases)),
	}
	for index, item := range cases {
		report.Cases[index] = knowledgeEvaluationRow{knowledgeEvaluationCase: item, Status: "not_started", ObservedIDs: []string{}}
	}
	report.Summary = knowledgeEvaluationCounts(report.Cases)
	baseline, err := loadKnowledgeEvaluationBaseline(baselinePath)
	if err != nil {
		return Result{Data: report}, err
	}
	preflight, err := s.readPreflight(ctx, "knowledge_base.retrieve", "resource.view", options)
	if err != nil {
		return Result{Data: report, Meta: preflight.Meta()}, err
	}
	report.Target.InstanceUUID, report.Target.WorkspaceUUID = preflight.Identity.InstanceUUID, preflight.Identity.WorkspaceUUID
	if baseline != nil {
		if err := validateKnowledgeBaseline(baseline, report); err != nil {
			return Result{Data: report, Meta: preflight.Meta()}, err
		}
	}
	client, target := s.writeClient(preflight)
	var firstError error
	for index := range report.Cases {
		row := &report.Cases[index]
		if ctx.Err() != nil {
			firstError = result.New("network", i18n.T("Knowledge-base evaluation cancelled"))
			break
		}
		data, callErr := client.KnowledgeBaseRetrieve(ctx, target, identifier, map[string]any{
			"query": row.Query, "retrieval_settings": map[string]any{"top_k": *row.TopK},
		})
		if callErr == nil {
			callErr = evaluateKnowledgeResults(row, data)
		}
		if callErr == nil {
			continue
		}
		row.Status, row.Error = "error", result.AsError(callErr)
		if firstError == nil {
			firstError = callErr
		}
		if ctx.Err() != nil {
			row.Status = "cancelled"
			break
		}
		if row.Error.Kind == "auth" || row.Error.Kind == "permission" || row.Error.Kind == "not_found" || row.Error.Kind == "incompatible" {
			break
		}
	}
	report.Summary = knowledgeEvaluationCounts(report.Cases)
	if baseline != nil {
		report.Comparison = compareKnowledgeEvaluation(baseline, report)
	}
	value := Result{Data: report, Meta: preflight.Meta()}
	if firstError != nil {
		return value, firstError
	}
	if report.Summary.Failed > 0 {
		failure := result.New("precondition", i18n.T("Knowledge-base evaluation assertions failed"))
		failure.Type = "evaluation_failed"
		return value, failure
	}
	return value, nil
}

func parseKnowledgeEvaluationCases(input map[string]any) ([]knowledgeEvaluationCase, error) {
	var document struct {
		TopK  *int                      `json:"top_k"`
		Cases []knowledgeEvaluationCase `json:"cases"`
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, result.New("input", i18n.T("Invalid knowledge evaluation cases"))
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if decoder.Decode(&document) != nil || len(document.Cases) == 0 {
		return nil, result.New("input", i18n.T("Invalid knowledge evaluation cases"))
	}
	topK := 5
	if document.TopK != nil {
		topK = *document.TopK
	}
	if topK < 1 {
		return nil, result.New("input", i18n.T("Each evaluation case requires a unique case_id, query, positive top_k and expectation"))
	}
	seen := map[string]bool{}
	for index := range document.Cases {
		item := &document.Cases[index]
		item.CaseID = strings.TrimSpace(item.CaseID)
		if item.TopK == nil {
			value := topK
			item.TopK = &value
		}
		if item.CaseID == "" || seen[item.CaseID] || strings.TrimSpace(item.Query) == "" || *item.TopK < 1 ||
			(item.Expect.TextContains == "" && item.Expect.ID == "" && len(item.Expect.Metadata) == 0) {
			return nil, result.New("input", i18n.T("Each evaluation case requires a unique case_id, query, positive top_k and expectation"))
		}
		seen[item.CaseID] = true
	}
	return document.Cases, nil
}

func knowledgeCasesDigest(cases []knowledgeEvaluationCase) string {
	ordered := append([]knowledgeEvaluationCase(nil), cases...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].CaseID < ordered[j].CaseID })
	encoded, _ := json.Marshal(ordered)
	return fmt.Sprintf("sha256:%x", sha256.Sum256(encoded))
}

func evaluateKnowledgeResults(row *knowledgeEvaluationRow, data any) error {
	object, ok := data.(map[string]any)
	if !ok {
		return result.New("incompatible", i18n.T("Invalid knowledge retrieval results"))
	}
	items, ok := object["results"].([]any)
	if !ok {
		return result.New("incompatible", i18n.T("Invalid knowledge retrieval results"))
	}
	if len(items) > *row.TopK {
		items = items[:*row.TopK]
	}
	row.ResultCount, row.EmptyResult, row.Status = len(items), len(items) == 0, "failed"
	for index, item := range items {
		entry, ok := item.(map[string]any)
		if !ok {
			return result.New("incompatible", i18n.T("Invalid knowledge retrieval results"))
		}
		identifier, _ := entry["id"].(string)
		row.ObservedIDs = append(row.ObservedIDs, identifier)
		if entry["score"] == nil {
			row.ScoreMissingCount++
		}
		if row.MatchedRank == 0 && matchesKnowledgeExpectation(row.Expect, entry) {
			row.Status, row.MatchedRank, row.MatchedID = "passed", index+1, identifier
		}
	}
	return nil
}

func matchesKnowledgeExpectation(expect knowledgeExpectation, entry map[string]any) bool {
	if expect.ID != "" && entry["id"] != expect.ID {
		return false
	}
	metadata, _ := entry["metadata"].(map[string]any)
	if !knowledgeMetadataMatches(expect.Metadata, metadata) {
		return false
	}
	if expect.TextContains == "" {
		return true
	}
	var text []string
	content, _ := entry["content"].([]any)
	for _, value := range content {
		part, _ := value.(map[string]any)
		if part["type"] == "text" {
			if value, ok := part["text"].(string); ok {
				text = append(text, value)
			}
		}
	}
	return strings.Contains(strings.Join(text, "\n"), expect.TextContains)
}

func knowledgeMetadataMatches(expected, actual map[string]any) bool {
	for key, value := range expected {
		observed, exists := actual[key]
		if !exists {
			return false
		}
		if nested, ok := value.(map[string]any); ok {
			observedMap, ok := observed.(map[string]any)
			if !ok || !knowledgeMetadataMatches(nested, observedMap) {
				return false
			}
			continue
		}
		if !knowledgeJSONEqual(value, observed) {
			return false
		}
	}
	return true
}

func knowledgeJSONEqual(expected, actual any) bool {
	switch value := expected.(type) {
	case []any:
		observed, ok := actual.([]any)
		if !ok || len(value) != len(observed) {
			return false
		}
		for index := range value {
			if !knowledgeJSONEqual(value[index], observed[index]) {
				return false
			}
		}
		return true
	case map[string]any:
		observed, ok := actual.(map[string]any)
		if !ok || len(value) != len(observed) {
			return false
		}
		for key, expectedValue := range value {
			actualValue, exists := observed[key]
			if !exists || !knowledgeJSONEqual(expectedValue, actualValue) {
				return false
			}
		}
		return true
	default:
		wanted, _ := json.Marshal(expected)
		found, _ := json.Marshal(actual)
		if bytes.Equal(wanted, found) {
			return true
		}
		// JSON numbers retain their exact value across integer/decimal encodings.
		wantedNumber, wantedOK := new(big.Rat).SetString(string(wanted))
		foundNumber, foundOK := new(big.Rat).SetString(string(found))
		return wantedOK && foundOK && wantedNumber.Cmp(foundNumber) == 0
	}
}

func knowledgeEvaluationCounts(rows []knowledgeEvaluationRow) knowledgeEvaluationSummary {
	summary := knowledgeEvaluationSummary{Total: len(rows)}
	for _, row := range rows {
		switch row.Status {
		case "passed":
			summary.Passed++
		case "failed":
			summary.Failed++
		case "error":
			summary.Errors++
		case "cancelled":
			summary.Cancelled++
		case "not_started":
			summary.NotStarted++
		}
	}
	summary.Evaluated = summary.Passed + summary.Failed
	if summary.Evaluated > 0 {
		rate := float64(summary.Passed) / float64(summary.Evaluated)
		summary.HitRate = &rate
	}
	return summary
}

func loadKnowledgeEvaluationBaseline(path string) (*knowledgeEvaluationReport, error) {
	if path == "" {
		return nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, result.New("input", i18n.T("Unable to read evaluation baseline"))
	}
	defer file.Close()
	var envelope struct {
		Data knowledgeEvaluationReport `json:"data"`
	}
	decoder := json.NewDecoder(file)
	decoder.UseNumber()
	if decoder.Decode(&envelope) != nil || decoder.Decode(new(any)) != io.EOF || envelope.Data.SchemaVersion != 1 || envelope.Data.Operation != "knowledge_base.evaluate" {
		return nil, result.New("input", i18n.T("Baseline must be a JSON result from knowledge-base evaluate"))
	}
	return &envelope.Data, nil
}

func validateKnowledgeBaseline(baseline *knowledgeEvaluationReport, current knowledgeEvaluationReport) error {
	if current.Target.InstanceUUID == "" || current.Target.WorkspaceUUID == "" || baseline.Target.InstanceUUID == "" || baseline.Target.WorkspaceUUID == "" {
		return result.New("precondition", i18n.T("Evaluation baseline comparison requires confirmed instance and Workspace identities"))
	}
	expected := map[string]knowledgeEvaluationCase{}
	for _, row := range current.Cases {
		expected[row.CaseID] = row.knowledgeEvaluationCase
	}
	seen := map[string]bool{}
	for _, row := range baseline.Cases {
		item, exists := expected[row.CaseID]
		if !exists || seen[row.CaseID] || row.TopK == nil || *row.TopK < 1 {
			return result.New("input", i18n.T("Evaluation baseline contains invalid case results"))
		}
		if !bytes.Equal(visibleKnowledgeRule(item), visibleKnowledgeRule(row.knowledgeEvaluationCase)) {
			return result.New("input", i18n.T("Evaluation baseline target or case rules differ from this run"))
		}
		seen[row.CaseID] = true
		switch row.Status {
		case "passed", "failed", "error", "cancelled", "not_started":
		default:
			return result.New("input", i18n.T("Evaluation baseline contains invalid case results"))
		}
	}
	if len(seen) != len(expected) || baseline.Target != current.Target || baseline.CasesDigest != current.CasesDigest {
		return result.New("input", i18n.T("Evaluation baseline target or case rules differ from this run"))
	}
	return nil
}

func visibleKnowledgeRule(item knowledgeEvaluationCase) []byte {
	// Baseline files pass through the normal output redactor. Compare the same
	// public representation while the full rules digest binds the original input.
	encoded, _ := json.Marshal(item)
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var value any
	_ = decoder.Decode(&value)
	encoded, _ = json.Marshal(redact.Value(value, ""))
	return encoded
}

func compareKnowledgeEvaluation(baseline *knowledgeEvaluationReport, current knowledgeEvaluationReport) *knowledgeEvaluationComparison {
	previous := map[string]string{}
	for _, row := range baseline.Cases {
		previous[row.CaseID] = row.Status
	}
	summary := knowledgeEvaluationCounts(baseline.Cases)
	comparison := &knowledgeEvaluationComparison{BaselineHitRate: summary.HitRate, Improved: []string{}, Regressed: []string{}, Stable: []string{}, NotComparable: []string{}}
	sameCohort := true
	for _, row := range current.Cases {
		before := previous[row.CaseID]
		beforeEvaluated := before == "passed" || before == "failed"
		nowEvaluated := row.Status == "passed" || row.Status == "failed"
		switch {
		case !beforeEvaluated || !nowEvaluated:
			if beforeEvaluated != nowEvaluated {
				sameCohort = false
			}
			comparison.NotComparable = append(comparison.NotComparable, row.CaseID)
		case before == "failed" && row.Status == "passed":
			comparison.Improved = append(comparison.Improved, row.CaseID)
		case before == "passed" && row.Status == "failed":
			comparison.Regressed = append(comparison.Regressed, row.CaseID)
		default:
			comparison.Stable = append(comparison.Stable, row.CaseID)
		}
	}
	if sameCohort && summary.HitRate != nil && current.Summary.HitRate != nil {
		delta := *current.Summary.HitRate - *summary.HitRate
		comparison.HitRateDelta = &delta
	}
	return comparison
}

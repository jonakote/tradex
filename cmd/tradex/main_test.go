package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tradex/internal/broker/ctrader"
	"tradex/internal/config"
	"tradex/internal/journal"
	"tradex/internal/reporting"
	"tradex/internal/research/evaluation"
	"tradex/internal/research/replay"
	"tradex/internal/research/robustness"
)

func TestReplayCommandWritesResultAndAudit(t *testing.T) {
	var stdout, stderr bytes.Buffer
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	manifestPath := filepath.Join(t.TempDir(), "manifest.json")
	scenarioPath := filepath.Join("..", "..", "internal", "research", "replay", "testdata", "eurusd_icc_win.json")
	if code := run([]string{"manifest", scenarioPath, manifestPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("manifest code=%d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"replay", scenarioPath, manifestPath, auditPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	var result replay.Result
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Scenario != "eurusd-london-icc-win" || result.Report.NetProfitMinor != 4620 {
		t.Fatalf("unexpected command result: %+v", result)
	}
	events, err := journal.ReadAll(auditPath)
	if err != nil || len(events) != 11 || events[0].Kind != "EXPERIMENT_MANIFEST" {
		t.Fatalf("audit events=%d err=%v", len(events), err)
	}
}

func TestReportCommandWritesVerifiedJSONAndMarkdown(t *testing.T) {
	directory := t.TempDir()
	auditPath := filepath.Join(directory, "audit.jsonl")
	resultPath := filepath.Join(directory, "result.json")
	reportPath := filepath.Join(directory, "report.json")
	markdownPath := filepath.Join(directory, "report.md")
	scenarioPath := filepath.Join("..", "..", "internal", "research", "replay", "testdata", "eurusd_icc_win.json")
	scenario, err := replay.Load(scenarioPath)
	if err != nil {
		t.Fatal(err)
	}
	result, err := replay.Run(context.Background(), scenario, auditPath)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resultPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"report", resultPath, reportPath, markdownPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("report code=%d stderr=%s", code, stderr.String())
	}
	encoded, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	var report reporting.Report
	if err := json.Unmarshal(encoded, &report); err != nil {
		t.Fatal(err)
	}
	if err := report.Verify(); err != nil {
		t.Fatal(err)
	}
	markdown, err := os.ReadFile(markdownPath)
	if err != nil || !strings.Contains(string(markdown), "Account performance") {
		t.Fatalf("invalid Markdown report: %v", err)
	}
	if strings.TrimSpace(stdout.String()) != report.ReportSHA256 {
		t.Fatalf("CLI did not print report identity: %q", stdout.String())
	}
}

func TestSharedReplayCommandsWriteResultAndAudit(t *testing.T) {
	fixturePath := filepath.Join("..", "..", "internal", "research", "replay", "testdata", "eurusd_icc_win.json")
	base, err := replay.Load(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	base.Events = base.Events[:5]
	eurusd := commandSharedLeg(base, "EURUSD", "EUR")
	gbpusd := commandSharedLeg(base, "GBPUSD", "GBP")
	shared := replay.SharedScenario{
		Name: "shared-cli", InitialEquityMinor: base.InitialEquityMinor, Risk: base.Risk,
		Legs: []replay.Scenario{gbpusd, eurusd},
	}
	payload, err := json.Marshal(shared)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	scenarioPath := filepath.Join(directory, "shared.json")
	manifestPath := filepath.Join(directory, "manifest.json")
	auditPath := filepath.Join(directory, "audit.jsonl")
	resultPath := filepath.Join(directory, "result.json")
	reportPath := filepath.Join(directory, "report.json")
	markdownPath := filepath.Join(directory, "report.md")
	if err := os.WriteFile(scenarioPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"manifest-shared", scenarioPath, manifestPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("manifest-shared code=%d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"replay-shared", scenarioPath, manifestPath, auditPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("replay-shared code=%d stderr=%s", code, stderr.String())
	}
	var result replay.SharedResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Scenario != "shared-cli" || len(result.Decisions) != 2 || len(result.Orders) != 2 {
		t.Fatalf("unexpected shared command result: %+v", result)
	}
	if err := os.WriteFile(resultPath, stdout.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	events, err := journal.ReadAll(auditPath)
	if err != nil || len(events) == 0 || events[0].Kind != "EXPERIMENT_MANIFEST" || events[len(events)-1].Kind != "REPORT" {
		t.Fatalf("shared audit events=%d err=%v", len(events), err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"report-shared", resultPath, reportPath, markdownPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("report-shared code=%d stderr=%s", code, stderr.String())
	}
	encodedReport, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	var sharedReport reporting.Report
	if err := json.Unmarshal(encodedReport, &sharedReport); err != nil {
		t.Fatal(err)
	}
	if err := sharedReport.Verify(); err != nil {
		t.Fatal(err)
	}
	if sharedReport.Mode != "shared" || len(sharedReport.Symbols) != 2 {
		t.Fatalf("unexpected shared report: %+v", sharedReport)
	}
}

func commandSharedLeg(base replay.Scenario, symbol, baseCurrency string) replay.Scenario {
	leg := base
	leg.Name = symbol + "-open"
	leg.Instrument.Symbol = symbol
	leg.Instrument.BaseCurrency = baseCurrency
	leg.Events = append([]replay.Event(nil), base.Events...)
	for index := range leg.Events {
		if leg.Events[index].Candle != nil {
			candle := *leg.Events[index].Candle
			candle.Symbol = symbol
			leg.Events[index].Candle = &candle
		}
		if leg.Events[index].Quote != nil {
			quote := *leg.Events[index].Quote
			quote.Symbol = symbol
			leg.Events[index].Quote = &quote
		}
	}
	return leg
}

func TestUsageIsAnError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(nil, &stdout, &stderr); code != 2 || stderr.Len() == 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

func TestCTraderEvidenceCommandWritesReport(t *testing.T) {
	directory := t.TempDir()
	auditPath := filepath.Join(directory, "audit.jsonl")
	evidencePath := filepath.Join(directory, "evidence.json")
	stream, err := journal.Create(auditPath, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Append(context.Background(), time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC), "CTRADER_SAFETY_BLOCKED", "acct", map[string]any{"detail": "stop"}); err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"ctrader-evidence", auditPath, evidencePath}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	var evidence map[string]any
	payload, err := os.ReadFile(evidencePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(payload, &evidence); err != nil || evidence["safety_blocked"] != float64(1) {
		t.Fatalf("evidence=%v err=%v", evidence, err)
	}
}

func TestCTraderReadinessCommandWritesReadyReport(t *testing.T) {
	directory := t.TempDir()
	inputPath := filepath.Join(directory, "readiness.json")
	reportPath := filepath.Join(directory, "readiness-report.json")
	document := ctrader.ReadinessDocument{Config: ctrader.Config{Name: "demo", AccountID: "acct", Environment: "paper", Mode: config.BrokerActiveDemo, Endpoint: "demo", ClientID: "client", CredentialReference: "ref", ManifestSHA256: strings.Repeat("a", 64)}, Reconciliation: ctrader.Reconciliation{Match: true}, Evidence: ctrader.OperationalEvidence{Healthy: true}, CircuitState: ctrader.CircuitClosed}
	payload, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inputPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"ctrader-readiness", inputPath, reportPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	var report ctrader.ReadinessReport
	encoded, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &report); err != nil || !report.Ready {
		t.Fatalf("report=%#v err=%v", report, err)
	}
}

func TestCTraderArtifactCreateAndVerifyCommands(t *testing.T) {
	directory := t.TempDir()
	inputPath := filepath.Join(directory, "readiness.json")
	keyPath := filepath.Join(directory, "key")
	artifactPath := filepath.Join(directory, "artifact.json")
	document := ctrader.ReadinessDocument{Config: ctrader.Config{Name: "demo", AccountID: "acct", Environment: "paper", Mode: config.BrokerActiveDemo, Endpoint: "demo", ClientID: "client", CredentialReference: "ref", ManifestSHA256: strings.Repeat("a", 64)}, Reconciliation: ctrader.Reconciliation{Match: true, CheckedAt: time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)}, Evidence: ctrader.OperationalEvidence{Healthy: true}, CircuitState: ctrader.CircuitClosed}
	payload, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inputPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, []byte("0123456789abcdef"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"ctrader-artifact-create", inputPath, strings.Repeat("a", 64), strings.Repeat("b", 64), keyPath, artifactPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("create code=%d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"ctrader-artifact-verify", artifactPath, keyPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("verify code=%d stderr=%s", code, stderr.String())
	}
}

func TestCTraderArtifactRotationPolicyCommand(t *testing.T) {
	directory := t.TempDir()
	inputPath := filepath.Join(directory, "readiness.json")
	keyPath := filepath.Join(directory, "key")
	policyPath := filepath.Join(directory, "policy.json")
	artifactPath := filepath.Join(directory, "artifact.json")
	document := ctrader.ReadinessDocument{Config: ctrader.Config{Name: "demo", AccountID: "acct", Environment: "paper", Mode: config.BrokerActiveDemo, Endpoint: "demo", ClientID: "client", CredentialReference: "ref", ManifestSHA256: strings.Repeat("a", 64)}, Reconciliation: ctrader.Reconciliation{Match: true, CheckedAt: time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)}, Evidence: ctrader.OperationalEvidence{Healthy: true}, CircuitState: ctrader.CircuitClosed}
	input, _ := json.Marshal(document)
	_ = os.WriteFile(inputPath, input, 0o600)
	_ = os.WriteFile(keyPath, []byte("0123456789abcdef"), 0o600)
	policy := map[string]any{"allowed_key_ids": map[string]bool{"rotated-key": true}, "allowed_versions": map[string]bool{"hmac-sha256-v2": true}}
	policyPayload, _ := json.Marshal(policy)
	_ = os.WriteFile(policyPath, policyPayload, 0o600)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"ctrader-artifact-create", inputPath, strings.Repeat("a", 64), strings.Repeat("b", 64), keyPath, "rotated-key", "hmac-sha256-v2", artifactPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("create code=%d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"ctrader-artifact-verify", artifactPath, keyPath, policyPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("verify code=%d stderr=%s", code, stderr.String())
	}
}

func TestCTraderRevocationsCommandExportsJournalState(t *testing.T) {
	directory := t.TempDir()
	auditPath := filepath.Join(directory, "audit.jsonl")
	outputPath := filepath.Join(directory, "revocations.json")
	stream, err := journal.Create(auditPath, true)
	if err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("a", 64)
	payload, _ := json.Marshal(map[string]any{"artifact_sha256": hash, "revoked_at": time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC), "reason": "superseded"})
	if _, err := stream.Append(context.Background(), time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC), "CTRADER_ARTIFACT_REVOKED", hash, json.RawMessage(payload)); err != nil {
		t.Fatal(err)
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"ctrader-revocations", auditPath, outputPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	encoded, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), "superseded") {
		t.Fatalf("output=%s", encoded)
	}
}

func TestCTraderOAuthURLCommandUsesEnvironment(t *testing.T) {
	t.Setenv("CTRADER_CLIENT_ID", "client-123")
	t.Setenv("CTRADER_CLIENT_SECRET", "local-secret")
	t.Setenv("CTRADER_REDIRECT_URI", "http://localhost:8080/oauth/callback")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"ctrader-oauth-url", "state-abc", "accounts"}, &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "client_id=client-123") || strings.Contains(stdout.String(), "local-secret") {
		t.Fatalf("unsafe or invalid URL: %s", stdout.String())
	}
}

func TestWalkForwardCommandsFreezeAndRunStudy(t *testing.T) {
	fixturePath := filepath.Join("..", "..", "internal", "research", "replay", "testdata", "eurusd_icc_win.json")
	base, err := replay.Load(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	events := make([]replay.Event, 0, len(base.Events)*5)
	for cycle := 0; cycle < 5; cycle++ {
		events = append(events, commandShiftEvents(base.Events, time.Duration(cycle)*24*time.Hour, uint64(cycle)*10)...)
	}
	first, second := base, base
	first.Name, second.Name = "candidate-a", "candidate-b"
	first.Events, second.Events = commandCloneEvents(events), commandCloneEvents(events)
	study := evaluation.Study{
		Name: "cli-walk-forward",
		Config: evaluation.Config{
			InitialTrainCycles: 2, TestCycles: 1, StepCycles: 1, HoldoutCycles: 1,
			MinTrainingTrades: 1, SelectionMetric: evaluation.SelectionScore,
		},
		Candidates: []evaluation.Candidate{{ID: "b", Scenario: second}, {ID: "a", Scenario: first}},
	}
	payload, err := json.Marshal(study)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	studyPath := filepath.Join(directory, "study.json")
	manifestPath := filepath.Join(directory, "manifest.json")
	auditDirectory := filepath.Join(directory, "audits")
	resultPath := filepath.Join(directory, "result.json")
	if err := os.WriteFile(studyPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"manifest-study", studyPath, manifestPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("manifest-study code=%d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"walk-forward", studyPath, manifestPath, auditDirectory, resultPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("walk-forward code=%d stderr=%s", code, stderr.String())
	}
	encoded, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	var result evaluation.Result
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatal(err)
	}
	if err := result.Verify(); err != nil {
		t.Fatal(err)
	}
	if result.FinalSelectedCandidateID != "a" || result.Holdout.CandidateID != "a" ||
		strings.TrimSpace(stdout.String()) != result.ResultSHA256 {
		t.Fatalf("unexpected walk-forward command result: %+v stdout=%q", result, stdout.String())
	}
}

func TestRobustnessCommandWritesVerifiedResult(t *testing.T) {
	fixturePath := filepath.Join("..", "..", "internal", "research", "replay", "testdata", "eurusd_icc_win.json")
	scenario, err := replay.Load(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	events := make([]replay.Event, 0, len(scenario.Events)*5)
	for cycle := 0; cycle < 5; cycle++ {
		events = append(events, commandShiftEvents(scenario.Events, time.Duration(cycle)*24*time.Hour, uint64(cycle)*10)...)
	}
	scenario.Name = "robustness-cli-source"
	scenario.Events = events
	directory := t.TempDir()
	replayed, err := replay.Run(context.Background(), scenario, filepath.Join(directory, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	resultPayload, err := json.Marshal(replayed)
	if err != nil {
		t.Fatal(err)
	}
	config := robustness.Config{
		Iterations: 1000, MeanBlockLength: 2, Seed: 99, RuinDrawdownBPS: 500,
		IndependentTrials: 2, TrialSharpeRatios: []float64{0.1, 0.4, 0.7},
	}
	configPayload, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	replayPath := filepath.Join(directory, "replay.json")
	configPath := filepath.Join(directory, "robustness-config.json")
	outputPath := filepath.Join(directory, "robustness.json")
	if err := os.WriteFile(replayPath, resultPayload, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, configPayload, 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"robustness", replayPath, configPath, outputPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("robustness code=%d stderr=%s", code, stderr.String())
	}
	encoded, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	var result robustness.Result
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatal(err)
	}
	if err := result.Verify(); err != nil {
		t.Fatal(err)
	}
	if result.TradeCount != 5 || strings.TrimSpace(stdout.String()) != result.ResultSHA256 {
		t.Fatalf("unexpected robustness command result: %+v stdout=%q", result, stdout.String())
	}
}

func commandShiftEvents(source []replay.Event, shift time.Duration, sequenceOffset uint64) []replay.Event {
	result := commandCloneEvents(source)
	for index := range result {
		result[index].At = result[index].At.Add(shift)
		if result[index].Candle != nil {
			result[index].Candle.OpenTime = result[index].Candle.OpenTime.Add(shift)
			result[index].Candle.CloseTime = result[index].Candle.CloseTime.Add(shift)
		}
		if result[index].Quote != nil {
			result[index].Quote.At = result[index].Quote.At.Add(shift)
			result[index].Quote.Sequence += sequenceOffset
		}
	}
	return result
}

func commandCloneEvents(source []replay.Event) []replay.Event {
	result := make([]replay.Event, len(source))
	for index, event := range source {
		result[index] = event
		if event.Candle != nil {
			candle := *event.Candle
			result[index].Candle = &candle
		}
		if event.Quote != nil {
			quote := *event.Quote
			result[index].Quote = &quote
		}
	}
	return result
}

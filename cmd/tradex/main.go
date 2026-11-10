package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"tradex/internal/broker/ctrader"
	"tradex/internal/config"
	"tradex/internal/experiment"
	"tradex/internal/journal"
	"tradex/internal/marketdata"
	"tradex/internal/reporting"
	"tradex/internal/research/evaluation"
	"tradex/internal/research/replay"
	"tradex/internal/research/robustness"
)

const version = "0.52.0-dev"

func main() {
	// Load .env file (does not override existing environment variables)
	config.LoadDotEnv(".env")
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && args[0] == "version" {
		fmt.Fprintln(stdout, version)
		return 0
	}
	if len(args) == 3 && args[0] == "ctrader-oauth-url" {
		config, err := ctrader.OAuthConfigFromEnv()
		if err != nil {
			fmt.Fprintf(stderr, "tradex: load cTrader OAuth configuration: %v\n", err)
			return 1
		}
		redirect, err := config.AuthorizationURL(args[1], args[2])
		if err != nil {
			fmt.Fprintf(stderr, "tradex: build cTrader authorization URL: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, redirect)
		return 0
	}
	if len(args) == 3 && args[0] == "ctrader-oauth-exchange" {
		config, err := ctrader.OAuthConfigFromEnv()
		if err != nil {
			fmt.Fprintf(stderr, "tradex: load cTrader OAuth configuration: %v\n", err)
			return 1
		}
		client, err := ctrader.NewOAuthClient(config, http.DefaultClient, time.Now)
		if err != nil {
			fmt.Fprintf(stderr, "tradex: build cTrader OAuth client: %v\n", err)
			return 1
		}
		token, err := client.ExchangeAuthorizationCode(context.Background(), args[1])
		if err != nil {
			fmt.Fprintf(stderr, "tradex: exchange cTrader authorization code: %v\n", err)
			return 1
		}
		if _, err := os.Stat(args[2]); err == nil {
			fmt.Fprintln(stderr, "tradex: cTrader token output already exists")
			return 1
		} else if !os.IsNotExist(err) {
			fmt.Fprintf(stderr, "tradex: inspect cTrader token output: %v\n", err)
			return 1
		}
		if err := ctrader.WriteOAuthTokenFile(args[2], token); err != nil {
			fmt.Fprintf(stderr, "tradex: write cTrader token: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "cTrader OAuth token stored")
		return 0
	}
	if len(args) == 2 && args[0] == "ctrader-oauth-bootstrap" {
		return runCTraderOAuthBootstrap(args[1], stdout, stderr)
	}
	if len(args) == 3 && args[0] == "ctrader-evidence" {
		events, err := journal.ReadAll(args[1])
		if err != nil {
			fmt.Fprintf(stderr, "tradex: read cTrader journal: %v\n", err)
			return 1
		}
		report, err := ctrader.BuildOperationalEvidence(events, nil)
		if err != nil {
			fmt.Fprintf(stderr, "tradex: build cTrader evidence: %v\n", err)
			return 1
		}
		encoded, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			fmt.Fprintf(stderr, "tradex: encode cTrader evidence: %v\n", err)
			return 1
		}
		encoded = append(encoded, '\n')
		if _, err := os.Stat(args[2]); err == nil {
			fmt.Fprintln(stderr, "tradex: cTrader evidence output already exists")
			return 1
		} else if !os.IsNotExist(err) {
			fmt.Fprintf(stderr, "tradex: inspect cTrader evidence output: %v\n", err)
			return 1
		}
		if err := os.WriteFile(args[2], encoded, 0o600); err != nil {
			fmt.Fprintf(stderr, "tradex: write cTrader evidence: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "cTrader operational evidence written")
		if !report.Healthy {
			return 1
		}
		return 0
	}
	if len(args) == 3 && args[0] == "ctrader-readiness" {
		var document ctrader.ReadinessDocument
		if err := loadJSONStrict(args[1], &document); err != nil {
			fmt.Fprintf(stderr, "tradex: load cTrader readiness document: %v\n", err)
			return 1
		}
		report := ctrader.CheckReadinessDocument(document)
		encoded, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			fmt.Fprintf(stderr, "tradex: encode cTrader readiness: %v\n", err)
			return 1
		}
		encoded = append(encoded, '\n')
		if _, err := os.Stat(args[2]); err == nil {
			fmt.Fprintln(stderr, "tradex: cTrader readiness output already exists")
			return 1
		} else if !os.IsNotExist(err) {
			fmt.Fprintf(stderr, "tradex: inspect cTrader readiness output: %v\n", err)
			return 1
		}
		if err := os.WriteFile(args[2], encoded, 0o600); err != nil {
			fmt.Fprintf(stderr, "tradex: write cTrader readiness: %v\n", err)
			return 1
		}
		if !report.Ready {
			fmt.Fprintln(stderr, "tradex: deployment is not ready")
			return 1
		}
		fmt.Fprintln(stdout, "cTrader deployment is ready")
		return 0
	}
	if len(args) == 6 && args[0] == "ctrader-artifact-create" {
		var document ctrader.ReadinessDocument
		if err := loadJSONStrict(args[1], &document); err != nil {
			fmt.Fprintf(stderr, "tradex: load cTrader readiness document: %v\n", err)
			return 1
		}
		key, err := os.ReadFile(args[4])
		if err != nil {
			fmt.Fprintf(stderr, "tradex: read artifact signing key: %v\n", err)
			return 1
		}
		signer, err := ctrader.NewHMACSHA256Signer(key)
		if err != nil {
			fmt.Fprintf(stderr, "tradex: build artifact signer: %v\n", err)
			return 1
		}
		if document.Reconciliation.CheckedAt.IsZero() {
			fmt.Fprintln(stderr, "tradex: readiness reconciliation timestamp is required")
			return 1
		}
		artifact, err := ctrader.BuildReadinessArtifact(document, args[2], args[3], document.Reconciliation.CheckedAt, time.Now().UTC(), signer)
		if err != nil {
			fmt.Fprintf(stderr, "tradex: build cTrader readiness artifact: %v\n", err)
			return 1
		}
		encoded, err := json.MarshalIndent(artifact, "", "  ")
		if err != nil {
			fmt.Fprintf(stderr, "tradex: encode cTrader readiness artifact: %v\n", err)
			return 1
		}
		encoded = append(encoded, '\n')
		if _, err := os.Stat(args[5]); err == nil {
			fmt.Fprintln(stderr, "tradex: cTrader artifact output already exists")
			return 1
		} else if !os.IsNotExist(err) {
			fmt.Fprintf(stderr, "tradex: inspect cTrader artifact output: %v\n", err)
			return 1
		}
		if err := os.WriteFile(args[5], encoded, 0o600); err != nil {
			fmt.Fprintf(stderr, "tradex: write cTrader readiness artifact: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "cTrader readiness artifact written")
		return 0
	}
	if len(args) == 8 && args[0] == "ctrader-artifact-create" {
		var document ctrader.ReadinessDocument
		if err := loadJSONStrict(args[1], &document); err != nil {
			fmt.Fprintf(stderr, "tradex: load cTrader readiness document: %v\n", err)
			return 1
		}
		key, err := os.ReadFile(args[4])
		if err != nil {
			fmt.Fprintf(stderr, "tradex: read artifact signing key: %v\n", err)
			return 1
		}
		signer, err := ctrader.NewHMACSHA256Signer(key)
		if err != nil {
			fmt.Fprintf(stderr, "tradex: build artifact signer: %v\n", err)
			return 1
		}
		if document.Reconciliation.CheckedAt.IsZero() {
			fmt.Fprintln(stderr, "tradex: readiness reconciliation timestamp is required")
			return 1
		}
		artifact, err := ctrader.BuildReadinessArtifactWithMetadata(document, args[2], args[3], document.Reconciliation.CheckedAt, time.Now().UTC(), args[5], args[6], signer)
		if err != nil {
			fmt.Fprintf(stderr, "tradex: build cTrader readiness artifact: %v\n", err)
			return 1
		}
		encoded, err := json.MarshalIndent(artifact, "", "  ")
		if err != nil {
			fmt.Fprintf(stderr, "tradex: encode cTrader readiness artifact: %v\n", err)
			return 1
		}
		encoded = append(encoded, '\n')
		if _, err := os.Stat(args[7]); err == nil {
			fmt.Fprintln(stderr, "tradex: cTrader artifact output already exists")
			return 1
		} else if !os.IsNotExist(err) {
			fmt.Fprintf(stderr, "tradex: inspect cTrader artifact output: %v\n", err)
			return 1
		}
		if err := os.WriteFile(args[7], encoded, 0o600); err != nil {
			fmt.Fprintf(stderr, "tradex: write cTrader readiness artifact: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "cTrader readiness artifact written")
		return 0
	}
	if len(args) == 3 && args[0] == "ctrader-artifact-verify" {
		var artifact ctrader.ReadinessArtifact
		if err := loadJSONStrict(args[1], &artifact); err != nil {
			fmt.Fprintf(stderr, "tradex: load cTrader readiness artifact: %v\n", err)
			return 1
		}
		key, err := os.ReadFile(args[2])
		if err != nil {
			fmt.Fprintf(stderr, "tradex: read artifact signing key: %v\n", err)
			return 1
		}
		signer, err := ctrader.NewHMACSHA256Signer(key)
		if err != nil {
			fmt.Fprintf(stderr, "tradex: build artifact signer: %v\n", err)
			return 1
		}
		if err := artifact.Verify(signer); err != nil {
			fmt.Fprintf(stderr, "tradex: verify cTrader readiness artifact: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "cTrader readiness artifact verified")
		return 0
	}
	if len(args) == 4 && args[0] == "ctrader-artifact-verify" {
		var artifact ctrader.ReadinessArtifact
		if err := loadJSONStrict(args[1], &artifact); err != nil {
			fmt.Fprintf(stderr, "tradex: load cTrader readiness artifact: %v\n", err)
			return 1
		}
		key, err := os.ReadFile(args[2])
		if err != nil {
			fmt.Fprintf(stderr, "tradex: read artifact signing key: %v\n", err)
			return 1
		}
		signer, err := ctrader.NewHMACSHA256Signer(key)
		if err != nil {
			fmt.Fprintf(stderr, "tradex: build artifact signer: %v\n", err)
			return 1
		}
		var policy struct {
			AllowedKeyIDs               map[string]bool `json:"allowed_key_ids"`
			AllowedVersions             map[string]bool `json:"allowed_versions"`
			MaxArtifactAgeSeconds       int64           `json:"max_artifact_age_seconds"`
			MaxReconciliationAgeSeconds int64           `json:"max_reconciliation_age_seconds"`
			ExpectedAudience            string          `json:"expected_audience"`
			ExpectedPurpose             string          `json:"expected_purpose"`
			AllowedSchemaVersions       map[string]bool `json:"allowed_schema_versions"`
			RevokedArtifactHashes       map[string]bool `json:"revoked_artifact_hashes"`
		}
		if err := loadJSONStrict(args[3], &policy); err != nil {
			fmt.Fprintf(stderr, "tradex: load signer policy: %v\n", err)
			return 1
		}
		if err := artifact.VerifyWithPolicyAt(signer, ctrader.SignerPolicy{AllowedKeyIDs: policy.AllowedKeyIDs, AllowedVersions: policy.AllowedVersions, MaxArtifactAge: time.Duration(policy.MaxArtifactAgeSeconds) * time.Second, MaxReconciliationAge: time.Duration(policy.MaxReconciliationAgeSeconds) * time.Second, ExpectedAudience: policy.ExpectedAudience, ExpectedPurpose: policy.ExpectedPurpose, AllowedSchemaVersions: policy.AllowedSchemaVersions, RevokedArtifactHashes: policy.RevokedArtifactHashes}, time.Now().UTC()); err != nil {
			fmt.Fprintf(stderr, "tradex: verify cTrader readiness artifact: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "cTrader readiness artifact verified")
		return 0
	}
	if len(args) == 3 && args[0] == "ctrader-revocations" {
		events, err := journal.ReadAll(args[1])
		if err != nil {
			fmt.Fprintf(stderr, "tradex: read cTrader journal: %v\n", err)
			return 1
		}
		list, err := ctrader.LoadRevocations(events)
		if err != nil {
			fmt.Fprintf(stderr, "tradex: load cTrader revocations: %v\n", err)
			return 1
		}
		encoded, err := json.MarshalIndent(list.Entries(), "", "  ")
		if err != nil {
			fmt.Fprintf(stderr, "tradex: encode cTrader revocations: %v\n", err)
			return 1
		}
		encoded = append(encoded, '\n')
		if _, err := os.Stat(args[2]); err == nil {
			fmt.Fprintln(stderr, "tradex: cTrader revocations output already exists")
			return 1
		} else if !os.IsNotExist(err) {
			fmt.Fprintf(stderr, "tradex: inspect cTrader revocations output: %v\n", err)
			return 1
		}
		if err := os.WriteFile(args[2], encoded, 0o600); err != nil {
			fmt.Fprintf(stderr, "tradex: write cTrader revocations: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "cTrader revocations written")
		return 0
	}
	if len(args) == 5 && args[0] == "ctrader-artifact-verify-journal" {
		var artifact ctrader.ReadinessArtifact
		if err := loadJSONStrict(args[1], &artifact); err != nil {
			fmt.Fprintf(stderr, "tradex: load cTrader readiness artifact: %v\n", err)
			return 1
		}
		key, err := os.ReadFile(args[2])
		if err != nil {
			fmt.Fprintf(stderr, "tradex: read artifact signing key: %v\n", err)
			return 1
		}
		signer, err := ctrader.NewHMACSHA256Signer(key)
		if err != nil {
			fmt.Fprintf(stderr, "tradex: build artifact signer: %v\n", err)
			return 1
		}
		policy, err := loadArtifactPolicy(args[3])
		if err != nil {
			fmt.Fprintf(stderr, "tradex: load signer policy: %v\n", err)
			return 1
		}
		events, err := journal.ReadAll(args[4])
		if err != nil {
			fmt.Fprintf(stderr, "tradex: read cTrader journal: %v\n", err)
			return 1
		}
		revocations, err := ctrader.LoadRevocations(events)
		if err != nil {
			fmt.Fprintf(stderr, "tradex: load cTrader revocations: %v\n", err)
			return 1
		}
		policy.RevokedArtifactHashes = make(map[string]bool)
		for _, entry := range revocations.Entries() {
			policy.RevokedArtifactHashes[entry.ArtifactSHA256] = true
		}
		if err := artifact.VerifyWithPolicyAt(signer, policy, time.Now().UTC()); err != nil {
			fmt.Fprintf(stderr, "tradex: verify cTrader readiness artifact: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "cTrader readiness artifact verified against journal revocations")
		return 0
	}
	if len(args) == 3 && args[0] == "compile-raw" {
		raw, err := replay.LoadRaw(args[1])
		if err != nil {
			fmt.Fprintf(stderr, "tradex: load raw scenario: %v\n", err)
			return 1
		}
		scenario, err := replay.CompileRaw(raw)
		if err != nil {
			fmt.Fprintf(stderr, "tradex: compile raw scenario: %v\n", err)
			return 1
		}
		if err := replay.WriteScenarioNew(args[2], scenario); err != nil {
			fmt.Fprintf(stderr, "tradex: write compiled scenario: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "%d derived events\n", len(scenario.Events))
		return 0
	}
	if len(args) == 4 && args[0] == "compile-quotes" {
		base, err := replay.Load(args[1])
		if err != nil {
			fmt.Fprintf(stderr, "tradex: load base scenario: %v\n", err)
			return 1
		}
		quotes, err := marketdata.LoadQuoteDataset(args[2])
		if err != nil {
			fmt.Fprintf(stderr, "tradex: load quote dataset: %v\n", err)
			return 1
		}
		scenario, err := replay.CompileQuotes(base, quotes)
		if err != nil {
			fmt.Fprintf(stderr, "tradex: compile quotes: %v\n", err)
			return 1
		}
		if err := replay.WriteScenarioNew(args[3], scenario); err != nil {
			fmt.Fprintf(stderr, "tradex: write quote-compiled scenario: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "%d events including broker quotes\n", len(scenario.Events))
		return 0
	}
	if len(args) == 4 && args[0] == "compile-schedule" {
		base, err := replay.Load(args[1])
		if err != nil {
			fmt.Fprintf(stderr, "tradex: load base scenario: %v\n", err)
			return 1
		}
		schedule, err := marketdata.LoadBrokerSchedule(args[2])
		if err != nil {
			fmt.Fprintf(stderr, "tradex: load broker schedule: %v\n", err)
			return 1
		}
		scenario, err := replay.CompileBrokerSchedule(base, schedule)
		if err != nil {
			fmt.Fprintf(stderr, "tradex: compile broker schedule: %v\n", err)
			return 1
		}
		if err := replay.WriteScenarioNew(args[3], scenario); err != nil {
			fmt.Fprintf(stderr, "tradex: write schedule-compiled scenario: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "%d events including market schedule\n", len(scenario.Events))
		return 0
	}
	if len(args) == 4 && args[0] == "normalize-quotes" {
		raw, err := marketdata.LoadRawQuoteDataset(args[1])
		if err != nil {
			fmt.Fprintf(stderr, "tradex: load raw quotes: %v\n", err)
			return 1
		}
		var config marketdata.QuoteNormalization
		if err := loadJSONStrict(args[2], &config); err != nil {
			fmt.Fprintf(stderr, "tradex: load quote normalization: %v\n", err)
			return 1
		}
		normalized, err := marketdata.NormalizeQuotes(raw, config)
		if err != nil {
			fmt.Fprintf(stderr, "tradex: normalize quotes: %v\n", err)
			return 1
		}
		encoded, err := json.MarshalIndent(normalized, "", "  ")
		if err != nil {
			fmt.Fprintf(stderr, "tradex: encode normalized quotes: %v\n", err)
			return 1
		}
		encoded = append(encoded, '\n')
		if _, err := os.Stat(args[3]); err == nil {
			fmt.Fprintln(stderr, "tradex: normalized quote output already exists")
			return 1
		}
		if err := os.WriteFile(args[3], encoded, 0o600); err != nil {
			fmt.Fprintf(stderr, "tradex: write normalized quotes: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, "normalized quote dataset written")
		return 0
	}
	if len(args) == 3 && args[0] == "manifest" {
		scenario, err := replay.Load(args[1])
		if err != nil {
			fmt.Fprintf(stderr, "tradex: load scenario: %v\n", err)
			return 1
		}
		manifest, err := replay.ManifestFor(scenario)
		if err != nil {
			fmt.Fprintf(stderr, "tradex: build manifest: %v\n", err)
			return 1
		}
		if err := experiment.WriteNew(args[2], manifest); err != nil {
			fmt.Fprintf(stderr, "tradex: write manifest: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, manifest.ManifestSHA256)
		return 0
	}
	if len(args) == 3 && args[0] == "manifest-shared" {
		scenario, err := replay.LoadShared(args[1])
		if err != nil {
			fmt.Fprintf(stderr, "tradex: load shared scenario: %v\n", err)
			return 1
		}
		manifest, err := replay.SharedManifestFor(scenario)
		if err != nil {
			fmt.Fprintf(stderr, "tradex: build shared manifest: %v\n", err)
			return 1
		}
		if err := experiment.WriteNew(args[2], manifest); err != nil {
			fmt.Fprintf(stderr, "tradex: write shared manifest: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, manifest.ManifestSHA256)
		return 0
	}
	if len(args) == 3 && args[0] == "manifest-study" {
		study, err := evaluation.Load(args[1])
		if err != nil {
			fmt.Fprintf(stderr, "tradex: load walk-forward study: %v\n", err)
			return 1
		}
		manifest, err := evaluation.ManifestFor(study)
		if err != nil {
			fmt.Fprintf(stderr, "tradex: build walk-forward manifest: %v\n", err)
			return 1
		}
		if err := experiment.WriteNew(args[2], manifest); err != nil {
			fmt.Fprintf(stderr, "tradex: write walk-forward manifest: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, manifest.ManifestSHA256)
		return 0
	}
	if len(args) == 3 && args[0] == "manifest-study-shared" {
		study, err := evaluation.LoadSharedStudy(args[1])
		if err != nil {
			fmt.Fprintf(stderr, "tradex: load shared walk-forward study: %v\n", err)
			return 1
		}
		manifest, err := evaluation.ManifestForShared(study)
		if err != nil {
			fmt.Fprintf(stderr, "tradex: build shared walk-forward manifest: %v\n", err)
			return 1
		}
		if err := experiment.WriteNew(args[2], manifest); err != nil {
			fmt.Fprintf(stderr, "tradex: write shared walk-forward manifest: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, manifest.ManifestSHA256)
		return 0
	}
	if len(args) == 4 && args[0] == "replay" {
		scenario, err := replay.Load(args[1])
		if err != nil {
			fmt.Fprintf(stderr, "tradex: load scenario: %v\n", err)
			return 1
		}
		manifest, err := experiment.Load(args[2])
		if err != nil {
			fmt.Fprintf(stderr, "tradex: load manifest: %v\n", err)
			return 1
		}
		result, err := replay.RunVerified(context.Background(), scenario, manifest, args[3])
		if err != nil {
			fmt.Fprintf(stderr, "tradex: run verified replay: %v\n", err)
			return 1
		}
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(result); err != nil {
			fmt.Fprintf(stderr, "tradex: encode replay result: %v\n", err)
			return 1
		}
		return 0
	}
	if len(args) == 4 && args[0] == "replay-shared" {
		scenario, err := replay.LoadShared(args[1])
		if err != nil {
			fmt.Fprintf(stderr, "tradex: load shared scenario: %v\n", err)
			return 1
		}
		manifest, err := experiment.Load(args[2])
		if err != nil {
			fmt.Fprintf(stderr, "tradex: load shared manifest: %v\n", err)
			return 1
		}
		result, err := replay.RunSharedVerified(context.Background(), scenario, manifest, args[3])
		if err != nil {
			fmt.Fprintf(stderr, "tradex: run verified shared replay: %v\n", err)
			return 1
		}
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(result); err != nil {
			fmt.Fprintf(stderr, "tradex: encode shared replay result: %v\n", err)
			return 1
		}
		return 0
	}
	if len(args) == 4 && args[0] == "report" {
		var result replay.Result
		if err := loadJSONStrict(args[1], &result); err != nil {
			fmt.Fprintf(stderr, "tradex: load replay result: %v\n", err)
			return 1
		}
		report, err := reporting.FromSingle(result)
		if err != nil {
			fmt.Fprintf(stderr, "tradex: build research report: %v\n", err)
			return 1
		}
		if err := reporting.WriteBundleNew(args[2], args[3], report); err != nil {
			fmt.Fprintf(stderr, "tradex: write research report: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, report.ReportSHA256)
		return 0
	}
	if len(args) == 4 && args[0] == "report-shared" {
		var result replay.SharedResult
		if err := loadJSONStrict(args[1], &result); err != nil {
			fmt.Fprintf(stderr, "tradex: load shared replay result: %v\n", err)
			return 1
		}
		report, err := reporting.FromShared(result)
		if err != nil {
			fmt.Fprintf(stderr, "tradex: build shared research report: %v\n", err)
			return 1
		}
		if err := reporting.WriteBundleNew(args[2], args[3], report); err != nil {
			fmt.Fprintf(stderr, "tradex: write shared research report: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, report.ReportSHA256)
		return 0
	}
	if len(args) == 4 && (args[0] == "robustness" || args[0] == "robustness-shared") {
		if _, err := os.Stat(args[3]); err == nil {
			fmt.Fprintln(stderr, "tradex: robustness result already exists")
			return 1
		} else if !os.IsNotExist(err) {
			fmt.Fprintf(stderr, "tradex: inspect robustness result: %v\n", err)
			return 1
		}
		config, err := robustness.LoadConfig(args[2])
		if err != nil {
			fmt.Fprintf(stderr, "tradex: load robustness config: %v\n", err)
			return 1
		}
		var result robustness.Result
		if args[0] == "robustness" {
			var replayResult replay.Result
			if err := loadJSONStrict(args[1], &replayResult); err != nil {
				fmt.Fprintf(stderr, "tradex: load replay result: %v\n", err)
				return 1
			}
			result, err = robustness.FromSingle(replayResult, config)
		} else {
			var replayResult replay.SharedResult
			if err := loadJSONStrict(args[1], &replayResult); err != nil {
				fmt.Fprintf(stderr, "tradex: load shared replay result: %v\n", err)
				return 1
			}
			result, err = robustness.FromShared(replayResult, config)
		}
		if err != nil {
			fmt.Fprintf(stderr, "tradex: build robustness analysis: %v\n", err)
			return 1
		}
		if err := robustness.WriteResultNew(args[3], result); err != nil {
			fmt.Fprintf(stderr, "tradex: write robustness result: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, result.ResultSHA256)
		return 0
	}
	if len(args) == 5 && args[0] == "walk-forward" {
		if _, err := os.Stat(args[4]); err == nil {
			fmt.Fprintln(stderr, "tradex: walk-forward result already exists")
			return 1
		} else if !os.IsNotExist(err) {
			fmt.Fprintf(stderr, "tradex: inspect walk-forward result: %v\n", err)
			return 1
		}
		study, err := evaluation.Load(args[1])
		if err != nil {
			fmt.Fprintf(stderr, "tradex: load walk-forward study: %v\n", err)
			return 1
		}
		manifest, err := experiment.Load(args[2])
		if err != nil {
			fmt.Fprintf(stderr, "tradex: load walk-forward manifest: %v\n", err)
			return 1
		}
		result, err := evaluation.RunVerified(context.Background(), study, manifest, args[3])
		if err != nil {
			fmt.Fprintf(stderr, "tradex: run walk-forward study: %v\n", err)
			return 1
		}
		if err := evaluation.WriteResultNew(args[4], result); err != nil {
			fmt.Fprintf(stderr, "tradex: write walk-forward result: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, result.ResultSHA256)
		return 0
	}
	if len(args) == 5 && args[0] == "walk-forward-shared" {
		if _, err := os.Stat(args[4]); err == nil {
			fmt.Fprintln(stderr, "tradex: shared walk-forward result already exists")
			return 1
		} else if !os.IsNotExist(err) {
			fmt.Fprintf(stderr, "tradex: inspect shared walk-forward result: %v\n", err)
			return 1
		}
		study, err := evaluation.LoadSharedStudy(args[1])
		if err != nil {
			fmt.Fprintf(stderr, "tradex: load shared walk-forward study: %v\n", err)
			return 1
		}
		manifest, err := experiment.Load(args[2])
		if err != nil {
			fmt.Fprintf(stderr, "tradex: load shared walk-forward manifest: %v\n", err)
			return 1
		}
		result, err := evaluation.RunSharedVerified(context.Background(), study, manifest, args[3])
		if err != nil {
			fmt.Fprintf(stderr, "tradex: run shared walk-forward study: %v\n", err)
			return 1
		}
		if err := evaluation.WriteSharedResultNew(args[4], result); err != nil {
			fmt.Fprintf(stderr, "tradex: write shared walk-forward result: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, result.ResultSHA256)
		return 0
	}

	if len(args) >= 1 && args[0] == "signals" {
		return runSignals(args[1:], stdout, stderr)
	}

	if len(args) >= 1 && args[0] == "price" {
		return runPrice(args[1:], stdout, stderr)
	}

	if len(args) >= 1 && args[0] == "watch" {
		return runWatch(args[1:], stdout, stderr)
	}

	if len(args) >= 1 && args[0] == "candles" {
		return runCandles(args[1:], stdout, stderr)
	}

	if len(args) >= 1 && args[0] == "outcomes" {
		return runOutcomes(args[1:], stdout, stderr)
	}

	fmt.Fprintln(stderr, "tradex: deterministic trading research")
	fmt.Fprintln(stderr, "usage:")
	fmt.Fprintln(stderr, "  tradex version")
	fmt.Fprintln(stderr, "  tradex ctrader-oauth-url <state> <scope>")
	fmt.Fprintln(stderr, "  tradex ctrader-oauth-exchange <authorization-code> <token-file>")
	fmt.Fprintln(stderr, "  tradex ctrader-oauth-bootstrap <token-file>")
	fmt.Fprintln(stderr, "  tradex ctrader-evidence <audit.jsonl> <evidence.json>")
	fmt.Fprintln(stderr, "  tradex ctrader-readiness <readiness.json> <report.json>")
	fmt.Fprintln(stderr, "  tradex ctrader-artifact-create <readiness.json> <manifest-sha256> <evidence-sha256> <key-file> <artifact.json>")
	fmt.Fprintln(stderr, "  tradex ctrader-artifact-create <readiness.json> <manifest-sha256> <evidence-sha256> <key-file> <key-id> <signer-version> <artifact.json>")
	fmt.Fprintln(stderr, "  tradex ctrader-artifact-verify <artifact.json> <key-file>")
	fmt.Fprintln(stderr, "  tradex ctrader-artifact-verify <artifact.json> <key-file> <policy.json>")
	fmt.Fprintln(stderr, "  tradex ctrader-revocations <audit.jsonl> <revocations.json>")
	fmt.Fprintln(stderr, "  tradex ctrader-artifact-verify-journal <artifact.json> <key-file> <policy.json> <audit.jsonl>")
	fmt.Fprintln(stderr, "  tradex compile-raw <raw.json> <scenario.json>")
	fmt.Fprintln(stderr, "  tradex compile-quotes <base-scenario.json> <quotes.json> <scenario.json>")
	fmt.Fprintln(stderr, "  tradex compile-schedule <base-scenario.json> <schedule.json> <scenario.json>")
	fmt.Fprintln(stderr, "  tradex normalize-quotes <raw-quotes.json> <normalization.json> <quotes.json>")
	fmt.Fprintln(stderr, "  tradex manifest <scenario.json> <manifest.json>")
	fmt.Fprintln(stderr, "  tradex replay <scenario.json> <manifest.json> <audit.jsonl>")
	fmt.Fprintln(stderr, "  tradex manifest-shared <shared-scenario.json> <manifest.json>")
	fmt.Fprintln(stderr, "  tradex replay-shared <shared-scenario.json> <manifest.json> <audit.jsonl>")
	fmt.Fprintln(stderr, "  tradex report <replay-result.json> <report.json> <report.md>")
	fmt.Fprintln(stderr, "  tradex report-shared <shared-result.json> <report.json> <report.md>")
	fmt.Fprintln(stderr, "  tradex robustness <replay-result.json> <config.json> <result.json>")
	fmt.Fprintln(stderr, "  tradex robustness-shared <shared-result.json> <config.json> <result.json>")
	fmt.Fprintln(stderr, "  tradex manifest-study <study.json> <manifest.json>")
	fmt.Fprintln(stderr, "  tradex manifest-study-shared <study.json> <manifest.json>")
	fmt.Fprintln(stderr, "  tradex walk-forward <study.json> <manifest.json> <audit-dir> <result.json>")
	fmt.Fprintln(stderr, "  tradex walk-forward-shared <study.json> <manifest.json> <audit-dir> <result.json>")
	fmt.Fprintln(stderr, "  tradex signals [config.json]")
	fmt.Fprintln(stderr, "  tradex price [symbol ...]")
	fmt.Fprintln(stderr, "  tradex watch [--self-test] <config.json>")
	fmt.Fprintln(stderr, "  tradex candles <symbol> <interval> <from> <to> <out.csv>")
	fmt.Fprintln(stderr, "  tradex outcomes <journal.jsonl> [journal2.jsonl ...]")
	return 2
}

func loadJSONStrict(path string, value any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("input contains trailing JSON")
	}
	return nil
}

type artifactPolicyDocument struct {
	AllowedKeyIDs               map[string]bool `json:"allowed_key_ids"`
	AllowedVersions             map[string]bool `json:"allowed_versions"`
	MaxArtifactAgeSeconds       int64           `json:"max_artifact_age_seconds"`
	MaxReconciliationAgeSeconds int64           `json:"max_reconciliation_age_seconds"`
	ExpectedAudience            string          `json:"expected_audience"`
	ExpectedPurpose             string          `json:"expected_purpose"`
	AllowedSchemaVersions       map[string]bool `json:"allowed_schema_versions"`
	RevokedArtifactHashes       map[string]bool `json:"revoked_artifact_hashes"`
}

func loadArtifactPolicy(path string) (ctrader.SignerPolicy, error) {
	var document artifactPolicyDocument
	if err := loadJSONStrict(path, &document); err != nil {
		return ctrader.SignerPolicy{}, err
	}
	return ctrader.SignerPolicy{AllowedKeyIDs: document.AllowedKeyIDs, AllowedVersions: document.AllowedVersions, MaxArtifactAge: time.Duration(document.MaxArtifactAgeSeconds) * time.Second, MaxReconciliationAge: time.Duration(document.MaxReconciliationAgeSeconds) * time.Second, ExpectedAudience: document.ExpectedAudience, ExpectedPurpose: document.ExpectedPurpose, AllowedSchemaVersions: document.AllowedSchemaVersions, RevokedArtifactHashes: document.RevokedArtifactHashes}, nil
}

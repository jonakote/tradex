package ctrader

import (
	"strings"
	"testing"
	"time"
)

func TestReadinessArtifactSignsAndVerifies(t *testing.T) {
	document := ReadinessDocument{Config: readinessConfig(), Reconciliation: Reconciliation{Match: true}, Evidence: OperationalEvidence{Healthy: true}, CircuitState: CircuitClosed}
	signer, err := NewHMACSHA256Signer([]byte("0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	artifact, err := BuildReadinessArtifact(document, strings.Repeat("a", 64), strings.Repeat("b", 64), at, at.Add(time.Minute), signer)
	if err != nil {
		t.Fatal(err)
	}
	if err := artifact.Verify(signer); err != nil {
		t.Fatal(err)
	}
	if err := artifact.VerifyWithPolicy(signer, SignerPolicy{AllowedKeyIDs: map[string]bool{"rotated-key": true}}); err == nil {
		t.Fatal("expected revoked key policy failure")
	}
	if err := artifact.VerifyWithPolicy(signer, SignerPolicy{AllowedKeyIDs: map[string]bool{"default": true}, AllowedVersions: map[string]bool{DefaultSignerVersion: true}}); err != nil {
		t.Fatal(err)
	}
	if err := artifact.VerifyWithPolicy(signer, SignerPolicy{ExpectedAudience: "other"}); err == nil {
		t.Fatal("expected audience restriction failure")
	}
	if err := artifact.VerifyWithPolicy(signer, SignerPolicy{AllowedSchemaVersions: map[string]bool{"2": true}}); err == nil {
		t.Fatal("expected schema version restriction failure")
	}
	if err := artifact.VerifyWithPolicy(signer, SignerPolicy{AllowedSchemaVersions: map[string]bool{CurrentArtifactSchemaVersion: true}}); err != nil {
		t.Fatal(err)
	}
	artifact.ManifestSHA256 = strings.Repeat("c", 64)
	if err := artifact.Verify(signer); err == nil {
		t.Fatal("expected tamper failure")
	}
	artifact, err = BuildReadinessArtifact(document, strings.Repeat("a", 64), strings.Repeat("b", 64), at, at.Add(time.Minute), signer)
	if err != nil {
		t.Fatal(err)
	}
	artifact.Scope.AccountID = "other"
	if err := artifact.Verify(signer); err == nil {
		t.Fatal("expected scope mismatch")
	}
}

func TestReadinessArtifactRejectsUnreadyDocument(t *testing.T) {
	document := ReadinessDocument{Config: readinessConfig(), Evidence: OperationalEvidence{Healthy: false}, CircuitState: CircuitClosed}
	signer, _ := NewHMACSHA256Signer([]byte("0123456789abcdef"))
	if _, err := BuildReadinessArtifact(document, strings.Repeat("a", 64), strings.Repeat("b", 64), time.Now(), time.Now(), signer); err == nil {
		t.Fatal("expected unready artifact rejection")
	}
}

func TestReadinessArtifactFreshnessPolicyRejectsStaleEvidence(t *testing.T) {
	document := ReadinessDocument{Config: readinessConfig(), Reconciliation: Reconciliation{Match: true}, Evidence: OperationalEvidence{Healthy: true}, CircuitState: CircuitClosed}
	signer, _ := NewHMACSHA256Signer([]byte("0123456789abcdef"))
	created := time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)
	artifact, err := BuildReadinessArtifact(document, strings.Repeat("a", 64), strings.Repeat("b", 64), created, created, signer)
	if err != nil {
		t.Fatal(err)
	}
	policy := SignerPolicy{MaxArtifactAge: time.Hour, MaxReconciliationAge: 30 * time.Minute}
	if err := artifact.VerifyWithPolicyAt(signer, policy, created.Add(2*time.Hour)); err == nil {
		t.Fatal("expected stale artifact rejection")
	}
	if err := artifact.VerifyWithPolicyAt(signer, policy, created.Add(20*time.Minute)); err != nil {
		t.Fatal(err)
	}
}

func TestReadinessArtifactRevocation(t *testing.T) {
	document := ReadinessDocument{Config: readinessConfig(), Reconciliation: Reconciliation{Match: true}, Evidence: OperationalEvidence{Healthy: true}, CircuitState: CircuitClosed}
	signer, _ := NewHMACSHA256Signer([]byte("0123456789abcdef"))
	at := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	artifact, err := BuildReadinessArtifact(document, strings.Repeat("a", 64), strings.Repeat("b", 64), at, at, signer)
	if err != nil {
		t.Fatal(err)
	}
	revocations := NewRevocationList()
	if err := revocations.Revoke(artifact.ArtifactSHA256, "superseded", at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	entry, ok := revocations.Lookup(artifact.ArtifactSHA256)
	if !ok || entry.Reason != "superseded" {
		t.Fatalf("entry=%#v ok=%v", entry, ok)
	}
	if err := artifact.VerifyWithPolicy(signer, SignerPolicy{RevokedArtifactHashes: map[string]bool{artifact.ArtifactSHA256: true}}); err == nil {
		t.Fatal("expected revocation failure")
	}
}

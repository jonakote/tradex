package ctrader

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

type ArtifactSigner interface {
	Sign([]byte) ([]byte, error)
	Verify([]byte, []byte) bool
}

type HMACSHA256Signer struct{ key []byte }

const DefaultSignerVersion = "hmac-sha256-v1"
const DefaultArtifactAudience = "tradex-deployment"
const DefaultArtifactPurpose = "ctrader-readiness"
const CurrentArtifactSchemaVersion = "1"

func NewHMACSHA256Signer(key []byte) (*HMACSHA256Signer, error) {
	if len(key) < 16 {
		return nil, errors.New("readiness artifact signing key must be at least 16 bytes")
	}
	return &HMACSHA256Signer{key: append([]byte(nil), key...)}, nil
}

func (s *HMACSHA256Signer) Sign(payload []byte) ([]byte, error) {
	mac := hmac.New(sha256.New, s.key)
	_, _ = mac.Write(payload)
	return mac.Sum(nil), nil
}

func (s *HMACSHA256Signer) Verify(payload, signature []byte) bool {
	expected, _ := s.Sign(payload)
	return hmac.Equal(expected, signature)
}

type ReadinessArtifact struct {
	Document         ReadinessDocument  `json:"document"`
	ManifestSHA256   string             `json:"manifest_sha256"`
	EvidenceSHA256   string             `json:"evidence_sha256"`
	ReconciliationAt time.Time          `json:"reconciliation_at"`
	Approval         *PromotionApproval `json:"approval,omitempty"`
	CreatedAt        time.Time          `json:"created_at"`
	ArtifactSHA256   string             `json:"artifact_sha256"`
	Signature        string             `json:"signature"`
	KeyID            string             `json:"key_id"`
	SignerVersion    string             `json:"signer_version"`
	Scope            ArtifactScope      `json:"scope"`
	Audience         string             `json:"audience"`
	Purpose          string             `json:"purpose"`
	SchemaVersion    string             `json:"schema_version"`
}

type artifactMaterial struct {
	Document         ReadinessDocument  `json:"document"`
	ManifestSHA256   string             `json:"manifest_sha256"`
	EvidenceSHA256   string             `json:"evidence_sha256"`
	ReconciliationAt time.Time          `json:"reconciliation_at"`
	Approval         *PromotionApproval `json:"approval,omitempty"`
	CreatedAt        time.Time          `json:"created_at"`
	KeyID            string             `json:"key_id"`
	SignerVersion    string             `json:"signer_version"`
	Scope            ArtifactScope      `json:"scope"`
	Audience         string             `json:"audience"`
	Purpose          string             `json:"purpose"`
	SchemaVersion    string             `json:"schema_version"`
}

type ArtifactScope struct {
	BrokerName     string `json:"broker_name"`
	AccountID      string `json:"account_id"`
	Environment    string `json:"environment"`
	ManifestSHA256 string `json:"manifest_sha256"`
}

type SignerPolicy struct {
	AllowedKeyIDs         map[string]bool
	AllowedVersions       map[string]bool
	MaxArtifactAge        time.Duration
	MaxReconciliationAge  time.Duration
	ExpectedAudience      string
	ExpectedPurpose       string
	AllowedSchemaVersions map[string]bool
	RevokedArtifactHashes map[string]bool
}

type ArtifactRevocation struct {
	ArtifactSHA256 string    `json:"artifact_sha256"`
	RevokedAt      time.Time `json:"revoked_at"`
	Reason         string    `json:"reason"`
}

type RevocationList struct {
	mu      sync.RWMutex
	entries map[string]ArtifactRevocation
}

func NewRevocationList() *RevocationList {
	return &RevocationList{entries: make(map[string]ArtifactRevocation)}
}

func (r *RevocationList) Revoke(artifactHash, reason string, at time.Time) error {
	if !validHash(artifactHash) || reason == "" || at.IsZero() {
		return errors.New("invalid artifact revocation")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.entries == nil {
		r.entries = make(map[string]ArtifactRevocation)
	}
	if existing, ok := r.entries[artifactHash]; ok {
		if existing.RevokedAt.Equal(at.UTC()) && existing.Reason == reason {
			return nil
		}
		if at.Before(existing.RevokedAt) {
			return errors.New("stale artifact revocation event")
		}
		if at.Equal(existing.RevokedAt) {
			return errors.New("conflicting artifact revocation event")
		}
	}
	r.entries[artifactHash] = ArtifactRevocation{ArtifactSHA256: artifactHash, RevokedAt: at.UTC(), Reason: reason}
	return nil
}

func (r *RevocationList) Lookup(artifactHash string) (ArtifactRevocation, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.entries[artifactHash]
	return entry, ok
}

func (r *RevocationList) Entries() []ArtifactRevocation {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entries := make([]ArtifactRevocation, 0, len(r.entries))
	for _, entry := range r.entries {
		entries = append(entries, entry)
	}
	return entries
}

func BuildReadinessArtifact(document ReadinessDocument, manifestHash, evidenceHash string, reconciliationAt, createdAt time.Time, signer ArtifactSigner) (ReadinessArtifact, error) {
	return BuildReadinessArtifactWithMetadata(document, manifestHash, evidenceHash, reconciliationAt, createdAt, "default", DefaultSignerVersion, signer)
}

func BuildReadinessArtifactWithMetadata(document ReadinessDocument, manifestHash, evidenceHash string, reconciliationAt, createdAt time.Time, keyID, signerVersion string, signer ArtifactSigner) (ReadinessArtifact, error) {
	if !CheckReadinessDocument(document).Ready {
		return ReadinessArtifact{}, errors.New("cannot build readiness artifact from unready document")
	}
	if !validHash(manifestHash) || !validHash(evidenceHash) || reconciliationAt.IsZero() || createdAt.IsZero() || keyID == "" || signerVersion == "" || signer == nil {
		return ReadinessArtifact{}, errors.New("invalid readiness artifact identity, times, or signer")
	}
	scope := ArtifactScope{BrokerName: document.Config.Name, AccountID: document.Config.AccountID, Environment: document.Config.Environment, ManifestSHA256: manifestHash}
	material := artifactMaterial{Document: document, ManifestSHA256: manifestHash, EvidenceSHA256: evidenceHash, ReconciliationAt: reconciliationAt.UTC(), Approval: document.Approval, CreatedAt: createdAt.UTC(), KeyID: keyID, SignerVersion: signerVersion, Scope: scope, Audience: DefaultArtifactAudience, Purpose: DefaultArtifactPurpose, SchemaVersion: CurrentArtifactSchemaVersion}
	encoded, err := json.Marshal(material)
	if err != nil {
		return ReadinessArtifact{}, err
	}
	digest := sha256.Sum256(encoded)
	signature, err := signer.Sign(encoded)
	if err != nil {
		return ReadinessArtifact{}, fmt.Errorf("sign readiness artifact: %w", err)
	}
	return ReadinessArtifact{Document: document, ManifestSHA256: manifestHash, EvidenceSHA256: evidenceHash, ReconciliationAt: reconciliationAt.UTC(), Approval: document.Approval, CreatedAt: createdAt.UTC(), ArtifactSHA256: hex.EncodeToString(digest[:]), Signature: hex.EncodeToString(signature), KeyID: keyID, SignerVersion: signerVersion, Scope: scope, Audience: DefaultArtifactAudience, Purpose: DefaultArtifactPurpose, SchemaVersion: CurrentArtifactSchemaVersion}, nil
}

func (a ReadinessArtifact) Verify(signer ArtifactSigner) error {
	return a.VerifyWithPolicy(signer, SignerPolicy{})
}

func (a ReadinessArtifact) VerifyWithPolicy(signer ArtifactSigner, policy SignerPolicy) error {
	return a.VerifyWithPolicyAt(signer, policy, time.Time{})
}

func (a ReadinessArtifact) VerifyWithPolicyAt(signer ArtifactSigner, policy SignerPolicy, now time.Time) error {
	if signer == nil || !validHash(a.ManifestSHA256) || !validHash(a.EvidenceSHA256) || a.ReconciliationAt.IsZero() || a.CreatedAt.IsZero() || a.Signature == "" {
		return errors.New("invalid readiness artifact")
	}
	if a.KeyID == "" || a.SignerVersion == "" || (len(policy.AllowedKeyIDs) > 0 && !policy.AllowedKeyIDs[a.KeyID]) || (len(policy.AllowedVersions) > 0 && !policy.AllowedVersions[a.SignerVersion]) {
		return errors.New("readiness artifact signer is not allowed")
	}
	if a.Audience == "" || a.Purpose == "" || (policy.ExpectedAudience != "" && a.Audience != policy.ExpectedAudience) || (policy.ExpectedPurpose != "" && a.Purpose != policy.ExpectedPurpose) {
		return errors.New("readiness artifact audience or purpose is not allowed")
	}
	if a.SchemaVersion == "" || (len(policy.AllowedSchemaVersions) > 0 && !policy.AllowedSchemaVersions[a.SchemaVersion]) {
		return errors.New("readiness artifact schema version is not allowed")
	}
	expectedScope := ArtifactScope{BrokerName: a.Document.Config.Name, AccountID: a.Document.Config.AccountID, Environment: a.Document.Config.Environment, ManifestSHA256: a.ManifestSHA256}
	if a.Scope != expectedScope {
		return errors.New("readiness artifact scope mismatch")
	}
	material := artifactMaterial{Document: a.Document, ManifestSHA256: a.ManifestSHA256, EvidenceSHA256: a.EvidenceSHA256, ReconciliationAt: a.ReconciliationAt.UTC(), Approval: a.Approval, CreatedAt: a.CreatedAt.UTC(), KeyID: a.KeyID, SignerVersion: a.SignerVersion, Scope: a.Scope, Audience: a.Audience, Purpose: a.Purpose, SchemaVersion: a.SchemaVersion}
	encoded, err := json.Marshal(material)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(encoded)
	if a.ArtifactSHA256 != hex.EncodeToString(digest[:]) {
		return errors.New("readiness artifact hash mismatch")
	}
	if policy.RevokedArtifactHashes[a.ArtifactSHA256] {
		return errors.New("readiness artifact is revoked")
	}
	signature, err := hex.DecodeString(strings.TrimSpace(a.Signature))
	if err != nil || !signer.Verify(encoded, signature) {
		return errors.New("readiness artifact signature mismatch")
	}
	if !CheckReadinessDocument(a.Document).Ready {
		return errors.New("readiness artifact document is no longer ready")
	}
	if now.IsZero() && (policy.MaxArtifactAge > 0 || policy.MaxReconciliationAge > 0) {
		return errors.New("freshness policy requires evaluation time")
	}
	if !now.IsZero() {
		if now.Before(a.CreatedAt) || now.Before(a.ReconciliationAt) {
			return errors.New("readiness artifact timestamp is in the future")
		}
		if policy.MaxArtifactAge > 0 && now.Sub(a.CreatedAt) > policy.MaxArtifactAge {
			return errors.New("readiness artifact is expired")
		}
		if policy.MaxReconciliationAge > 0 && now.Sub(a.ReconciliationAt) > policy.MaxReconciliationAge {
			return errors.New("readiness reconciliation is stale")
		}
	}
	return nil
}

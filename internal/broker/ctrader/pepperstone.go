package ctrader

import (
	"errors"
	"strings"

	"tradex/internal/config"
)

// PepperstoneDemoConfig is the broker-specific, demo-only configuration seam.
// Secret material is referenced, never embedded.
type PepperstoneDemoConfig struct {
	BrokerName         string            `json:"broker_name"`
	AccountID          string            `json:"account_id"`
	Endpoint           string            `json:"endpoint"`
	Serialization      Serialization     `json:"serialization"`
	OAuthClientID      string            `json:"oauth_client_id"`
	OAuthCredentialRef string            `json:"oauth_credential_ref"`
	AccessTokenRef     string            `json:"access_token_ref"`
	Mode               config.BrokerMode `json:"mode"`
}

func (c PepperstoneDemoConfig) Validate() error {
	if c.BrokerName != "Pepperstone" || c.AccountID == "" || c.Endpoint == "" || c.OAuthClientID == "" || c.OAuthCredentialRef == "" || c.AccessTokenRef == "" {
		return errors.New("invalid Pepperstone cTrader demo identity or credential references")
	}
	if !strings.Contains(strings.ToLower(c.Endpoint), "demo") {
		return errors.New("Pepperstone cTrader endpoint must be a demo endpoint")
	}
	if c.Serialization != SerializationJSON && c.Serialization != SerializationProtobuf {
		return errors.New("Pepperstone cTrader serialization must be JSON or PROTOBUF")
	}
	if c.Mode != config.BrokerShadow && c.Mode != config.BrokerActiveDemo {
		return errors.New("Pepperstone cTrader connector only permits shadow or active demo mode")
	}
	return nil
}

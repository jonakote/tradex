package ctrader

import (
	"strings"
	"testing"

	"tradex/internal/config"
)

func TestPepperstoneDemoConfigValidation(t *testing.T) {
	valid := PepperstoneDemoConfig{BrokerName: "Pepperstone", AccountID: "acct", Endpoint: "demo.ctraderapi.com:5036", Serialization: SerializationJSON, OAuthClientID: "client", OAuthCredentialRef: "secret-ref", AccessTokenRef: "token-ref", Mode: config.BrokerActiveDemo}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	invalid := valid
	invalid.Endpoint = strings.ReplaceAll(valid.Endpoint, "demo", "live")
	if err := invalid.Validate(); err == nil {
		t.Fatal("expected live endpoint rejection")
	}
	invalid = valid
	invalid.Mode = config.BrokerActiveLive
	if err := invalid.Validate(); err == nil {
		t.Fatal("expected live mode rejection")
	}
}

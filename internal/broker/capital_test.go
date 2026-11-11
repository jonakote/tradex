package broker

import "testing"

// TestNewClientRefusesNonDemoURL is the single most important test in this
// package: this is the only thing standing between a bug elsewhere and a
// real-money order. It must never be weakened or removed without a
// deliberate, separate decision to support live trading.
func TestNewClientRefusesNonDemoURL(t *testing.T) {
	cases := []struct {
		name    string
		baseURL string
		wantErr bool
	}{
		{"live api", "https://api-capital.backend-capital.com", true},
		{"demo api", "https://demo-api-capital.backend-capital.com", false},
		{"live, odd casing", "https://API-CAPITAL.backend-capital.com", true},
		{"demo, odd casing", "https://DEMO-api-capital.backend-capital.com", false},
		{"empty", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewClient(Config{APIKey: "k", Identifier: "i", Password: "p", BaseURL: tc.baseURL})
			if tc.wantErr && err == nil {
				t.Fatalf("NewClient(%q): expected a refusal, got none — this must fail closed", tc.baseURL)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("NewClient(%q): expected success, got %v", tc.baseURL, err)
			}
		})
	}
}

func TestNewClientRequiresAllCredentials(t *testing.T) {
	_, err := NewClient(Config{BaseURL: "https://demo-api-capital.backend-capital.com"})
	if err == nil {
		t.Fatal("expected an error with missing credentials")
	}
}

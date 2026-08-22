package ctrader

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type oauthRoundTripper struct {
	status int
	body   string
	method string
	query  string
}

func (r *oauthRoundTripper) Do(request *http.Request) (*http.Response, error) {
	r.method, r.query = request.Method, request.URL.RawQuery
	return &http.Response{StatusCode: r.status, Body: io.NopCloser(strings.NewReader(r.body)), Header: make(http.Header)}, nil
}

func testOAuthConfig() OAuthConfig {
	return OAuthConfig{ClientID: "client", ClientSecret: "secret", RedirectURI: "http://localhost/callback", TokenURL: "https://example.test/token", AuthorizeURL: "https://example.test/authorize"}
}

func TestOAuthAuthorizationURLAndExchange(t *testing.T) {
	config := testOAuthConfig()
	redirect, err := config.AuthorizationURL("state-1", "accounts")
	if err != nil || !strings.Contains(redirect, "client_id=client") || !strings.Contains(redirect, "state=state-1") {
		t.Fatalf("redirect=%s err=%v", redirect, err)
	}
	transport := &oauthRoundTripper{status: http.StatusOK, body: `{"accessToken":"access","refreshToken":"refresh","tokenType":"bearer","expiresIn":3600}`}
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	client, err := NewOAuthClient(config, transport, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	token, err := client.ExchangeAuthorizationCode(context.Background(), "code")
	if err != nil || token.AccessToken != "access" || !token.ExpiresAt.Equal(now.Add(time.Hour)) || transport.method != http.MethodGet {
		t.Fatalf("token=%#v method=%s err=%v", token, transport.method, err)
	}
}

func TestOAuthRefreshUsesPOSTAndDoesNotExposeSecretsInErrors(t *testing.T) {
	transport := &oauthRoundTripper{status: http.StatusUnauthorized, body: `{"errorCode":"invalid_grant","description":"denied"}`}
	client, err := NewOAuthClient(testOAuthConfig(), transport, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.RefreshToken(context.Background(), "refresh-secret")
	if err == nil || transport.method != http.MethodPost || strings.Contains(err.Error(), "refresh-secret") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("err=%v method=%s", err, transport.method)
	}
}

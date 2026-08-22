package ctrader

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const defaultOAuthEndpoint = "https://openapi.ctrader.com/apps/token"
const defaultAuthorizationEndpoint = "https://id.ctrader.com/my/settings/openapi/grantingaccess/"

type OAuthConfig struct {
	ClientID     string
	ClientSecret string
	RedirectURI  string
	TokenURL     string
	AuthorizeURL string
}

func OAuthConfigFromEnv() (OAuthConfig, error) {
	config := OAuthConfig{ClientID: os.Getenv("CTRADER_CLIENT_ID"), ClientSecret: os.Getenv("CTRADER_CLIENT_SECRET"), RedirectURI: os.Getenv("CTRADER_REDIRECT_URI"), TokenURL: os.Getenv("CTRADER_TOKEN_URL"), AuthorizeURL: os.Getenv("CTRADER_AUTHORIZE_URL")}
	if config.TokenURL == "" {
		config.TokenURL = defaultOAuthEndpoint
	}
	if config.AuthorizeURL == "" {
		config.AuthorizeURL = defaultAuthorizationEndpoint
	}
	if err := config.Validate(); err != nil {
		return OAuthConfig{}, err
	}
	return config, nil
}

func (c OAuthConfig) Validate() error {
	if c.ClientID == "" || c.ClientSecret == "" || c.RedirectURI == "" {
		return errors.New("cTrader OAuth client ID, secret, and redirect URI are required")
	}
	if _, err := url.ParseRequestURI(c.RedirectURI); err != nil {
		return errors.New("cTrader OAuth redirect URI is invalid")
	}
	if _, err := url.ParseRequestURI(c.TokenURL); err != nil {
		return errors.New("cTrader OAuth token URL is invalid")
	}
	if _, err := url.ParseRequestURI(c.AuthorizeURL); err != nil {
		return errors.New("cTrader OAuth authorization URL is invalid")
	}
	return nil
}

func (c OAuthConfig) AuthorizationURL(state, scope string) (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	if state == "" || scope == "" {
		return "", errors.New("cTrader OAuth state and scope are required")
	}
	query := url.Values{"client_id": {c.ClientID}, "redirect_uri": {c.RedirectURI}, "scope": {scope}, "product": {"web"}, "state": {state}}
	return c.AuthorizeURL + "?" + query.Encode(), nil
}

type OAuthHTTP interface {
	Do(*http.Request) (*http.Response, error)
}

type OAuthClient struct {
	config OAuthConfig
	http   OAuthHTTP
	clock  func() time.Time
}

func NewOAuthClient(config OAuthConfig, client OAuthHTTP, clock func() time.Time) (*OAuthClient, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if client == nil {
		return nil, errors.New("cTrader OAuth HTTP client is required")
	}
	if clock == nil {
		clock = time.Now
	}
	return &OAuthClient{config: config, http: client, clock: clock}, nil
}

func (c *OAuthClient) ExchangeAuthorizationCode(ctx context.Context, code string) (OAuthToken, error) {
	if code == "" {
		return OAuthToken{}, errors.New("cTrader authorization code is required")
	}
	values := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {c.config.RedirectURI}, "client_id": {c.config.ClientID}, "client_secret": {c.config.ClientSecret}}
	return c.exchange(ctx, http.MethodGet, values)
}

func (c *OAuthClient) RefreshToken(ctx context.Context, refreshToken string) (OAuthToken, error) {
	if refreshToken == "" {
		return OAuthToken{}, errors.New("cTrader refresh token is required")
	}
	values := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refreshToken}, "client_id": {c.config.ClientID}, "client_secret": {c.config.ClientSecret}}
	return c.exchange(ctx, http.MethodPost, values)
}

func (c *OAuthClient) exchange(ctx context.Context, method string, values url.Values) (OAuthToken, error) {
	if ctx == nil {
		return OAuthToken{}, errors.New("cTrader OAuth context is required")
	}
	var request *http.Request
	var err error
	if method == http.MethodGet {
		target, _ := url.Parse(c.config.TokenURL)
		target.RawQuery = values.Encode()
		request, err = http.NewRequestWithContext(ctx, method, target.String(), nil)
	} else {
		request, err = http.NewRequestWithContext(ctx, method, c.config.TokenURL, strings.NewReader(values.Encode()))
		if request != nil {
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
	}
	if err != nil {
		return OAuthToken{}, fmt.Errorf("build cTrader OAuth request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return OAuthToken{}, fmt.Errorf("cTrader OAuth request: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return OAuthToken{}, fmt.Errorf("read cTrader OAuth response: %w", err)
	}
	var payload struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		TokenType    string `json:"tokenType"`
		ExpiresIn    int64  `json:"expiresIn"`
		ErrorCode    string `json:"errorCode"`
		Description  string `json:"description"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return OAuthToken{}, fmt.Errorf("decode cTrader OAuth response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || payload.ErrorCode != "" {
		return OAuthToken{}, fmt.Errorf("cTrader OAuth exchange failed: %s", oauthError(payload.ErrorCode, payload.Description))
	}
	if payload.ExpiresIn <= 0 {
		return OAuthToken{}, errors.New("cTrader OAuth response has invalid expiry")
	}
	token := OAuthToken{AccessToken: payload.AccessToken, RefreshToken: payload.RefreshToken, TokenType: strings.Title(strings.ToLower(payload.TokenType)), ExpiresAt: c.clock().UTC().Add(time.Duration(payload.ExpiresIn) * time.Second)}
	if err := token.Validate(); err != nil {
		return OAuthToken{}, fmt.Errorf("validate cTrader OAuth token: %w", err)
	}
	return token, nil
}

func oauthError(code, description string) string {
	if description == "" {
		return "code " + strconv.Quote(code)
	}
	return "code " + strconv.Quote(code) + ": " + description
}

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
	"path/filepath"
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
	if err := config.validateTokenClient(); err != nil {
		return OAuthConfig{}, err
	}
	return config, nil
}

func (c OAuthConfig) Validate() error {
	if err := c.validateTokenClient(); err != nil {
		return err
	}
	if c.RedirectURI == "" {
		return errors.New("cTrader OAuth redirect URI is required for authorization-code flow")
	}
	if _, err := url.ParseRequestURI(c.RedirectURI); err != nil {
		return errors.New("cTrader OAuth redirect URI is invalid")
	}
	return nil
}

func (c OAuthConfig) validateTokenClient() error {
	if c.ClientID == "" || c.ClientSecret == "" {
		return errors.New("cTrader OAuth client ID and secret are required")
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
	if err := config.validateTokenClient(); err != nil {
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
	if err := c.config.Validate(); err != nil {
		return OAuthToken{}, err
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

// Refresh implements TokenRefresher so an OAuthClient can be composed with
// TokenManager without exposing refresh-token material to callers.
func (c *OAuthClient) Refresh(ctx context.Context, token OAuthToken) (OAuthToken, error) {
	if err := token.Validate(); err != nil {
		return OAuthToken{}, err
	}
	return c.RefreshToken(ctx, token.RefreshToken)
}

// ReadOAuthTokenFile loads a token produced by ctrader-oauth-exchange. The
// file is bounded and strictly decoded so malformed or unexpected credential
// material is rejected before a socket session is attempted.
func ReadOAuthTokenFile(path string) (OAuthToken, error) {
	if path == "" {
		return OAuthToken{}, errors.New("cTrader OAuth token file path is required")
	}
	file, err := os.Open(path)
	if err != nil {
		return OAuthToken{}, fmt.Errorf("open cTrader OAuth token file: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.DisallowUnknownFields()
	var token OAuthToken
	if err := decoder.Decode(&token); err != nil {
		return OAuthToken{}, fmt.Errorf("decode cTrader OAuth token file: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return OAuthToken{}, errors.New("decode cTrader OAuth token file: trailing JSON")
		}
		return OAuthToken{}, fmt.Errorf("decode cTrader OAuth token file: %w", err)
	}
	if err := token.Validate(); err != nil {
		return OAuthToken{}, fmt.Errorf("validate cTrader OAuth token file: %w", err)
	}
	return token, nil
}

// WriteOAuthTokenFile atomically replaces a token file with mode 0600. It is
// intended for refresh-token rotation and never prints token contents.
func WriteOAuthTokenFile(path string, token OAuthToken) error {
	if path == "" {
		return errors.New("cTrader OAuth token file path is required")
	}
	if err := token.Validate(); err != nil {
		return fmt.Errorf("validate cTrader OAuth token for storage: %w", err)
	}
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".ctrader-token-*")
	if err != nil {
		return fmt.Errorf("create temporary cTrader OAuth token file: %w", err)
	}
	temporaryName := temporary.Name()
	cleanup := func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryName)
	}
	if err := temporary.Chmod(0o600); err != nil {
		cleanup()
		return fmt.Errorf("protect temporary cTrader OAuth token file: %w", err)
	}
	encoded, err := json.MarshalIndent(token, "", "  ")
	if err != nil {
		cleanup()
		return fmt.Errorf("encode cTrader OAuth token file: %w", err)
	}
	encoded = append(encoded, '\n')
	if _, err := temporary.Write(encoded); err != nil {
		cleanup()
		return fmt.Errorf("write temporary cTrader OAuth token file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("sync temporary cTrader OAuth token file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryName)
		return fmt.Errorf("close temporary cTrader OAuth token file: %w", err)
	}
	if err := os.Rename(temporaryName, path); err != nil {
		_ = os.Remove(temporaryName)
		return fmt.Errorf("replace cTrader OAuth token file: %w", err)
	}
	return nil
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

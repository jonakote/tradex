package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"tradex/internal/broker/ctrader"
)

const cTraderOAuthBootstrapTimeout = 5 * time.Minute

// runCTraderOAuthBootstrap performs the one-time interactive OAuth step for a
// terminal bot. It listens on the already-registered loopback redirect URI,
// prints the authorization URL, exchanges the callback code, and writes a
// protected token file. The running bot never invokes this flow.
func runCTraderOAuthBootstrap(tokenPath string, stdout, stderr io.Writer) int {
	if tokenPath == "" {
		fmt.Fprintln(stderr, "tradex: cTrader token file path is required")
		return 1
	}
	if _, err := os.Stat(tokenPath); err == nil {
		fmt.Fprintln(stderr, "tradex: cTrader token file already exists; refusing to overwrite")
		return 1
	} else if !os.IsNotExist(err) {
		fmt.Fprintf(stderr, "tradex: inspect cTrader token file: %v\n", err)
		return 1
	}
	config, err := ctrader.OAuthConfigFromEnv()
	if err != nil {
		fmt.Fprintf(stderr, "tradex: load cTrader OAuth configuration: %v\n", err)
		return 1
	}
	redirect, err := url.Parse(config.RedirectURI)
	if err != nil || redirect.Scheme != "http" || redirect.Host == "" || redirect.Path == "" {
		fmt.Fprintln(stderr, "tradex: cTrader bootstrap requires an http loopback redirect URI with a path")
		return 1
	}
	if !isLoopbackHost(redirect.Hostname()) {
		fmt.Fprintln(stderr, "tradex: cTrader bootstrap refuses non-loopback redirect URI")
		return 1
	}
	if redirect.Port() == "" {
		fmt.Fprintln(stderr, "tradex: cTrader bootstrap redirect URI must include a port")
		return 1
	}
	state, err := randomOAuthState()
	if err != nil {
		fmt.Fprintf(stderr, "tradex: create cTrader OAuth state: %v\n", err)
		return 1
	}
	authorizationURL, err := config.AuthorizationURL(state, "accounts")
	if err != nil {
		fmt.Fprintf(stderr, "tradex: build cTrader authorization URL: %v\n", err)
		return 1
	}
	listener, err := net.Listen("tcp", redirect.Host)
	if err != nil {
		fmt.Fprintf(stderr, "tradex: listen for cTrader OAuth callback on %s: %v\n", redirect.Host, err)
		return 1
	}
	defer listener.Close()
	codeChannel := make(chan string, 1)
	callbackServer := &http.Server{Handler: http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != redirect.Path {
			http.NotFound(response, request)
			return
		}
		if request.URL.Query().Get("state") != state {
			http.Error(response, "invalid OAuth state", http.StatusBadRequest)
			return
		}
		code := request.URL.Query().Get("code")
		if code == "" {
			http.Error(response, "missing OAuth code", http.StatusBadRequest)
			return
		}
		select {
		case codeChannel <- code:
		default:
		}
		response.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(response, "TradeX authorization received. You may close this browser window.\n")
	})}
	serverDone := make(chan error, 1)
	go func() { serverDone <- callbackServer.Serve(listener) }()
	defer func() {
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = callbackServer.Shutdown(shutdownContext)
		select {
		case <-serverDone:
		case <-shutdownContext.Done():
		}
	}()

	fmt.Fprintln(stdout, "Open this URL once in an authenticated browser:")
	fmt.Fprintln(stdout, authorizationURL)
	fmt.Fprintf(stdout, "Waiting for callback on %s (scope: accounts, timeout: %s) ...\n", config.RedirectURI, cTraderOAuthBootstrapTimeout)
	var code string
	select {
	case code = <-codeChannel:
	case <-time.After(cTraderOAuthBootstrapTimeout):
		fmt.Fprintln(stderr, "tradex: cTrader OAuth bootstrap timed out")
		return 1
	case serverError := <-serverDone:
		if serverError != nil && !errors.Is(serverError, http.ErrServerClosed) {
			fmt.Fprintf(stderr, "tradex: cTrader OAuth callback server: %v\n", serverError)
		}
		return 1
	}
	oauthClient, err := ctrader.NewOAuthClient(config, http.DefaultClient, time.Now)
	if err != nil {
		fmt.Fprintf(stderr, "tradex: create cTrader OAuth client: %v\n", err)
		return 1
	}
	token, err := oauthClient.ExchangeAuthorizationCode(context.Background(), code)
	if err != nil {
		fmt.Fprintf(stderr, "tradex: exchange cTrader authorization code: %v\n", err)
		return 1
	}
	if err := ctrader.WriteOAuthTokenFile(tokenPath, token); err != nil {
		fmt.Fprintf(stderr, "tradex: write cTrader OAuth token: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "cTrader OAuth token stored with mode 0600")
	return 0
}

func randomOAuthState() (string, error) {
	bytes := make([]byte, 24)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func isLoopbackHost(host string) bool {
	host = strings.Trim(host, "[]")
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

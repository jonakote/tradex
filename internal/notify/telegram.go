package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Telegram delivers a signal as a bot message.
//
// The token is a credential: it is held in the struct, sent only in the
// request path, and never placed in an error message. Telegram echoes the
// request URL in some error bodies, so the body is scrubbed of the token
// before it is surfaced.
type Telegram struct {
	Token  string
	ChatID string
	// BaseURL allows a test server to stand in for api.telegram.org.
	BaseURL string
	Timeout time.Duration
	Client  *http.Client
}

func (t Telegram) Name() string { return "telegram" }

// Configured reports whether both credentials are present, so the runner can
// skip the sink cleanly instead of failing on every signal.
func (t Telegram) Configured() bool {
	return strings.TrimSpace(t.Token) != "" && strings.TrimSpace(t.ChatID) != ""
}

func (t Telegram) baseURL() string {
	if strings.TrimSpace(t.BaseURL) != "" {
		return strings.TrimRight(t.BaseURL, "/")
	}
	return "https://api.telegram.org"
}

func (t Telegram) client() *http.Client {
	if t.Client != nil {
		return t.Client
	}
	timeout := t.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	return &http.Client{Timeout: timeout}
}

func (t Telegram) Send(ctx context.Context, signal Signal) error {
	if !t.Configured() {
		return errors.New("notify: telegram token and chat id are required")
	}

	// Plain text, no parse_mode: a price string cannot then be mangled by
	// Markdown escaping rules, and no message content is ever interpreted.
	body, err := json.Marshal(map[string]any{
		"chat_id":                  t.ChatID,
		"text":                     signal.Subject() + "\n\n" + signal.Text(),
		"disable_web_page_preview": true,
	})
	if err != nil {
		return err
	}

	endpoint := t.baseURL() + "/bot" + t.Token + "/sendMessage"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return t.scrub(err)
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := t.client().Do(request)
	if err != nil {
		return t.scrub(err)
	}
	defer response.Body.Close()
	payload, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("notify: telegram status %d: %s",
			response.StatusCode, t.scrubText(describeTelegramError(payload)))
	}
	return nil
}

// scrub removes the bot token from an error that may contain the request URL.
func (t Telegram) scrub(err error) error {
	if err == nil {
		return nil
	}
	return errors.New(t.scrubText(err.Error()))
}

func (t Telegram) scrubText(text string) string {
	if t.Token == "" {
		return text
	}
	return strings.ReplaceAll(text, t.Token, "<redacted>")
}

func describeTelegramError(payload []byte) string {
	var body struct {
		Description string `json:"description"`
	}
	if err := json.Unmarshal(payload, &body); err == nil && body.Description != "" {
		return body.Description
	}
	text := strings.TrimSpace(string(payload))
	if len(text) > 200 {
		text = text[:200]
	}
	return text
}

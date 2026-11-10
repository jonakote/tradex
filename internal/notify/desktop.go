package notify

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"time"
)

// Desktop raises a desktop notification through notify-send.
//
// It is intentionally a process call rather than a D-Bus client: the binary
// is present on this host, the contract is stable, and a notification daemon
// that is not running should degrade to a reported sink failure, not a
// blocked runner.
type Desktop struct {
	// Binary defaults to notify-send.
	Binary string
	// Timeout bounds the call; a hung notification daemon must not stall
	// signal delivery.
	Timeout time.Duration
	// Urgency is passed through: low, normal or critical.
	Urgency string
}

func (d Desktop) Name() string { return "desktop" }

// Available reports whether the notification binary exists, so the runner can
// say so at startup instead of failing on the first signal.
func (d Desktop) Available() bool {
	_, err := exec.LookPath(d.binary())
	return err == nil
}

func (d Desktop) binary() string {
	if strings.TrimSpace(d.Binary) != "" {
		return d.Binary
	}
	return "notify-send"
}

func (d Desktop) Send(ctx context.Context, signal Signal) error {
	binary, err := exec.LookPath(d.binary())
	if err != nil {
		return errors.New("notify: " + d.binary() + " is not installed")
	}
	timeout := d.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	urgency := d.Urgency
	if urgency == "" {
		urgency = "normal"
	}
	// The body is passed as a single argument; no shell is involved, so
	// nothing in the message can be interpreted as a command.
	command := exec.CommandContext(ctx, binary,
		"--app-name=tradex",
		"--urgency="+urgency,
		signal.Subject(),
		signal.Text(),
	)
	output, err := command.CombinedOutput()
	if err != nil {
		return errors.New("notify: notify-send failed: " + strings.TrimSpace(string(output)))
	}
	return nil
}

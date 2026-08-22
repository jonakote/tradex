package ctrader

import "testing"

func TestClassifyRetryRequiresExplicitProtocolFlag(t *testing.T) {
	if got := ClassifyRetry(ProtocolError{Code: 1, Description: "rejected"}); got != RetryNever {
		t.Fatalf("got %s", got)
	}
	if got := ClassifyRetry(&ProtocolError{Code: 2, Description: "temporary", Retriable: true}); got != RetryAfterReconnect {
		t.Fatalf("got %s", got)
	}
	if IsRetryable(nil) || IsRetryable(assertionError{}) {
		t.Fatal("unexpected retryable error")
	}
}

type assertionError struct{}

func (assertionError) Error() string { return "unknown" }

package ctrader

import "errors"

type RetryDisposition string

const (
	RetryNever          RetryDisposition = "NEVER"
	RetryAfterReconnect RetryDisposition = "AFTER_RECONNECT"
	RetrySafe           RetryDisposition = "SAFE"
)

// ClassifyRetry keeps retry decisions explicit. Unknown errors are never
// retried automatically because order status may be ambiguous.
func ClassifyRetry(err error) RetryDisposition {
	if err == nil {
		return RetryNever
	}
	var protocolErr *ProtocolError
	if errors.As(err, &protocolErr) {
		if protocolErr.Retriable {
			return RetryAfterReconnect
		}
		return RetryNever
	}
	var protocolValue ProtocolError
	if errors.As(err, &protocolValue) {
		if protocolValue.Retriable {
			return RetryAfterReconnect
		}
	}
	return RetryNever
}

func IsRetryable(err error) bool { return ClassifyRetry(err) != RetryNever }

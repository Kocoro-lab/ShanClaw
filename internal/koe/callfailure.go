package koe

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
)

// Why this exists: a call that fails to come up used to reach Desktop as a bare
// `call_state: ended` — byte for byte the same event a normal hang-up sends —
// with the reason going only to koe.log. The user saw the call screen open and
// close instantly and had no way to learn why. Measured 2026-08-27: 25,685
// consecutive failures over two days reading, to the user, as "it hangs up the
// moment it connects".
//
// These codes are the closed set Desktop localizes. Every one of them was
// derived from a failure actually observed in koe.log, not invented:
//
//	account_required     "mint relay failed: realtime usage principal unavailable"
//	                     — the daemon has no Cloud-verified account to bill the
//	                     turn to, so it refuses to relay. The one the user hit.
//	quota_exceeded       402/429 from the mint gate (realtime's only pre-spend check)
//	backend_unavailable  5xx / "mint failed" / "404 page not found" from Cloud
//	network              dial, DNS, i/o timeout, context deadline
//	audio                the local device failed to open or reconfigure
//	unknown              anything unclassified — still better than silence
const (
	CallFailureAccountRequired    = "account_required"
	CallFailureQuotaExceeded      = "quota_exceeded"
	CallFailureBackendUnavailable = "backend_unavailable"
	CallFailureNetwork            = "network"
	CallFailureAudio              = "audio"
	CallFailureUnknown            = "unknown"
)

// ClassifyCallFailure maps a call-bringup error to one of the codes above.
//
// Order is load-bearing. The account case arrives WRAPPED in an HTTP 502, so a
// status-code check that ran first would report the user's actual problem —
// "no account is bound, realtime cannot be billed" — as the useless and
// misleading "the service is temporarily unavailable".
func ClassifyCallFailure(err error) string {
	if err == nil {
		return ""
	}
	lower := strings.ToLower(err.Error())

	// Identity before status: see the ordering note above.
	if strings.Contains(lower, "usage principal") {
		return CallFailureAccountRequired
	}

	var bootstrap *RealtimeBootstrapError
	if errors.As(err, &bootstrap) {
		switch {
		case bootstrap.StatusCode == http.StatusUnauthorized,
			bootstrap.StatusCode == http.StatusForbidden:
			return CallFailureAccountRequired
		case bootstrap.StatusCode == http.StatusPaymentRequired,
			bootstrap.StatusCode == http.StatusTooManyRequests:
			return CallFailureQuotaExceeded
		case bootstrap.StatusCode >= 500, bootstrap.StatusCode == http.StatusNotFound:
			return CallFailureBackendUnavailable
		}
	}

	// Transport, whether typed or only describable in text. The string arm is
	// not redundant: these errors reach here having been wrapped and re-wrapped
	// through the daemon relay, where the typed net.Error no longer survives
	// errors.As (measured — the relay reports them as plain HTTP body text).
	var netErr net.Error
	if errors.As(err, &netErr) || errors.Is(err, context.DeadlineExceeded) {
		return CallFailureNetwork
	}
	for _, marker := range []string{
		"no such host", "dial tcp", "i/o timeout", "connection refused",
		"context deadline exceeded", "unexpected eof", "tls handshake",
	} {
		if strings.Contains(lower, marker) {
			return CallFailureNetwork
		}
	}

	// Last: a bare 5xx/mint failure with none of the more specific markers.
	if strings.Contains(lower, "http 5") || strings.Contains(lower, "mint failed") ||
		strings.Contains(lower, "404 page not found") {
		return CallFailureBackendUnavailable
	}
	return CallFailureUnknown
}

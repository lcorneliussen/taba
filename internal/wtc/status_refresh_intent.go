package wtc

import (
	"context"
	"errors"
	"time"
)

// StatusRefreshIntent distinguishes a requested observation from periodic work.
// Foreground bypasses ordinary detail TTL and periodic reuse for active records;
// recorded and retained merges still bypass provider reads. Accounting and cooldowns apply.
type StatusRefreshIntent uint8

const (
	StatusRefreshBackground StatusRefreshIntent = iota
	StatusRefreshForeground
)

const statusForegroundTimeout = 30 * time.Second

// StatusObservation describes the result of a read, independently of the last
// known PR state. RetryAt is populated only from an actual admission/cooldown
// decision. Missing admission or budget reasons in older snapshots stay unknown;
// observation age can independently establish expiration.
type StatusObservation struct {
	Freshness         string `json:"freshness,omitempty"`
	Reason            string `json:"reason,omitempty"`
	RetryAt           string `json:"retry_at,omitempty"`
	ObservedAt        string `json:"observed_at,omitempty"`
	Source            string `json:"source,omitempty"`
	ProviderConfirmed bool   `json:"provider_confirmed,omitempty"`
}

type statusAdmissionError struct {
	cause error
	retry time.Time
}

func (e *statusAdmissionError) Error() string { return e.cause.Error() }
func (e *statusAdmissionError) Unwrap() error { return e.cause }

func statusRefreshContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, statusForegroundTimeout)
}

func statusObservationFailure(err error) StatusObservation {
	out := StatusObservation{Freshness: "unavailable", Reason: "refresh-failed"}
	switch {
	case errors.Is(err, errForgeCooling):
		out.Freshness, out.Reason = "deferred", "provider-cooldown"
	case errors.Is(err, errForgeBudget):
		out.Freshness, out.Reason = "deferred", "shared-budget"
	case errors.Is(err, context.Canceled):
		out.Freshness, out.Reason = "deferred", "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		out.Freshness, out.Reason = "deferred", "deadline"
	default:
		// A refused Bitbucket read names its cause (#471), distinct from a
		// network failure, so the row is not mistaken for "no pull request".
		var classified *bitbucketReadError
		var httpErr *bitbucketHTTPError
		if errors.As(err, &classified) || errors.As(err, &httpErr) {
			if refusal := ClassifyBitbucketRefusal(err, ""); refusal.Access() {
				out.Reason = "bitbucket-" + refusal.Cause
			}
		}
	}
	var admission *statusAdmissionError
	if errors.As(err, &admission) && !admission.retry.IsZero() {
		out.RetryAt = admission.retry.UTC().Format(time.RFC3339Nano)
	}
	return out
}

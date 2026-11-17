package wtc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Check evidence is separate from code-review judgment and required-check policy.
// Only normalized states leave the collector; forge names and prose are untrusted.
type headCheckEvidence struct {
	Head         string `json:"head"`
	Result       string `json:"result"`
	Complete     bool   `json:"complete"`
	ObservedAt   string `json:"observed_at"`
	Requiredness string `json:"requiredness"`
}

func unknownHeadChecks(head string) headCheckEvidence {
	return headCheckEvidence{Head: head, Result: "UNKNOWN", ObservedAt: time.Now().UTC().Format(time.RFC3339), Requiredness: "unknown"}
}

func validCheckHead(head string) bool {
	return len(head) == 40 && validCheckHeadPrefix(head)
}

func checkAggregate(states []string, complete bool) string {
	pending, passed := false, false
	for _, state := range states {
		switch state {
		case "FAILURE":
			return "FAILURE"
		case "SUCCESS":
			passed = true
		default:
			pending = true
		}
	}
	if !complete {
		return "UNKNOWN"
	}
	if pending {
		return "PENDING"
	}
	if passed {
		return "SUCCESS"
	}
	return "NONE"
}

func bbCheckState(state string) string {
	switch strings.ToUpper(state) {
	case "SUCCESSFUL":
		return "SUCCESS"
	case "FAILED", "ERROR", "STOPPED":
		return "FAILURE"
	default:
		return "PENDING"
	}
}

// Every page spends the shared bitbucket.org forge allowance. Partial collections
// retain observed failures but cannot claim success or absence of checks.
func fetchBBCheckPages(endpoint, user, pass string, consume func(json.RawMessage) error) bool {
	complete, _ := fetchBBCheckPagesContext(context.Background(), StatusRefreshBackground, endpoint, user, pass, consume)
	return complete
}

func fetchBBCheckPagesContext(ctx context.Context, intent StatusRefreshIntent, endpoint, user, pass string, consume func(json.RawMessage) error) (bool, error) {
	seen := map[string]bool{}
	for page := 0; endpoint != "" && page < 10; page++ {
		if seen[endpoint] || !statusSameOrigin(statusBBAPI, endpoint) {
			return false, errors.New("incomplete check pages")
		}
		seen[endpoint] = true
		raw, err := statusBBCheckRead(ctx, intent, endpoint, user, pass)
		if err != nil {
			return false, err
		}
		var body struct {
			Values []json.RawMessage `json:"values"`
			Next   string            `json:"next"`
		}
		if json.Unmarshal(raw, &body) != nil || body.Values == nil {
			return false, errors.New("incomplete check pages")
		}
		for _, item := range body.Values {
			if consume(item) != nil {
				return false, errors.New("incomplete check pages")
			}
		}
		endpoint = body.Next
	}
	if endpoint != "" {
		return false, errors.New("check page limit reached")
	}
	return true, nil
}

func statusBBHeadChecks(slug, head string, fresh bool) headCheckEvidence {
	evidence, _ := statusBBHeadChecksContext(context.Background(), StatusRefreshBackground, slug, head, fresh)
	return evidence
}

// Foreground checks share the metadata phase deadline and per-hop allowance.
// Background/review wrappers retain their existing cache and admission policy.
func statusBBHeadChecksContext(ctx context.Context, intent StatusRefreshIntent, slug, head string, fresh bool) (headCheckEvidence, error) {
	if intent == StatusRefreshForeground {
		var cancel context.CancelFunc
		ctx, cancel = statusRefreshContext(ctx)
		defer cancel()
	}
	evidence := unknownHeadChecks(head)
	parts := strings.Split(slug, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || !validCheckHeadPrefix(head) {
		return evidence, nil
	}
	user, pass := bitbucketCredentials()
	if user == "" || pass == "" {
		if intent == StatusRefreshForeground {
			if raw, ok := statusReadForgeEntryAny("checks", "bitbucket.org", slug, head); ok {
				var saved headCheckEvidence
				if json.Unmarshal(raw, &saved) == nil && saved.Head == head && saved.Result == "FAILURE" {
					saved.Complete = false
					return saved, errors.New("head checks credentials unavailable")
				}
			}
			return evidence, errors.New("head checks credentials unavailable")
		}
		return evidence, nil
	}
	if len(head) < 40 {
		resolved, err := resolveBBCheckHeadContext(ctx, intent, slug, head, user, pass)
		if err != nil {
			return evidence, err
		}
		head = resolved
		evidence = unknownHeadChecks(head)
	}
	var fetchErr error
	fetch := func() ([]byte, error) {
		result := unknownHeadChecks(head)
		root := statusBBAPI + "/repositories/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1])
		var states []string
		statusesComplete, statusErr := fetchBBCheckPagesContext(ctx, intent, root+"/commit/"+head+"/statuses?pagelen=100", user, pass, func(raw json.RawMessage) error {
			var status struct {
				State string `json:"state"`
			}
			if err := json.Unmarshal(raw, &status); err != nil {
				return err
			}
			states = append(states, bbCheckState(status.State))
			return nil
		})
		query := url.Values{"target.commit.hash": {head}, "sort": {"-created_on"}, "pagelen": {"100"}}
		seen := map[string]bool{}
		pipelinesComplete, pipelineErr := fetchBBCheckPagesContext(ctx, intent, root+"/pipelines/?"+query.Encode(), user, pass, func(raw json.RawMessage) error {
			var run struct {
				State struct {
					Name   string `json:"name"`
					Result struct {
						Name string `json:"name"`
					} `json:"result"`
				} `json:"state"`
				Target struct {
					Commit struct {
						Hash string `json:"hash"`
					} `json:"commit"`
					Selector struct {
						Type    string `json:"type"`
						Pattern string `json:"pattern"`
					} `json:"selector"`
				} `json:"target"`
			}
			if err := json.Unmarshal(raw, &run); err != nil {
				return err
			}
			if run.Target.Commit.Hash != head {
				return nil
			}
			selector := run.Target.Selector.Type
			if !statusHeadPipelineSelector(selector) {
				return nil
			}
			// The query sorts by creation time descending, so the first rerun wins.
			key := selector + ":" + run.Target.Selector.Pattern
			if seen[key] {
				return nil
			}
			seen[key] = true
			state := "PENDING"
			if strings.EqualFold(run.State.Name, "COMPLETED") {
				state = bbCheckState(run.State.Result.Name)
			}
			states = append(states, state)
			return nil
		})
		fetchErr = errors.Join(statusErr, pipelineErr)
		result.Complete = statusesComplete && pipelinesComplete
		result.Result = checkAggregate(states, result.Complete)
		return json.Marshal(result)
	}
	if intent == StatusRefreshForeground {
		cached := unknownHeadChecks(head)
		if raw, ok := statusReadForgeEntryAny("checks", "bitbucket.org", slug, head); ok {
			var saved headCheckEvidence
			if json.Unmarshal(raw, &saved) == nil && saved.Head == head {
				cached = saved
			}
		}
		// Known failures remain evidence when admission, I/O or a lock fails.
		fallback := func(err error) (headCheckEvidence, error) {
			cached.Complete = false
			if cached.Result != "FAILURE" {
				cached.Result = "UNKNOWN"
			}
			return cached, err
		}
		unlock, err := statusLockCacheContext(ctx, statusForgeEntryPath("checks", "bitbucket.org", slug, head))
		if err != nil {
			return fallback(err)
		}
		defer unlock()
		if raw, ok := statusReadForgeEntryAny("checks", "bitbucket.org", slug, head); ok {
			var saved headCheckEvidence
			if json.Unmarshal(raw, &saved) == nil && saved.Head == head {
				cached = saved
			}
		}
		started := time.Now()
		raw, err := fetch()
		if err != nil {
			return fallback(err)
		}
		if json.Unmarshal(raw, &evidence) != nil {
			return fallback(errors.New("invalid check evidence"))
		}
		if err := ctx.Err(); err != nil {
			// A received failing status remains evidence even if a later page
			// was cancelled. Cancellation still forbids cache publication.
			if evidence.Result == "FAILURE" {
				cached = evidence
			}
			return fallback(err)
		}
		if !evidence.Complete && cached.Result == "FAILURE" && evidence.Result != "FAILURE" {
			cached.Complete = false
			evidence = cached
			raw, _ = json.Marshal(evidence)
		}
		publication, err := statusWriteForgeEntrySinceObservedContext(ctx, "checks", "bitbucket.org", slug, head, raw, started, time.Now())
		if err != nil {
			return fallback(err)
		}
		if !publication.Written {
			if winner, ok := statusReadForgeEntryAny("checks", "bitbucket.org", slug, head); ok {
				var newer headCheckEvidence
				if json.Unmarshal(winner, &newer) == nil && newer.Head == head {
					if !newer.Complete {
						return newer, errors.New("incomplete newer head checks")
					}
					return newer, nil
				}
			}
			return fallback(errors.New("check evidence publication failed"))
		}
		if !evidence.Complete {
			if fetchErr != nil {
				return evidence, fetchErr
			}
			return evidence, errors.New("incomplete head checks")
		}
		return evidence, nil
	}
	// The collector budgets each HTTP request itself; avoid charging an extra
	// token for its aggregate.
	if fresh {
		raw, _ := fetch()
		_ = json.Unmarshal(raw, &evidence)
		return evidence, nil
	}
	// Status uses a head-keyed cache under the existing lock.
	unlock, err := statusLockCache(statusForgeEntryPath("checks", "bitbucket.org", slug, head))
	if err == nil {
		defer unlock()
	}
	if raw, ok := statusReadForgeEntry("checks", "bitbucket.org", slug, head); ok {
		if json.Unmarshal(raw, &evidence) == nil && evidence.Head == head {
			return evidence, nil
		}
	}
	started := time.Now()
	raw, err := fetch()
	if err == nil {
		if json.Unmarshal(raw, &evidence) == nil {
			statusWriteForgeEntrySince("checks", "bitbucket.org", slug, head, raw, started)
		}
	}
	return evidence, nil
}

func (e headCheckEvidence) report() string {
	conclusion := "Observed checks: " + e.Result + "."
	if e.Result == "FAILURE" {
		conclusion = "Confirmed failing check on the reviewed head."
	}
	if !e.Complete {
		conclusion += " Evidence is incomplete; unavailable queries are not green."
	}
	return fmt.Sprintf("### Forge checks\n\n%s Head `%s`; observed %s. Required-check policy is unknown. This evidence is separate from the code-review verdict.\n", conclusion, e.Head, e.ObservedAt)
}

func validCheckHeadPrefix(head string) bool {
	if len(head) < 12 || len(head) > 40 {
		return false
	}
	for _, c := range head {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// Resolve abbreviations through the immutable commit endpoint before caching
// or filtering pipelines. Never attach prefix-scoped evidence to another head.
func resolveBBCheckHead(slug, prefix, user, pass string) (string, error) {
	return resolveBBCheckHeadContext(context.Background(), StatusRefreshBackground, slug, prefix, user, pass)
}

func resolveBBCheckHeadContext(ctx context.Context, intent StatusRefreshIntent, slug, prefix, user, pass string) (string, error) {
	parts := strings.Split(slug, "/")
	endpoint := statusBBAPI + "/repositories/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1]) + "/commit/" + prefix
	fetch := func() ([]byte, error) { return statusBBGET(endpoint, user, pass) }
	var raw []byte
	var err error
	if intent == StatusRefreshForeground {
		unlock, lockErr := statusLockCacheContext(ctx, statusForgeEntryPath("checkhead", "bitbucket.org", slug, prefix))
		if lockErr != nil {
			return "", lockErr
		}
		defer unlock()
		if cached, ok := statusReadForgeEntry("checkhead", "bitbucket.org", slug, prefix); ok {
			raw = cached
		} else {
			started := time.Now()
			raw, err = statusBBCheckRead(ctx, intent, endpoint, user, pass)
			if err == nil {
				var publication statusCachePublication
				publication, err = statusWriteForgeEntrySinceObservedContext(ctx, "checkhead", "bitbucket.org", slug, prefix, raw, started, time.Now())
				if err == nil && !publication.Written {
					var ok bool
					raw, ok = statusReadForgeEntryAny("checkhead", "bitbucket.org", slug, prefix)
					if !ok {
						err = errors.New("head resolution superseded without evidence")
					}
				}
			}
		}
	} else {
		raw, err = statusSingleFlight("checkhead", "bitbucket.org", slug, prefix, fetch)
	}
	if err != nil {
		return "", err
	}
	var commit struct {
		Hash string `json:"hash"`
	}
	if json.Unmarshal(raw, &commit) != nil || !validCheckHead(commit.Hash) || !strings.HasPrefix(commit.Hash, prefix) {
		return "", fmt.Errorf("cannot resolve exact check head")
	}
	return commit.Hash, nil
}

// Each actual check page or immutable-head resolution pays once. Redirects pay
// inside the HTTP helper; the aggregate never consumes an additional token.
func statusBBCheckRead(ctx context.Context, intent StatusRefreshIntent, endpoint, user, pass string) ([]byte, error) {
	if intent != StatusRefreshForeground {
		raw, _, err := statusWithForgeBudget("bitbucket.org", nil, func() ([]byte, error) { return statusBBGET(endpoint, user, pass) })
		return raw, err
	}
	if err := statusTakeForegroundToken(ctx, "bitbucket.org"); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw, err := statusBBGETContext(ctx, intent, endpoint, user, pass)
	if err != nil && statusRateLimited(err) {
		until, coolErr := statusForgeCoolContext(ctx, "bitbucket.org", statusRetryAfter(err))
		if coolErr != nil {
			return nil, coolErr
		}
		return nil, &statusAdmissionError{cause: errForgeCooling, retry: until}
	}
	return raw, err
}

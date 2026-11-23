package wtc

import (
	"context"
	"errors"
	"os/exec"
	"strings"
)

// Refused Bitbucket reads are visible, with their cause (#471): a missing,
// invalid or expired credential, a missing scope, or a repository the
// credential cannot see is not the same as a network failure, and it must not
// look like "no pull request" or a silent UNKNOWN.

// Refusal causes. The access causes are the ones a person must act on; the
// transient ones (network, timeout, rate-limited) clear on their own.
const (
	RefusalNoCredential = "no-credential"          // no credential for this repository
	RefusalUnsupported  = "unsupported-credential" // the configured credential type cannot be used here
	RefusalUnauthorized = "unauthorized"           // 401: credential invalid or expired
	RefusalForbidden    = "forbidden"              // 403: a scope or permission is missing
	RefusalNotFound     = "not-visible"            // 404: the credential cannot see the repository
	RefusalClient       = "client-unavailable"     // the selected client is missing or unsupported
	RefusalRateLimited  = "rate-limited"
	RefusalTimeout      = "timeout"
	RefusalNetwork      = "network"
	RefusalIncomplete   = "incomplete"
	RefusalOther        = "other"
)

// bitbucketCauseBranchLookup refuses a gateway branch lookup through bb.
const bitbucketCauseBranchLookup = "branch lookups need twg; the deprecated bb client is selected (unset " + bitbucketClientVariable + ")"

// BitbucketRefusal is one classified read failure. Message is fixed text for
// people; it never holds a credential, response body or stderr.
type BitbucketRefusal struct {
	Cause   string `json:"cause"`
	Status  int    `json:"status,omitempty"`
	Message string `json:"message"`
}

// Access reports whether a person must change access or credentials.
func (r BitbucketRefusal) Access() bool {
	switch r.Cause {
	case RefusalNoCredential, RefusalUnsupported, RefusalUnauthorized, RefusalForbidden, RefusalNotFound, RefusalClient:
		return true
	}
	return false
}

// bitbucketHTTPError is a REST answer outside 2xx. Its text is the existing
// diagnostic; status lets the cause be classified.
type bitbucketHTTPError struct {
	status int
	text   string
}

func (e *bitbucketHTTPError) Error() string { return e.text }

// ClassifyBitbucketRefusal classifies a read error. credential names the
// read credential in use, for the message (never its value).
func ClassifyBitbucketRefusal(err error, credential string) BitbucketRefusal {
	status := 0
	var classified *bitbucketReadError
	var httpErr *bitbucketHTTPError
	switch {
	case errors.As(err, &httpErr):
		status = httpErr.status
	case errors.As(err, &classified):
		status = classified.status
	}
	text := ""
	if err != nil {
		text = err.Error()
	}
	who := "the read credential"
	if credential != "" {
		who = credential
	}
	switch {
	case status == 401:
		return BitbucketRefusal{RefusalUnauthorized, 401, "Bitbucket read refused (401): " + who + " is invalid or expired; replace it"}
	case status == 403:
		return BitbucketRefusal{RefusalForbidden, 403, "Bitbucket read refused (403): " + who + " lacks a permission or scope (repository and pull request read)"}
	case status == 404:
		return BitbucketRefusal{RefusalNotFound, 404, "Bitbucket read refused (404): " + who + " cannot see this repository; grant it read access"}
	case status == 429 || strings.Contains(text, "HTTP 429") || strings.Contains(text, "returned 429"):
		return BitbucketRefusal{RefusalRateLimited, 429, "Bitbucket rate limit; retried later"}
	case strings.Contains(text, bitbucketCauseNoCredential):
		return BitbucketRefusal{Cause: RefusalNoCredential, Message: "no Bitbucket read credential for this repository: the host's git credential helper has none"}
	case strings.Contains(text, "need the twg client") || strings.Contains(text, "unsupported "+bitbucketReadCredentialVariable) || strings.Contains(text, bitbucketCauseCredentialFail) || strings.Contains(text, "gave no username"):
		return BitbucketRefusal{Cause: RefusalUnsupported, Message: "the configured Bitbucket read credential cannot be used: " + text}
	case classified != nil && classified.cause == bitbucketCauseAuth:
		return BitbucketRefusal{Cause: RefusalUnauthorized, Message: "Bitbucket read refused: " + who + " was not accepted"}
	case errors.Is(err, exec.ErrNotFound) || strings.Contains(text, "is not installed") || strings.Contains(text, "unsupported TWG version") || strings.Contains(text, bitbucketCauseBranchLookup) ||
		strings.Contains(text, "Bitbucket client selection unavailable") || strings.Contains(text, "unsupported "+bitbucketClientVariable):
		return BitbucketRefusal{Cause: RefusalClient, Message: "the Bitbucket client cannot run: " + text}
	case errors.Is(err, context.DeadlineExceeded) || strings.Contains(text, "timed out"):
		return BitbucketRefusal{Cause: RefusalTimeout, Message: "Bitbucket read timed out"}
	case strings.Contains(text, "API request failed") || strings.Contains(text, "redirect"):
		return BitbucketRefusal{Cause: RefusalNetwork, Message: "Bitbucket could not be reached"}
	case classified != nil && (classified.cause == bitbucketCauseIncomplete || classified.cause == bitbucketCauseMalformed || classified.cause == bitbucketCauseInvalidJSON):
		return BitbucketRefusal{Cause: RefusalIncomplete, Message: "Bitbucket answered incompletely; retried later"}
	}
	return BitbucketRefusal{Cause: RefusalOther, Message: "Bitbucket read failed"}
}

// BitbucketReadCredentialLabel names the selected read credential for
// diagnostics, never its value. root is the control root ("" uses the
// environment).
func BitbucketReadCredentialLabel(root string) string {
	return selectedBitbucketRead(root).CredentialLabel()
}

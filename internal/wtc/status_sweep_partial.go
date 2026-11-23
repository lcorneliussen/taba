package wtc

import (
	"context"
	"errors"
	"os"
	"strings"
)

// StatusUnreadable is one collection a workspace sweep could not read, or,
// in a local sweep, one whose named repository has no resolvable Git owner
// while its other repositories were read. The reason is a fixed class, never
// the raw error: an open failure may carry paths, environment values or output
// that the report must not repeat. Only the repository-owner reason adds the
// sanitized repository name.
type StatusUnreadable struct {
	Collection string `json:"collection"`
	Reason     string `json:"reason"`
}

const (
	unreadableGeneratedRoot = "generated environment names a control root the receiving policy rejects; preview `env setup --repair-generated-roots --dry-run` after reviewing receiving policy"
	unreadableLocalRoot     = "local environment names a control root the receiving policy rejects; remove the stale root assignment or declare it in receiving config"
	unreadableInheritedRoot = "inherited environment names a control root the receiving policy rejects"
	unreadablePolicy        = "receiving policy rejects this collection"
	unreadableEnvironment   = "collection environment file is not readable as a regular file"
	unreadableRegistry      = "harness registry is missing or invalid"
	unreadableRetired       = "collection is being retired"
	unreadableNotCollection = "not a collection"
	unreadableName          = "invalid collection name" // catch-up's row reason for the same case
	unreadableOther         = "collection context could not be opened"
	unreadableStatus        = "collection status could not be read"
	unreadableRepoOwner     = "cannot find Git owner of"
	unreadableInventory     = "collection inventory could not be read"
)

// collectionOpenFailure classifies an OpenCollectionContext error into one of
// the fixed reasons above. Status sweeps check cancellation first, since it is
// not a collection fact; catch-up records whatever its open returned.
func collectionOpenFailure(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, os.ErrNotExist) {
		return unreadableRegistry
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "in .env.collection conflicts with receiving secrets policy"):
		return unreadableGeneratedRoot
	case strings.Contains(msg, "in .env.collection.local conflicts with receiving secrets policy"):
		return unreadableLocalRoot
	case strings.Contains(msg, "in inherited environment conflicts with receiving secrets policy"):
		return unreadableInheritedRoot
	case strings.Contains(msg, "receiving environment"):
		return unreadableEnvironment
	case strings.Contains(msg, "secrets root") || strings.Contains(msg, "receiving owner") || strings.Contains(msg, "receiving collection"):
		return unreadablePolicy
	case strings.Contains(msg, "registry"):
		return unreadableRegistry
	case strings.Contains(msg, "retired"):
		return unreadableRetired
	case msg == "not a collection":
		return unreadableNotCollection
	}
	return unreadableOther
}

// statusInventoryUnreadable turns the failed collection and repository rows of
// a catch-up inventory into sweep rows. Only fixed reasons and the sanitized
// repository name pass through; anything else becomes a generic class.
func statusInventoryUnreadable(report CatchUpReport) []StatusUnreadable {
	var rows []StatusUnreadable
	seen := map[string]bool{}
	for _, row := range report.Outcomes {
		if row.Outcome != "failed" || row.Kind != "collection" && row.Kind != "repo" || seen[row.Collection+"\x00"+row.Repo] {
			continue
		}
		seen[row.Collection+"\x00"+row.Repo] = true
		reason := unreadableInventory
		switch {
		case row.Kind == "repo":
			// Its sibling repositories are still read; this one has no owner.
			reason = unreadableRepoOwner + " " + runtimeTextSafe(row.Repo)
		case row.Reason == unreadableName:
			reason = unreadableName
		case strings.HasPrefix(row.Reason, catchUpOpenFailurePrefix):
			reason = strings.TrimPrefix(row.Reason, catchUpOpenFailurePrefix)
		}
		rows = append(rows, StatusUnreadable{Collection: runtimeTextSafe(row.Collection), Reason: reason})
	}
	return rows
}

// statusCanceled reports the caller's cancellation, which ends a sweep rather
// than marking a collection unreadable.
func statusCanceled(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return nil
}

// statusSweepFatal returns the error that ends a whole sweep: cancellation,
// or a forge credential selection that changed under it. Either would make
// every later collection fail too, so it is not one collection's fact.
func statusSweepFatal(ctx context.Context, err error) error {
	if err := statusCanceled(ctx, err); err != nil {
		return err
	}
	if !statusAuthSelectionCurrent(ctx) {
		return errStatusCredentialChanged
	}
	return nil
}

var errStatusCredentialChanged = errors.New("forge credential context changed")

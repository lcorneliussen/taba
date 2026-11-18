package wtc

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

type statusPublicationHeads struct{ head, base, short string }

func statusParsePublicationHeads(raw string) (statusPublicationHeads, bool) {
	fields := strings.Split(strings.TrimSpace(raw), "\n")
	if len(fields) != 3 {
		return statusPublicationHeads{}, false
	}
	full := func(value string) bool {
		_, err := hex.DecodeString(value)
		return err == nil && (len(value) == 40 || len(value) == 64) && value == strings.ToLower(value)
	}
	if !full(fields[0]) || !full(fields[1]) || len(fields[0]) != len(fields[1]) ||
		len(fields[2]) < 4 || !strings.HasPrefix(fields[0], fields[2]) {
		return statusPublicationHeads{}, false
	}
	return statusPublicationHeads{fields[0], fields[1], fields[2]}, true
}

// The probe and compatibility reads share one original head-read budget.
// Git supplies the unique abbreviation; no mutable observation is memoized.
func statusReadPublicationHeads(ctx context.Context, row StatusRepo) (statusPublicationHeads, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	read := func(args ...string) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		value, err := statusGitContext(ctx, row.Worktree, args...)
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return value, err
	}
	if row.baseRef != "" {
		raw, err := read("rev-parse", "HEAD", row.baseRef, "--short", "HEAD")
		if ctx.Err() != nil {
			return statusPublicationHeads{}, ctx.Err()
		}
		if heads, ok := statusParsePublicationHeads(raw); err == nil && ok {
			return heads, nil
		}
	}
	var heads statusPublicationHeads
	if row.baseRef != "" {
		raw, err := read("rev-parse", "HEAD", row.baseRef)
		if ctx.Err() != nil {
			return heads, ctx.Err()
		}
		fields := strings.Fields(raw)
		if err == nil && len(fields) == 2 && validCheckHead(fields[0]) && validCheckHead(fields[1]) {
			heads.head, heads.base = fields[0], fields[1]
		}
	}
	if heads.head == "" {
		heads.head, _ = read("rev-parse", "HEAD")
		if ctx.Err() != nil {
			return heads, ctx.Err()
		}
		if row.baseRef != "" {
			heads.base, _ = read("rev-parse", row.baseRef)
		}
	}
	if ctx.Err() != nil {
		return heads, ctx.Err()
	}
	heads.short, _ = read("rev-parse", "--short", "HEAD")
	return heads, ctx.Err()
}

type statusPublicationRepo struct{ collection, repo, dir, worktree, branch, kind, base string }

func statusPublicationKey(row StatusRepo) statusPublicationRepo {
	return statusPublicationRepo{row.Collection, row.Repo, row.Dir, row.Worktree, row.Branch, row.BranchKind, row.baseRef}
}

// Selected collectors finalize and publish on this stack. Neither observations
// nor callbacks are attached to a receiver, snapshot or UI for later reuse.
func (c *Context) statusFinalizePublication(ctx context.Context, snapshot *StatusSnapshot) error {
	c = c.WithStatusContext(ctx)
	if err := c.statusPublicationError(ctx, *snapshot); err != nil {
		return err
	}
	shorts := map[statusPublicationRepo]string{}
	associations := map[statusPublicationRepo]bool{}
	for _, row := range snapshot.Repos {
		associations[statusPublicationKey(row)] = true
	}
	if err := statusNextWithHeads(ctx, snapshot, func(ctx context.Context, row StatusRepo) (string, string, error) {
		heads, err := statusReadPublicationHeads(ctx, row)
		if err == nil {
			shorts[statusPublicationKey(row)] = heads.short
		}
		return heads.head, heads.base, err
	}); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	c.statusProgress("Writing status snapshot")
	return c.writeStatusSnapshotContext(ctx, *snapshot, func(row StatusRepo) (string, error) {
		key := statusPublicationKey(row)
		if !associations[key] {
			return "", fmt.Errorf("repository association changed during status publication")
		}
		if short, ok := shorts[key]; ok {
			return short, nil
		}
		// Rows ineligible for choices still keep the independent legacy read.
		short, _ := statusGitContext(ctx, row.Worktree, "rev-parse", "--short", "HEAD")
		return short, ctx.Err()
	})
}

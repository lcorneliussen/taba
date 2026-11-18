package wtc

import (
	"context"
	"encoding/hex"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// These facts belong to this inspection only. Every command keeps retirement's
// guarded runner; no status fact cache or live-resource policy participates.
func statusResourceRetirementRead(ctx context.Context, path string) RetirementRepo {
	row := RetirementRepo{Directory: filepath.Base(path), Unpushed: -1}
	localCtx, stop := context.WithTimeout(ctx, 30*time.Second)
	defer stop()
	raw, err := retirementGitContext(localCtx, path, "status", "--porcelain=v2", "--branch", "--untracked-files=normal")
	if localCtx.Err() != nil {
		row.Error = "Git evidence unavailable"
		return row
	}
	head, branch, dirty, ok := statusResourceRetirementParse(raw)
	if err == nil && ok && branch == "(detached)" {
		// A real branch may have this name; preserve the legacy symbolic read.
		branch, err = retirementGitContext(localCtx, path, "branch", "--show-current")
	}
	if localCtx.Err() != nil {
		row.Error = "Git evidence unavailable"
		return row
	}
	if err == nil && ok {
		row.Head, row.Branch, row.Dirty = head, branch, dirty
	} else {
		// Unsupported, unborn or malformed output retains the established reader.
		// The probe and fallback share the original first-read budget.
		read := func(args ...string) string {
			if row.Error != "" {
				return ""
			}
			out, e := retirementGitContext(localCtx, path, args...)
			if e != nil {
				row.Error = "Git evidence unavailable"
				return ""
			}
			return out
		}
		row.Head = read("rev-parse", "--verify", "HEAD")
		row.Branch = read("branch", "--show-current")
		row.Dirty = read("status", "--porcelain", "--untracked-files=normal") != ""
	}
	if localCtx.Err() != nil {
		row.Error = "Git evidence unavailable"
		return row
	}
	stop()
	if row.Error != "" {
		return row
	}
	value, err := retirementGitContext(ctx, path, "rev-list", "--count", "HEAD", "--not", "--remotes")
	if err != nil {
		row.Error = "Git evidence unavailable"
		return row
	}
	row.Unpushed, err = strconv.Atoi(value)
	if err != nil {
		row.Error = "Cannot count commits absent from cached remote refs"
	}
	return row
}

func statusResourceRetirementParse(raw string) (head, branch string, dirty, ok bool) {
	haveHead, haveBranch := false, false
	for _, line := range strings.Split(raw, "\n") {
		switch {
		case strings.HasPrefix(line, "# branch.oid "):
			if haveHead {
				return "", "", false, false
			}
			haveHead = true
			head = strings.TrimPrefix(line, "# branch.oid ")
		case strings.HasPrefix(line, "# branch.head "):
			if haveBranch {
				return "", "", false, false
			}
			haveBranch = true
			branch = strings.TrimPrefix(line, "# branch.head ")
		case strings.HasPrefix(line, "# branch.upstream "), strings.HasPrefix(line, "# branch.ab "):
		case strings.HasPrefix(line, "1 "), strings.HasPrefix(line, "2 "), strings.HasPrefix(line, "u "):
			count := 9
			if line[0] == '2' {
				count = 10
			} else if line[0] == 'u' {
				count = 11
			}
			fields := strings.SplitN(line, " ", count)
			if len(fields) != count || fields[count-1] == "" || len(fields[1]) != 2 || len(fields[2]) != 4 {
				return "", "", false, false
			}
			dirty = true
		case strings.HasPrefix(line, "? "):
			if len(line) == 2 {
				return "", "", false, false
			}
			dirty = true
		case line == "":
		default:
			return "", "", false, false
		}
	}
	if !haveHead || !haveBranch || branch == "" || strings.ContainsAny(branch, "\x00\r\n") || (len(head) != 40 && len(head) != 64) {
		return "", "", false, false
	}
	if _, err := hex.DecodeString(head); err != nil || head != strings.ToLower(head) {
		return "", "", false, false
	}
	return head, branch, dirty, true
}

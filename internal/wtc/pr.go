package wtc

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// PRRecord is the collection-local link from a branch to a forge pull request.
// The file format is shared with the shell status and catch-up commands.
type PRRecord struct {
	Repo        string `json:"repo"`
	Number      string `json:"number"`
	Branch      string `json:"branch,omitempty"`
	URL         string `json:"url,omitempty"`
	Title       string `json:"title,omitempty"`
	MergedOn    string `json:"merged_on,omitempty"`
	FinalChecks string `json:"final_checks,omitempty"`
	MergeCommit string `json:"merge_commit,omitempty"`
	MergeBase   string `json:"merge_base,omitempty"`
	MergeHead   string `json:"merge_head,omitempty"`
	// DeclinedOn and DeclinedAt record a Bitbucket decline (updated_on and,
	// when known, the decline event). Background refresh trusts the record
	// until a current open list (even a capped one) shows the PR again.
	DeclinedOn string `json:"declined_on,omitempty"`
	DeclinedAt string `json:"declined_at,omitempty"`
}

const prHeader = "# Local PR enlistment for this collection (not committed; dies with retire).\n# Format: repo  number  [branch]  [url]  [title…]\n# Final merges: # merged-pr repo number merged-at checks\n# Merge commits: # merged-commit repo number sha [base]\n# Merged heads: # merged-head repo number merge-sha head-sha\n# Bitbucket declines: # declined-pr repo number updated-on [declined-at]\n# Manage: wtc pr enlist|unlist|list\n"

var prNumber = regexp.MustCompile(`^[0-9]+$`)
var prRepoName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func ValidatePRIdentity(repo, number string) error {
	if !prRepoName.MatchString(repo) || repo == "." || repo == ".." || strings.Contains(repo, "..") || !prNumber.MatchString(number) {
		return fmt.Errorf("expected a single repository name and numeric PR number")
	}
	return nil
}

// PRFile reports the selected registry path. Readers and mutators also check
// selection errors; a broken legacy path must not hide an alternate registry.
func (c *Context) PRFile() string {
	path, _ := c.prInput()
	return path
}

func (c *Context) prInput() (string, error) {
	return collectionStateInput(c.Collection, ".wtc-prs", ".taba-prs")
}

func cleanPRField(s string) string { return strings.Join(strings.Fields(s), " ") }

func prURL(remote, number string) string {
	remote = strings.TrimSuffix(strings.TrimSpace(remote), ".git")
	for _, forge := range []struct{ host, suffix string }{{"github.com", "pull"}, {"bitbucket.org", "pull-requests"}} {
		for _, prefix := range []string{"https://" + forge.host + "/", "http://" + forge.host + "/", "git@" + forge.host + ":", "ssh://git@" + forge.host + "/"} {
			if strings.HasPrefix(remote, prefix) {
				slug := strings.TrimPrefix(remote, prefix)
				if strings.Count(slug, "/") == 1 {
					return "https://" + forge.host + "/" + slug + "/" + forge.suffix + "/" + number
				}
			}
		}
	}
	return ""
}

func parsePR(line string) (PRRecord, bool) {
	fields := strings.Fields(line)
	if len(fields) < 2 || strings.HasPrefix(fields[0], "#") {
		return PRRecord{}, false
	}
	r := PRRecord{Repo: fields[0], Number: fields[1]}
	if len(fields) > 2 && fields[2] != "-" {
		r.Branch = fields[2]
	}
	if len(fields) > 3 && fields[3] != "-" {
		r.URL = fields[3]
	}
	if len(fields) > 4 {
		r.Title = strings.Join(fields[4:], " ")
	}
	return r, true
}

func prRecordKey(repo, number string) string { return repo + "\x00" + number }

func parseMergedPR(line string) (repo, number, mergedOn, checks string, ok bool) {
	fields := strings.Fields(line)
	if len(fields) != 6 || fields[0] != "#" || fields[1] != "merged-pr" || ValidatePRIdentity(fields[2], fields[3]) != nil {
		return "", "", "", "", false
	}
	if _, err := time.Parse(time.RFC3339, fields[4]); err != nil {
		return "", "", "", "", false
	}
	switch fields[5] {
	case "SUCCESS", "FAILURE", "NONE":
		return fields[2], fields[3], fields[4], fields[5], true
	}
	return "", "", "", "", false
}

// parseDeclinedPR reads a recorded Bitbucket decline: updated_on of the
// observed decline and, when the activity log dated it, the decline event.
func parseDeclinedPR(line string) (repo, number, declinedOn, declinedAt string, ok bool) {
	fields := strings.Fields(line)
	if (len(fields) != 5 && len(fields) != 6) || fields[0] != "#" || fields[1] != "declined-pr" || ValidatePRIdentity(fields[2], fields[3]) != nil {
		return "", "", "", "", false
	}
	if _, err := time.Parse(time.RFC3339, fields[4]); err != nil {
		return "", "", "", "", false
	}
	if len(fields) == 6 {
		if _, err := time.Parse(time.RFC3339, fields[5]); err != nil {
			return "", "", "", "", false
		}
		declinedAt = fields[5]
	}
	return fields[2], fields[3], fields[4], declinedAt, true
}

// parseMergeCommit reads the commit a merge landed as. It is a separate row so
// that older readers, which require exactly six merged-pr fields, keep working.
func parseMergeCommit(line string) (repo, number, sha, base string, ok bool) {
	fields := strings.Fields(line)
	if (len(fields) != 5 && len(fields) != 6) || fields[0] != "#" || fields[1] != "merged-commit" || ValidatePRIdentity(fields[2], fields[3]) != nil || !statusCommitHash.MatchString(fields[4]) {
		return "", "", "", "", false
	}
	if len(fields) == 6 {
		base = fields[5]
	}
	return fields[2], fields[3], fields[4], base, true
}

func (c *Context) ListPRs() ([]PRRecord, error) {
	path, err := c.prInput()
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var records []PRRecord
	merged := map[string]PRRecord{}
	commits := map[string][2]string{}
	heads := map[string][2]string{}
	declined := map[string][2]string{}
	s := bufio.NewScanner(f)
	for s.Scan() {
		if record, ok := parsePR(s.Text()); ok {
			records = append(records, record)
		} else if repo, number, when, checks, ok := parseMergedPR(s.Text()); ok {
			merged[prRecordKey(repo, number)] = PRRecord{MergedOn: when, FinalChecks: checks}
		} else if repo, number, merge, head, ok := parseMergedHead(s.Text()); ok {
			heads[prRecordKey(repo, number)] = [2]string{merge, head}
		} else if repo, number, sha, base, ok := parseMergeCommit(s.Text()); ok {
			commits[prRecordKey(repo, number)] = [2]string{sha, base}
		} else if repo, number, on, at, ok := parseDeclinedPR(s.Text()); ok {
			declined[prRecordKey(repo, number)] = [2]string{on, at}
		}
	}
	if err := s.Err(); err != nil {
		return nil, err
	}
	for i := range records {
		if final, ok := merged[prRecordKey(records[i].Repo, records[i].Number)]; ok {
			records[i].MergedOn, records[i].FinalChecks = final.MergedOn, final.FinalChecks
			commit := commits[prRecordKey(records[i].Repo, records[i].Number)]
			records[i].MergeCommit, records[i].MergeBase = commit[0], commit[1]
			if head := heads[prRecordKey(records[i].Repo, records[i].Number)]; head[0] == commit[0] {
				records[i].MergeHead = head[1]
			}
		} else if decline, ok := declined[prRecordKey(records[i].Repo, records[i].Number)]; ok {
			records[i].DeclinedOn, records[i].DeclinedAt = decline[0], decline[1]
		}
	}
	return records, nil
}

func formatPR(r PRRecord) string {
	parts := []string{r.Repo, r.Number}
	if r.Branch != "" || r.URL != "" || r.Title != "" {
		branch := r.Branch
		if branch == "" {
			branch = "-"
		}
		parts = append(parts, branch)
	}
	if r.URL != "" || r.Title != "" {
		url := r.URL
		if url == "" {
			url = "-"
		}
		parts = append(parts, url)
	}
	if r.Title != "" {
		parts = append(parts, r.Title)
	}
	return strings.Join(parts, " ")
}

func (c *Context) rewritePRs(removeRepo, removeNumber string, add *PRRecord) error {
	return c.withPRLock(func() error { return c.rewritePRsLocked(removeRepo, removeNumber, add) })
}

func (c *Context) withPRLock(action func() error) error {
	// Keep the legacy lock shared with older clients, even when reading an
	// alternate registry. Select the input only after acquiring this lock.
	path := filepath.Join(c.Collection, ".wtc-prs.lock")
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return fmt.Errorf("open PR registry lock: %w", err)
	}
	lock := os.NewFile(uintptr(fd), path)
	defer lock.Close()
	info, err := lock.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("PR registry lock is not a regular file")
	}
	if err := unix.Flock(fd, unix.LOCK_EX); err != nil {
		return fmt.Errorf("lock PR registry: %w", err)
	}
	defer unix.Flock(fd, unix.LOCK_UN)
	return action()
}

func (c *Context) rewritePRsLocked(removeRepo, removeNumber string, add *PRRecord) error {
	path, err := c.prInput()
	if err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("refusing non-regular PR file: %s", path)
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		if add == nil {
			return nil
		}
		data = []byte(prHeader)
	} else if err != nil {
		return err
	}
	var out bytes.Buffer
	repos := c.prRepositories(removeRepo)
	identity, err := repos.resolve(removeRepo)
	if err != nil && add != nil {
		return err
	}
	matching := map[string]bool{removeRepo: true}
	if identity != nil {
		for _, line := range strings.Split(string(data), "\n") {
			if record, ok := parsePR(line); ok && record.Number == removeNumber {
				other, err := repos.resolve(record.Repo)
				if err == nil && other == identity {
					matching[record.Repo] = true
				}
			}
		}
	}
	previous := 0
	var existing PRRecord
	for _, line := range strings.Split(string(data), "\n") {
		if record, ok := parsePR(line); ok && matching[record.Repo] && record.Number == removeNumber {
			previous++
			existing = record
		}
	}
	if previous > 1 && add != nil {
		return fmt.Errorf("multiple enlisted rows for repository %q PR %s; reconcile duplicate rows first", removeRepo, removeNumber)
	}
	if add != nil {
		if previous == 1 {
			// Keep the legacy key for merge markers and older clients. Resolve
			// omitted metadata under the lock, before considering URL defaults.
			add.Repo = existing.Repo
			if add.Title == "" {
				add.Title = existing.Title
			}
			if add.URL == "" {
				add.URL = existing.URL
			}
		}
		if add.URL == "" && identity != nil {
			add.URL = prURL(identity.remote, add.Number)
		}
	}
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		if record, ok := parsePR(line); ok && matching[record.Repo] && record.Number == removeNumber {
			continue
		}
		if repo, number, _, _, ok := parseMergedHead(line); ok && matching[repo] && number == removeNumber && add == nil {
			continue
		}
		if repo, number, _, _, ok := parseMergedPR(line); ok && matching[repo] && number == removeNumber && add == nil {
			continue
		}
		if repo, number, _, _, ok := parseDeclinedPR(line); ok && matching[repo] && number == removeNumber && add == nil {
			continue
		}
		fmt.Fprintln(&out, line)
	}
	if add != nil {
		fmt.Fprintln(&out, formatPR(*add))
	}
	return c.writePRFile(out.Bytes())
}

func (c *Context) writePRFile(data []byte) error {
	tmp, err := os.CreateTemp(c.Collection, ".wtc-prs.tmp-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Phase 0 still writes the legacy name. The caller read the selected
	// registry under the shared lock, so this carries every alternate row
	// forward before the legacy output becomes authoritative.
	return os.Rename(tmp.Name(), filepath.Join(c.Collection, ".wtc-prs"))
}

// recordMergedPRs records settled merge facts and an optional observed PR head.
// The head observation is not guaranteed to be the immutable pre-merge head.
// Extra comment rows keep older readers parsing the ordinary PR rows.
func (c *Context) recordMergedPRs(records []PRRecord, details []statusPRDetail) (int, error) {
	if len(records) != len(details) {
		return 0, fmt.Errorf("PR records and details differ in length")
	}
	updates := map[string]statusPRDetail{}
	for i, record := range records {
		detail := details[i]
		if record.MergedOn != "" || detail.State != "MERGED" || detail.ChecksUnsettled {
			continue
		}
		if _, err := time.Parse(time.RFC3339, detail.MergedOn); err != nil {
			continue
		}
		switch detail.Checks {
		case "PENDING":
			// Checks can still settle after the merge; retry later.
			continue
		case "SUCCESS", "FAILURE", "NONE":
		default:
			continue
		}
		updates[prRecordKey(record.Repo, record.Number)] = detail
	}
	if len(updates) == 0 {
		return 0, nil
	}
	count := 0
	err := c.withPRLock(func() error {
		var err error
		count, err = c.recordMergedPRsLocked(records, updates)
		return err
	})
	return count, err
}

func (c *Context) recordMergedPRsLocked(records []PRRecord, updates map[string]statusPRDetail) (int, error) {
	path, err := c.prInput()
	if err != nil {
		return 0, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() {
		return 0, fmt.Errorf("refusing non-regular PR file: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		if repo, number, _, _, ok := parseMergedPR(line); ok {
			delete(updates, prRecordKey(repo, number))
		}
	}
	if len(updates) == 0 {
		return 0, nil
	}
	var out bytes.Buffer
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		if record, ok := parsePR(line); ok {
			key := prRecordKey(record.Repo, record.Number)
			if detail, update := updates[key]; update {
				if detail.Title != "" {
					record.Title = cleanPRField(detail.Title)
				}
				fmt.Fprintln(&out, formatPR(record))
				seen[key] = true
				continue
			}
		}
		if repo, number, _, _, ok := parseMergedPR(line); ok && updates[prRecordKey(repo, number)].State == "MERGED" {
			continue
		}
		if repo, number, _, _, ok := parseMergedHead(line); ok && updates[prRecordKey(repo, number)].State == "MERGED" {
			continue
		}
		if repo, number, _, _, ok := parseMergeCommit(line); ok && updates[prRecordKey(repo, number)].State == "MERGED" {
			continue
		}
		fmt.Fprintln(&out, line)
	}
	count := 0
	for _, record := range records {
		key := prRecordKey(record.Repo, record.Number)
		if detail, ok := updates[key]; ok && seen[key] {
			fmt.Fprintf(&out, "# merged-pr %s %s %s %s\n", record.Repo, record.Number, detail.MergedOn, detail.Checks)
			if statusCommitHash.MatchString(detail.MergeCommit) {
				line := fmt.Sprintf("# merged-commit %s %s %s", record.Repo, record.Number, detail.MergeCommit)
				if detail.Base != "" && !strings.ContainsAny(detail.Base, " \t\r\n") {
					line += " " + detail.Base
				}
				fmt.Fprintln(&out, line)
				if statusCommitHash.MatchString(detail.HeadCommit) {
					fmt.Fprintf(&out, "# merged-head %s %s %s %s\n", record.Repo, record.Number, detail.MergeCommit, detail.HeadCommit)
				}
			}
			count++
		}
	}
	if count == 0 {
		return 0, nil
	}
	return count, c.writePRFile(out.Bytes())
}

// statusDeclineUpdate is one change to a recorded Bitbucket decline: a new
// record (DeclinedOn set) or its removal once the PR is seen open or merged.
type statusDeclineUpdate struct {
	DeclinedOn, DeclinedAt string
}

// recordDeclinedPRs records Bitbucket declines observed by this pass, so
// background refresh does not read them again, and removes the record of a
// PR observed open or merged since. bitbucket[i] reports whether records[i] is a
// Bitbucket PR; other forges are left alone (a GitHub close is not read from
// an open list here).
func (c *Context) recordDeclinedPRs(records []PRRecord, details []statusPRDetail, bitbucket []bool) (recorded, cleared int, err error) {
	if len(records) != len(details) || len(records) != len(bitbucket) {
		return 0, 0, fmt.Errorf("PR records and details differ in length")
	}
	updates := map[string]statusDeclineUpdate{}
	for i, record := range records {
		detail := details[i]
		if !bitbucket[i] || record.MergedOn != "" || statusObservationUnavailable(detail.Freshness) || detail.StateSource == "registry" {
			continue
		}
		key := prRecordKey(record.Repo, record.Number)
		switch detail.State {
		case "DECLINED":
			on, at := detail.ClosedOn, detail.ClosedAt
			if _, err := time.Parse(time.RFC3339, on); err != nil || strings.ContainsAny(on, " \t\r\n") {
				continue
			}
			if _, err := time.Parse(time.RFC3339, at); err != nil || strings.ContainsAny(at, " \t\r\n") {
				at = ""
			}
			if on != record.DeclinedOn || at != "" && at != record.DeclinedAt {
				updates[key] = statusDeclineUpdate{DeclinedOn: on, DeclinedAt: at}
			}
		case "OPEN", "DRAFT", "MERGED":
			// Open again, or merged after a reopen (seen by an explicit
			// refresh): the recorded decline no longer holds.
			if record.DeclinedOn != "" {
				updates[key] = statusDeclineUpdate{}
			}
		}
	}
	if len(updates) == 0 {
		return 0, 0, nil
	}
	err = c.withPRLock(func() error {
		var err error
		recorded, cleared, err = c.recordDeclinedPRsLocked(updates)
		return err
	})
	return recorded, cleared, err
}

func (c *Context) recordDeclinedPRsLocked(updates map[string]statusDeclineUpdate) (recorded, cleared int, err error) {
	path, err := c.prInput()
	if err != nil {
		return 0, 0, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return 0, 0, err
	}
	if !info.Mode().IsRegular() {
		return 0, 0, fmt.Errorf("refusing non-regular PR file: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, err
	}
	var out bytes.Buffer
	rows := map[string]PRRecord{}
	var order []string
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		if repo, number, _, _, ok := parseDeclinedPR(line); ok {
			if update, change := updates[prRecordKey(repo, number)]; change {
				if update.DeclinedOn == "" {
					cleared++
				}
				continue
			}
		}
		if record, ok := parsePR(line); ok {
			key := prRecordKey(record.Repo, record.Number)
			if _, seen := rows[key]; !seen {
				order = append(order, key)
			}
			rows[key] = record
		}
		fmt.Fprintln(&out, line)
	}
	for _, key := range order {
		update, change := updates[key]
		if !change || update.DeclinedOn == "" {
			continue
		}
		record := rows[key]
		line := fmt.Sprintf("# declined-pr %s %s %s", record.Repo, record.Number, update.DeclinedOn)
		if update.DeclinedAt != "" {
			line += " " + update.DeclinedAt
		}
		fmt.Fprintln(&out, line)
		recorded++
	}
	if recorded == 0 && cleared == 0 {
		return 0, 0, nil
	}
	return recorded, cleared, c.writePRFile(out.Bytes())
}

func (c *Context) EnlistPR(r PRRecord) (PRRecord, error) {
	r.Repo = cleanPRField(r.Repo)
	r.Number = cleanPRField(r.Number)
	r.Branch = cleanPRField(r.Branch)
	r.URL = cleanPRField(r.URL)
	r.Title = cleanPRField(r.Title)
	if err := ValidatePRIdentity(r.Repo, r.Number); err != nil {
		return r, err
	}
	repo, err := c.prRepositories(r.Repo).resolve(r.Repo)
	if err != nil {
		return r, err
	}
	dir := ""
	if repo != nil {
		r.Repo, dir = repo.name, repo.dir()
	}
	if r.Branch == "" && dir != "" {
		command := exec.Command("git", "-C", filepath.Join(c.Collection, dir), "symbolic-ref", "-q", "--short", "HEAD")
		if b, err := command.Output(); err == nil {
			r.Branch = strings.TrimSpace(string(b))
		}
	}
	err = c.withPRLock(func() error {
		if err := c.rewritePRsLocked(r.Repo, r.Number, &r); err != nil {
			return err
		}
		// Return the persisted row, including retained merge evidence, before
		// another writer can replace it.
		records, err := c.ListPRs()
		if err != nil {
			return err
		}
		for _, record := range records {
			if record.Repo == r.Repo && record.Number == r.Number {
				r = record
				return nil
			}
		}
		return fmt.Errorf("enlisted PR was not present after writing")
	})
	return r, err
}

func (c *Context) UnlistPR(repo, number string) error {
	if err := ValidatePRIdentity(repo, number); err != nil {
		return err
	}
	return c.rewritePRs(repo, number, nil)
}

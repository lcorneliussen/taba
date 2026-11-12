package wtc

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

type status319Event struct {
	Kind     string
	PID      int
	Snapshot *StatusSnapshot
	Markdown string
	Error    string
	Progress []string
}

// Test-binary children exercise the production collector and CLI transport;
// they do not implement a second refresh or admission algorithm.
func TestStatus319RefreshProcess(t *testing.T) {
	mode := os.Getenv("WTC_TEST_319_CHILD")
	if mode == "" {
		return
	}
	if mode == "legacy" {
		status319ReleasedConsumer()
		os.Exit(0)
	}
	if mode == "bb" {
		args := os.Args
		for len(args) > 0 && args[0] != "--" {
			args = args[1:]
		}
		if len(args) < 4 {
			os.Exit(2)
		}
		action, number := args[2], args[3]
		user, pass := bitbucketCredentials()
		if user == "" || pass == "" || os.Getenv("WTC_TEST_319_NO_AUTH") == "1" {
			fmt.Fprintln(os.Stderr, "not logged in")
			os.Exit(1)
		}
		request, _ := http.NewRequest(http.MethodGet, os.Getenv("WTC_TEST_319_API")+"/cli/"+action+"/"+number, nil)
		request.SetBasicAuth(user, pass)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		raw, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if response.StatusCode != 200 {
			fmt.Fprintf(os.Stderr, "HTTP %d Retry-After: %s", response.StatusCode, response.Header.Get("Retry-After"))
			os.Exit(1)
		}
		// Model the real CLI's large stdout behavior. forgeOutput must supply a file.
		info, _ := os.Stdout.Stat()
		if !info.Mode().IsRegular() && len(raw) > 65536 {
			raw = raw[:65536]
		}
		_, _ = os.Stdout.Write(raw)
		os.Exit(0)
	}
	statusBBAPI = os.Getenv("WTC_TEST_319_API")
	// Advance allowance opportunities from the parent rather than sleeping.
	interval := os.Getenv("WTC_TEST_319_INTERVAL")
	if interval == "" {
		interval = "3600"
	}
	os.Setenv("WTC_FORGE_MIN_INTERVAL", interval)
	os.Setenv("WTC_FORGE_FULL_REFRESH", "0")
	os.Setenv("WTC_STATUS_RETURN_MERGED", "true")
	var output sync.Mutex
	emit := func(event status319Event) {
		output.Lock()
		defer output.Unlock()
		_ = json.NewEncoder(os.Stdout).Encode(event)
	}
	c, err := OpenCollection(os.Getenv("WTC_TEST_319_COLLECTION"))
	if err != nil {
		emit(status319Event{Kind: "final", Error: err.Error()})
		os.Exit(1)
	}
	var progress []string
	c.StatusProgress = func(message string) {
		output.Lock()
		progress = append(progress, message)
		output.Unlock()
		if strings.HasPrefix(message, "Collected pull request facts") || strings.HasPrefix(message, "Checked live pull requests") {
			emit(status319Event{Kind: "progress"})
		}
	}
	emit(status319Event{Kind: "ready", PID: os.Getpid()})
	snapshot, fetches, err := c.StatusLiveSnapshot(false, true)
	if fetches.Attempted != 0 || fetches.Failed != 0 {
		err = fmt.Errorf("collector attempted a Git network refresh: %+v", fetches)
	}
	event := status319Event{Kind: "final", PID: os.Getpid(), Snapshot: &snapshot, Progress: progress}
	if err != nil {
		event.Error = err.Error()
	} else {
		// Render the persisted production snapshot, not a manually injected row.
		saved, readErr := c.ReadStatusSnapshot()
		if readErr != nil {
			event.Error = readErr.Error()
		} else {
			event.Markdown = saved.Markdown()
		}
	}
	emit(event)
	if event.Error != "" {
		os.Exit(1)
	}
	os.Exit(0)
}

type status319Process struct {
	command *exec.Cmd
	events  chan status319Event
	done    chan error
	stderr  bytes.Buffer
}

func status319Start(t *testing.T, c *Context, api, bin string, noAuth bool) *status319Process {
	return status319StartMode(t, c, api, bin, noAuth, "refresh")
}

func status319StartMode(t *testing.T, c *Context, api, bin string, noAuth bool, mode string) *status319Process {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestStatus319RefreshProcess$")
	command.Env = append(os.Environ(), "WTC_TEST_319_CHILD="+mode, "WTC_TEST_319_COLLECTION="+c.Collection, "WTC_TEST_319_API="+api,
		"WTC_TEST_319_EXE="+os.Args[0], "PATH="+bin+string(os.PathListSeparator)+"/usr/bin:/bin", "WTC_CONFIG_ROOT="+c.ConfigRoot,
		"TABA_DEV_BUILD=off", "WTC_DEV_BUILD=off", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=protocol.https.allow", "GIT_CONFIG_VALUE_0=never", "WTC_TEST_319_NO_AUTH="+map[bool]string{false: "0", true: "1"}[noAuth])
	child := &status319Process{command: command, events: make(chan status319Event, 64), done: make(chan error, 1)}
	command.Stderr = &child.stderr
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cancel)
	go func() {
		defer close(child.events)
		decoder := json.NewDecoder(bufio.NewReader(stdout))
		for {
			var event status319Event
			if err := decoder.Decode(&event); err != nil {
				break
			}
			child.events <- event
		}
		child.done <- command.Wait()
	}()
	return child
}

func (p *status319Process) until(t *testing.T, kind string) status319Event {
	t.Helper()
	for {
		event, ok := <-p.events
		if !ok {
			t.Fatalf("refresh child stopped before %s: %s", kind, p.stderr.String())
		}
		if event.Kind == kind {
			if kind == "final" {
				if err := <-p.done; err != nil || event.Error != "" {
					t.Fatalf("refresh failed: %v %s %s", err, event.Error, p.stderr.String())
				}
			}
			return event
		}
		if event.Kind == "final" {
			t.Fatalf("refresh finished before %s: %s", kind, event.Error)
		}
	}
}

func status319Refresh(t *testing.T, c *Context, api, bin string, noAuth bool) status319Event {
	t.Helper()
	p := status319Start(t, c, api, bin, noAuth)
	p.until(t, "ready")
	return p.until(t, "final")
}

type status319Barrier struct {
	entered  chan struct{}
	release  chan struct{}
	once     sync.Once
	finished sync.Once
}

func (b *status319Barrier) unblock() { b.finished.Do(func() { close(b.release) }) }

type status319Wire struct {
	mu                sync.Mutex
	merged            bool
	mode              string
	head, base, merge string
	calls             []string
	barrier           *status319Barrier
}

func (s *status319Wire) item(number int, merged bool) map[string]any {
	target := "beta"
	if number == 99 {
		target = "main"
	}
	branch := map[int]string{7: "topic-open", 8: "topic-draft", 9: "still-draft", 99: "pressure-topic"}[number]
	item := map[string]any{"id": number, "state": "OPEN", "draft": number == 8 || number == 9, "title": fmt.Sprintf("Synthetic %d", number),
		"updated_on": "2026-11-04T10:00:00Z", "source": map[string]any{"branch": map[string]string{"name": branch}},
		"destination": map[string]any{"branch": map[string]string{"name": target}, "commit": map[string]string{"hash": s.base}}}
	if number != 99 {
		item["source"].(map[string]any)["commit"] = map[string]string{"hash": s.head}
	}
	if merged && number != 9 && number != 99 {
		item["state"] = "MERGED"
		item["draft"] = true
		item["merge_commit"] = map[string]string{"hash": s.merge}
		// bb view can omit merged_on: status must obtain the real activity time.
		item["description"] = strings.Repeat("synthetic ", 10000)
	}
	return item
}

func (s *status319Wire) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	s.mu.Lock()
	merged, mode, barrier := s.merged, s.mode, s.barrier
	path := r.URL.Path
	s.calls = append(s.calls, path)
	var body any
	switch {
	case strings.HasPrefix(path, "/cli/view/"):
		number, _ := strconv.Atoi(filepath.Base(path))
		body = s.item(number, merged)
		if mode == "wrong-identity" {
			body = s.item(700, true)
		}
	case strings.HasPrefix(path, "/cli/activity/"):
		body = map[string]any{"activities": []any{map[string]any{"description": strings.Repeat("synthetic ", 10000), "update": map[string]string{"state": "MERGED", "date": "2026-11-04T10:01:00.123456789Z"}}}}
	case strings.Contains(path, "/pullrequests"):
		values := []any{}
		if strings.Contains(path, "/pressure/") {
			values = append(values, s.item(99, false))
		} else {
			if !merged || mode == "old-list" {
				oldOpen, oldDraft := s.item(7, false), s.item(8, false)
				if mode == "old-list" {
					oldOpen["comment_count"] = 99
					oldDraft["comment_count"] = 99
				}
				values = append(values, oldOpen, oldDraft)
			}
			values = append(values, s.item(9, false))
		}
		body = map[string]any{"values": values}
	case strings.Contains(path, "/pipelines/"):
		body = map[string]any{"values": []any{map[string]any{"build_number": 11, "state": map[string]any{"name": "COMPLETED", "result": map[string]string{"name": "SUCCESSFUL"}},
			"target": map[string]any{"ref_name": "main", "selector": map[string]string{"type": "branches"}, "commit": map[string]string{"hash": s.base}}}}}
	default:
		body = map[string]any{"values": []any{}}
	}
	s.mu.Unlock()
	if barrier != nil && ((mode == "delay-view" && strings.HasPrefix(path, "/cli/view/")) || (mode == "old-list" && strings.Contains(path, "/widget/pullrequests"))) {
		barrier.once.Do(func() { close(barrier.entered) })
		<-barrier.release
	}
	if strings.HasPrefix(path, "/cli/view/") {
		if mode == "429" {
			w.Header().Set("Retry-After", "3600")
			w.WriteHeader(429)
			_, _ = w.Write([]byte("synthetic rate limit"))
			return
		}
		if mode == "malformed" {
			_, _ = w.Write([]byte("not-json"))
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func (s *status319Wire) count() int      { s.mu.Lock(); defer s.mu.Unlock(); return len(s.calls) }
func (s *status319Wire) set(mode string) { s.mu.Lock(); s.mode = mode; s.barrier = nil; s.mu.Unlock() }
func (s *status319Wire) delay(mode string) *status319Barrier {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mode = mode
	s.barrier = &status319Barrier{entered: make(chan struct{}), release: make(chan struct{})}
	return s.barrier
}

func status319Collection(t *testing.T, slug string) (*Context, string, string) {
	t.Helper()
	c := newWorkspaceFixture(t)
	owner := filepath.Join(c.Workspace, ".bare", "widget.git")
	worktree := filepath.Join(c.Collection, "widget")
	branch := "topic-draft"
	if slug == "pressure" {
		branch = "pressure-topic"
	}
	fixtureGit(t, "--git-dir="+owner, "worktree", "add", "-qb", branch, worktree, "origin/main")
	base := fixtureGit(t, "-C", worktree, "rev-parse", "HEAD")
	fixtureFile(t, filepath.Join(worktree, "feature.txt"), "synthetic feature\n", 0644)
	fixtureGit(t, "-C", worktree, "add", "feature.txt")
	fixtureGit(t, "-C", worktree, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "synthetic feature")
	head := fixtureGit(t, "-C", worktree, "rev-parse", "HEAD")
	source := filepath.Join(c.Workspace, "source-widget")
	if slug == "widget" {
		fixtureGit(t, "-C", source, "branch", "beta", "main")
	}
	for _, name := range []string{branch, "topic-open", "still-draft"} {
		fixtureGit(t, "-C", worktree, "push", "-q", source, "HEAD:refs/heads/"+name)
	}
	fixtureGit(t, "--git-dir="+owner, "fetch", "-q", "origin", "+refs/heads/*:refs/remotes/origin/*")
	fixtureGit(t, "-C", worktree, "branch", "--set-upstream-to=origin/"+branch, branch)
	registry := filepath.Join(c.Harness, ".harness-repos.yml")
	raw, _ := os.ReadFile(registry)
	configuration := strings.Replace(string(raw), "remote: "+source, "remote: https://bitbucket.org/example/"+slug+".git", 1)
	if slug == "widget" {
		configuration = strings.Replace(configuration, "remote: https://bitbucket.org/example/widget.git\n    default_ref: origin/main", "remote: https://bitbucket.org/example/widget.git\n    default_ref: origin/beta", 1)
	}
	fixtureFile(t, registry, configuration, 0644)
	fixtureGit(t, "--git-dir="+owner, "remote", "set-url", "origin", "https://bitbucket.org/example/"+slug+".git")
	numbers := []string{"7", "8", "9"}
	branches := []string{"topic-open", "topic-draft", "still-draft"}
	if slug == "pressure" {
		numbers = []string{"99"}
		branches = []string{branch}
	}
	for i, number := range numbers {
		if _, err := c.EnlistPR(PRRecord{Repo: "widget", Number: number, Branch: branches[i], URL: "https://bitbucket.org/example/" + slug + "/pull-requests/" + number, Title: "Synthetic " + number}); err != nil {
			t.Fatal(err)
		}
	}
	return c, head, base
}

func status319Age(t *testing.T, kind, slug, key string) {
	t.Helper()
	path := statusForgeEntryPath(kind, "bitbucket.org", slug, key)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return
	}
	past := time.Now().Add(-3 * time.Minute)
	if err := os.Chtimes(path, past, past); err != nil {
		t.Fatal(err)
	}
}

func status319Opportunity(t *testing.T) {
	t.Helper()
	// Model exactly one elapsed allowance interval. No queue order is injected.
	path := statusForgeStatePath("budget", "bitbucket.org")
	fixtureFile(t, path, fmt.Sprintf("0 %d\n", time.Now().Add(-time.Hour).UnixNano()), 0600)
}

func status319AssertOld(t *testing.T, event status319Event) {
	t.Helper()
	if event.Snapshot == nil {
		t.Fatal("missing producer snapshot")
	}
	if len(event.Progress) == 0 {
		t.Error("producer progress was not captured")
	}
	for _, message := range event.Progress {
		if strings.Contains(message, "live pull requests") {
			t.Errorf("cached/deferred facts advertised as live checks: %s", message)
		}
	}
	for _, number := range []string{"7", "8"} {
		found := false
		for _, pr := range event.Snapshot.PRs {
			if pr.Number != number {
				continue
			}
			found = true
			want := "OPEN"
			if number == "8" {
				want = "DRAFT"
			}
			raw, _ := json.Marshal(pr)
			var fields map[string]any
			_ = json.Unmarshal(raw, &fields)
			freshness, _ := fields["freshness"].(string)
			when, _ := fields["observed_at"].(string)
			if pr.State != want || (freshness != "stale" && freshness != "deferred" && freshness != "unavailable") || when == "" || fields["observation_source"] != "detail-cache" {
				t.Errorf("old fact claimed current or merged: %s", raw)
			}
			observed, err := time.Parse(time.RFC3339Nano, when)
			if err != nil || time.Since(observed) < 2*time.Minute {
				t.Errorf("observation age lost: %+v", pr)
			}
		}
		if !found {
			t.Fatalf("missing old PR %s", number)
		}
	}
	for _, pr := range event.Snapshot.PRs {
		if pr.Number == "9" && (pr.State != "DRAFT" || !pr.Draft) {
			t.Errorf("genuine draft changed during unavailable reads: %+v", pr)
		}
	}
	if !strings.Contains(event.Markdown, "forge observation; observed") || strings.Contains(event.Markdown, "_merged_") {
		t.Errorf("native stale rendering: %s", event.Markdown)
	}
	for _, repo := range event.Snapshot.Repos {
		if repo.Dir == "widget" && (repo.Branch != "topic-draft" || repo.Next != nil || repo.Choices != nil) {
			t.Errorf("unconfirmed observation authorized branch action: %+v", repo)
		}
	}
}

func status319AssertMerged(t *testing.T, event status319Event) {
	t.Helper()
	for _, number := range []string{"7", "8", "9"} {
		found := false
		for _, pr := range event.Snapshot.PRs {
			if pr.Number != number {
				continue
			}
			found = true
			if number == "9" {
				if pr.State != "DRAFT" || !pr.Draft {
					t.Fatalf("genuine draft changed: %+v", pr)
				}
			} else if pr.State != "MERGED" || pr.Draft {
				t.Fatalf("confirmed merge regressed: %+v", pr)
			}
		}
		if !found {
			t.Fatalf("missing PR %s", number)
		}
	}
	if !strings.Contains(event.Markdown, "_merged_") && !strings.Contains(event.Markdown, " merged 2026-11-04T10:01:00Z") {
		t.Fatalf("native merged rendering missing: %s", event.Markdown)
	}
}

func TestStatus319ProductionRefreshTransitionsAcrossProcesses(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	statusHideBitbucketLogin(t)
	primary, head, base := status319Collection(t, "widget")
	pressure, _, _ := status319Collection(t, "pressure")
	bin := t.TempDir()
	fixtureFile(t, filepath.Join(bin, "bb"), "#!/bin/sh\nWTC_TEST_319_CHILD=bb exec \"$WTC_TEST_319_EXE\" -test.run=^TestStatus319RefreshProcess$ -- \"$@\"\n", 0700)
	t.Setenv("BB_API_USER", "synthetic")
	t.Setenv("BB_API_PASS", "synthetic")
	wire := &status319Wire{head: head, base: base}
	server := httptest.NewServer(wire)
	defer server.Close()
	first := status319Refresh(t, primary, server.URL, bin, false)
	if len(first.Snapshot.PRs) != 3 {
		t.Fatal(first.Snapshot.PRs)
	}
	for _, pr := range first.Snapshot.PRs {
		want := "DRAFT"
		if pr.Number == "7" {
			want = "OPEN"
		}
		if pr.State != want {
			t.Fatalf("initial provider truth lost: %+v", pr)
		}
	}
	status319Opportunity(t)
	pressureFirst := status319Refresh(t, pressure, server.URL, bin, false)
	if first.PID == 0 || pressureFirst.PID == 0 || first.PID == pressureFirst.PID {
		t.Fatal("collectors did not run in independent processes")
	}
	// A second pressure refresh uses the cached list and must reach the real build provider.
	status319Opportunity(t)
	status319Refresh(t, pressure, server.URL, bin, false)
	wire.mu.Lock()
	pressureBuild := false
	for _, path := range wire.calls {
		if strings.Contains(path, "/pressure/pipelines/") {
			pressureBuild = true
		}
	}
	wire.mu.Unlock()
	if !pressureBuild {
		t.Fatal("pressure process never exercised the production build path")
	}
	pressureStep := func() status319Event {
		status319Age(t, "openprs", "example/pressure", "open")
		status319Age(t, "pipelines", "example/pressure", "recent")
		status319Age(t, "build", "example/pressure", "main")
		return status319Refresh(t, pressure, server.URL, bin, false)
	}
	// Merge only in the local synthetic Git remote, then remove both source branches.
	source := filepath.Join(primary.Workspace, "source-widget")
	fixtureGit(t, "-C", source, "switch", "-q", "beta")
	fixtureGit(t, "-C", source, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "merge", "--no-ff", "topic-draft", "-m", "synthetic merge")
	merge := fixtureGit(t, "-C", source, "rev-parse", "HEAD")
	fixtureGit(t, "-C", source, "branch", "-D", "topic-draft", "topic-open")
	owner := filepath.Join(primary.Workspace, ".bare", "widget.git")
	fixtureGit(t, "--git-dir="+owner, "fetch", "-q", "--prune", source, "+refs/heads/*:refs/remotes/origin/*")
	wire.mu.Lock()
	wire.merged = true
	wire.merge = merge
	wire.mu.Unlock()
	for _, number := range []string{"7", "8"} {
		status319Age(t, "pr", "example/widget", number)
	}
	status319Age(t, "openprs", "example/widget", "open")
	status319Opportunity(t)
	deferred := status319Refresh(t, primary, server.URL, bin, false)
	status319AssertOld(t, deferred)
	// Each failed attempt is a fresh process restarting from the persisted stale cache.
	for _, mode := range []string{"malformed", "wrong-identity", "429", "auth"} {
		wire.set(mode)
		status319Opportunity(t)
		before := wire.count()
		pressureStep()
		event := status319Refresh(t, primary, server.URL, bin, mode == "auth")
		status319AssertOld(t, event)
		if wire.count()-before > 1 {
			t.Fatalf("%s exceeded one admitted provider request", mode)
		}
		if mode == "429" {
			wire.set("")
			status319Opportunity(t)
			beforeCooling := wire.count()
			status319AssertOld(t, status319Refresh(t, primary, server.URL, bin, false))
			if wire.count() != beforeCooling {
				t.Fatal("cooldown dispatched a provider read")
			}
			fixtureFile(t, statusForgeStatePath("cool", "bitbucket.org"), "0", 0600)
		}
	}
	recordsBefore, _ := primary.ListPRs()
	for _, record := range recordsBefore {
		if record.Number != "9" && record.MergedOn != "" {
			t.Fatal("unavailable reads recorded a merge")
		}
	}
	// One-shot processes exited with reservations. Advance only their expiry,
	// then prove a surviving ordinary producer can perform a real provider read.
	queuePath := statusForgeStatePath("pending", "bitbucket.org")
	raw, _ := os.ReadFile(queuePath)
	var reservations []map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &reservations); err != nil {
			t.Fatal(err)
		}
	}
	for _, entry := range reservations {
		entry["seen"] = time.Now().Add(-6 * time.Minute).UTC().Format(time.RFC3339Nano)
	}
	raw, _ = json.Marshal(reservations)
	fixtureFile(t, queuePath, string(raw), 0600)
	status319Age(t, "openprs", "example/pressure", "open")
	status319Opportunity(t)
	before := wire.count()
	pressureStep()
	if wire.count()-before != 1 {
		t.Fatal("ordinary process did not recover after abandoned reservation expiry")
	}
	wire.set("")
	status319AssertOld(t, status319Refresh(t, primary, server.URL, bin, false))
	// Hold the first admitted confirmation while another real producer enters.
	// Both must share its response rather than spending a second token.
	barrier := wire.delay("delay-view")
	defer barrier.unblock()
	status319Opportunity(t)
	before = wire.count()
	a := status319Start(t, primary, server.URL, bin, false)
	a.until(t, "ready")
	select {
	case <-barrier.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("confirmation did not reach barrier")
	}
	b := status319Start(t, primary, server.URL, bin, false)
	b.until(t, "ready")
	b.until(t, "progress")
	barrier.unblock()
	aResult, bResult := a.until(t, "final"), b.until(t, "final")
	if wire.count()-before != 1 {
		t.Fatal("concurrent producers exceeded one confirmation opportunity")
	}
	for _, event := range []status319Event{aResult, bResult} {
		merged := 0
		for _, pr := range event.Snapshot.PRs {
			if pr.Number != "9" && pr.State == "MERGED" {
				merged++
			}
		}
		if merged != 1 {
			t.Fatalf("coalesced confirmation lost its result: %+v", event.Snapshot.PRs)
		}
	}
	wire.set("")
	status319Opportunity(t)
	before = wire.count()
	pressureStep()
	merged := status319Refresh(t, primary, server.URL, bin, false)
	status319AssertMerged(t, merged)
	if wire.count()-before != 1 {
		t.Fatal("remaining PR did not confirm within the second opportunity")
	}
	// Known state survives active-cache expiry while ancillary reads are cooling.
	for _, number := range []string{"7", "8"} {
		status319Age(t, "pr", "example/widget", number)
	}
	status319Age(t, "openprs", "example/widget", "open")
	fixtureFile(t, statusForgeStatePath("cool", "bitbucket.org"), fmt.Sprint(time.Now().Add(time.Hour).UnixNano()), 0600)
	status319Opportunity(t)
	before = wire.count()
	cooling := status319Refresh(t, primary, server.URL, bin, false)
	status319AssertMerged(t, cooling)
	if wire.count() != before || !strings.Contains(cooling.Markdown, "deferred forge observation") {
		t.Fatal("cooling lost known merge state or dispatched a follow-up read")
	}
	for _, pr := range cooling.Snapshot.PRs {
		if pr.Number != "9" && (pr.MergedOn != nil || pr.Base != "beta") {
			t.Fatalf("missing activity/non-default base misrepresented: %+v", pr)
		}
	}
	fixtureFile(t, statusForgeStatePath("cool", "bitbucket.org"), "0", 0600)
	// A delayed older open list is published after the newer merge confirmations.
	status319Age(t, "openprs", "example/widget", "open")
	barrier = wire.delay("old-list")
	defer barrier.unblock()
	status319Opportunity(t)
	before = wire.count()
	old := status319Start(t, primary, server.URL, bin, false)
	old.until(t, "ready")
	select {
	case <-barrier.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("old list did not reach barrier")
	}
	pressureStep()
	barrier.unblock()
	status319AssertMerged(t, old.until(t, "final"))
	if wire.count()-before != 1 {
		t.Fatal("old-list refresh exceeded the allowance")
	}
	wire.set("")
	// Missing merged_on is settled through separately admitted real activity reads.
	status319Age(t, "openprs", "example/widget", "open")
	for opportunity := 0; opportunity < 7; opportunity++ {
		status319Opportunity(t)
		merged = status319Refresh(t, primary, server.URL, bin, false)
		status319AssertMerged(t, merged)
		records, _ := primary.ListPRs()
		settled := 0
		for _, record := range records {
			if record.Number != "9" && record.MergedOn != "" {
				settled++
			}
		}
		if settled == 2 {
			break
		}
	}
	records, err := primary.ListPRs()
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if record.Number != "9" && record.MergedOn != "2026-11-04T10:01:00Z" {
			t.Fatalf("merge activity never settled: %+v", record)
		}
	}
}

// This independent fixture implements the released two-field budget protocol,
// including its existing flock, and deliberately knows no pending-queue API.
func status319ReleasedConsumer() {
	digest := sha256.Sum256([]byte("budget\x00bitbucket.org\x00\x00"))
	dir := filepath.Join(os.TempDir(), "wtc-status-"+strconv.Itoa(os.Getuid()))
	path := filepath.Join(dir, "budget-"+hex.EncodeToString(digest[:])+".json")
	_ = json.NewEncoder(os.Stdout).Encode(status319Event{Kind: "ready", PID: os.Getpid()})
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	operation := 0
	for range tick.C {
		lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			os.Exit(2)
		}
		if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
			os.Exit(2)
		}
		now := time.Now()
		tokens, updated := 1, now
		if raw, err := os.ReadFile(path); err == nil {
			fields := strings.Fields(string(raw))
			if len(fields) == 2 {
				if n, err := strconv.Atoi(fields[0]); err == nil {
					tokens = n
				}
				if n, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
					updated = time.Unix(0, n)
				}
			}
		}
		if now.After(updated) {
			gained := int(now.Sub(updated) / time.Second)
			if gained > 0 {
				tokens += gained
				updated = updated.Add(time.Duration(gained) * time.Second)
			}
		}
		if tokens > 1 {
			tokens = 1
			updated = now
		}
		admitted := tokens > 0
		if admitted {
			err = os.WriteFile(path, []byte(fmt.Sprintf("%d %d\n", tokens-1, updated.UnixNano())), 0600)
			admitted = err == nil
		}
		_ = unix.Flock(int(lock.Fd()), unix.LOCK_UN)
		_ = lock.Close()
		if !admitted {
			continue
		}
		endpoint := "/repositories/example/pressure/pullrequests"
		if operation%2 == 1 {
			endpoint = "/repositories/example/pressure/pipelines/"
		}
		operation++
		response, err := http.Get(os.Getenv("WTC_TEST_319_API") + endpoint)
		if err != nil {
			os.Exit(2)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		response.Body.Close()
		_ = json.NewEncoder(os.Stdout).Encode(status319Event{Kind: "admitted", PID: os.Getpid()})
	}
}

func TestStatus319RefreshCoexistsWithReleasedBudgetConsumer(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	statusHideBitbucketLogin(t)
	primary, head, base := status319Collection(t, "widget")
	fixtureFile(t, primary.PRFile(), "widget 8 topic-draft https://bitbucket.org/example/widget/pull-requests/8 Synthetic 8\n", 0600)
	bin := t.TempDir()
	fixtureFile(t, filepath.Join(bin, "bb"), "#!/bin/sh\nWTC_TEST_319_CHILD=bb exec \"$WTC_TEST_319_EXE\" -test.run=^TestStatus319RefreshProcess$ -- \"$@\"\n", 0700)
	t.Setenv("BB_API_USER", "synthetic")
	t.Setenv("BB_API_PASS", "synthetic")
	wire := &status319Wire{head: head, base: base}
	server := httptest.NewServer(wire)
	defer server.Close()
	status319Refresh(t, primary, server.URL, bin, false)
	source := filepath.Join(primary.Workspace, "source-widget")
	fixtureGit(t, "-C", source, "switch", "-q", "beta")
	fixtureGit(t, "-C", source, "-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "merge", "--no-ff", "topic-draft", "-m", "synthetic merge")
	merge := fixtureGit(t, "-C", source, "rev-parse", "HEAD")
	fixtureGit(t, "-C", source, "branch", "-D", "topic-draft", "topic-open")
	fixtureGit(t, "--git-dir="+filepath.Join(primary.Workspace, ".bare", "widget.git"), "fetch", "-q", "--prune", source, "+refs/heads/*:refs/remotes/origin/*")
	wire.mu.Lock()
	wire.merged = true
	wire.merge = merge
	wire.mu.Unlock()
	status319Age(t, "pr", "example/widget", "8")
	status319Age(t, "openprs", "example/widget", "open")
	status319Opportunity(t)
	deferred := status319Refresh(t, primary, server.URL, bin, false)
	if len(deferred.Snapshot.PRs) != 1 || deferred.Snapshot.PRs[0].State != "DRAFT" {
		t.Fatal("fixture failed to defer the omitted draft")
	}
	t.Setenv("WTC_TEST_319_INTERVAL", "1")
	started := time.Now()
	fixtureFile(t, statusForgeStatePath("budget", "bitbucket.org"), fmt.Sprintf("0 %d\n", started.UnixNano()), 0600)
	before := wire.count()
	legacy := status319StartMode(t, primary, server.URL, bin, false, "legacy")
	legacy.until(t, "ready")
	legacyStopped := false
	defer func() {
		if !legacyStopped {
			_ = legacy.command.Process.Kill()
			<-legacy.done
		}
	}()
	merged := false
	for range 3 {
		// Every new refresh starts just after a released client spent an allowance.
		legacy.until(t, "admitted")
		event := status319Refresh(t, primary, server.URL, bin, false)
		if event.Snapshot.PRs[0].State == "MERGED" {
			if event.Snapshot.PRs[0].Draft || !strings.Contains(strings.ToLower(event.Markdown), "merged") {
				t.Fatal("native producer/view lost merge")
			}
			merged = true
			break
		}
	}
	if !merged {
		t.Fatal("released shared-budget consumer starved the upgraded collector across three refreshes")
	}
	legacy.until(t, "admitted")
	requests := wire.count() - before
	if requests > 1+int(time.Since(started)/time.Second) {
		t.Fatalf("coexistence raised the request ceiling: %d requests", requests)
	}
	wire.mu.Lock()
	list, build := false, false
	for _, path := range wire.calls[before:] {
		list = list || strings.Contains(path, "/pressure/pullrequests")
		build = build || strings.Contains(path, "/pressure/pipelines/")
	}
	wire.mu.Unlock()
	if !list || !build {
		t.Fatal("released consumer did not exercise both list and build pressure")
	}
	// Cancel an actual collector while it owns the existing budget lock. No
	// provider read or merge marker may escape, and a released client recovers.
	_ = legacy.command.Process.Kill()
	<-legacy.done
	legacyStopped = true
	if _, err := primary.EnlistPR(PRRecord{Repo: "widget", Number: "10", Branch: "another", URL: "https://bitbucket.org/example/widget/pull-requests/10", Title: "Synthetic interrupted"}); err != nil {
		t.Fatal(err)
	}
	fixtureFile(t, statusForgeStatePath("budget", "bitbucket.org"), fmt.Sprintf("0 %d\n", time.Now().UnixNano()), 0600)
	cancelled := status319Start(t, primary, server.URL, bin, false)
	cancelled.until(t, "ready")
	probe, err := os.OpenFile(statusForgeStatePath("budget", "bitbucket.org")+".lock", os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	waiting := false
	for !waiting {
		select {
		case <-tick.C:
			err := unix.Flock(int(probe.Fd()), unix.LOCK_EX|unix.LOCK_NB)
			if err == nil {
				_ = unix.Flock(int(probe.Fd()), unix.LOCK_UN)
			} else if err == unix.EWOULDBLOCK {
				waiting = true
			} else {
				t.Fatal(err)
			}
		case <-deadline.C:
			t.Fatal("collector never held the admission lock")
		}
	}
	_ = cancelled.command.Process.Kill()
	<-cancelled.done
	survivor := status319StartMode(t, primary, server.URL, bin, false, "legacy")
	survivor.until(t, "ready")
	defer func() { _ = survivor.command.Process.Kill(); <-survivor.done }()
	survivor.until(t, "admitted")
	wire.mu.Lock()
	for _, path := range wire.calls {
		if path == "/cli/view/10" {
			wire.mu.Unlock()
			t.Fatal("cancelled admission dispatched a provider request")
		}
	}
	wire.mu.Unlock()
	records, _ := primary.ListPRs()
	for _, record := range records {
		if record.Number == "10" && record.MergedOn != "" {
			t.Fatal("cancelled admission recorded a merge")
		}
	}
}

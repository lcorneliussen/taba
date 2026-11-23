package wtc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"taba.sh/taba/internal/envcompat"
)

// Bitbucket reads without bb (issue #471).
//
// Every Bitbucket read taba makes goes through this adapter. It resolves the
// client (twg by default, bb when selected) and the read credential once per
// operation, and keeps both for that operation, so a selection or credential
// that changes mid-operation applies to the next one and never mixes into the
// current one.
//
// The read credential is either the client's own login ("personal", the
// default) or, opted in per host, a per-repository token the host's git
// credential helper answers for that repository ("git-credential"). A host
// token is asked for at most once per operation and repository, is kept in
// that operation's memory only, and reaches only the selected read's child
// process (or that repository's REST request). It never enters this process's
// environment, argv, a file, a cache key, a diagnostic or a write.

// bitbucketReadCredentialVariable selects the read credential. Legacy alias
// WTC_BITBUCKET_READ_CREDENTIAL. Resolved like TABA_BITBUCKET_TRANSPORT.
const bitbucketReadCredentialVariable = "TABA_BITBUCKET_READ_CREDENTIAL"

const (
	bitbucketCredentialPersonal = "personal"
	bitbucketCredentialGit      = "git-credential"
)

var errBitbucketReadCredential = errors.New("unsupported Bitbucket read credential selection")

// bitbucketReadSelection is the frozen read setup of one operation.
type bitbucketReadSelection struct {
	Client           bitbucketClient
	ClientSource     string
	Credential       string // bitbucketCredentialPersonal or bitbucketCredentialGit
	CredentialSource string
	err              error // a selection that cannot be used; every read fails with it
}

// selectedBitbucketRead resolves client and credential. root is the control
// root; empty uses TABA_CONFIG_ROOT from the environment, then skips the
// defaults file.
func selectedBitbucketRead(root string) bitbucketReadSelection {
	if root == "" {
		root, _ = envcompat.LookupEnv("TABA_CONFIG_ROOT")
	}
	client, err := selectedBitbucketClient(root)
	selection := bitbucketReadSelection{Client: client.Client, ClientSource: client.Source}
	if err != nil {
		selection.err = &bitbucketReadError{bitbucketSelectionCause(err, client.Source)}
		return selection
	}
	value, source, err := bitbucketSetting(root, bitbucketReadCredentialVariable)
	selection.CredentialSource = source
	if err != nil {
		selection.err = &bitbucketReadError{"Bitbucket read credential selection unavailable; the machine defaults file cannot be read safely"}
		return selection
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", bitbucketCredentialPersonal:
		selection.Credential = bitbucketCredentialPersonal
	case bitbucketCredentialGit:
		selection.Credential = bitbucketCredentialGit
		if selection.Client != bitbucketClientTWG {
			selection.err = &bitbucketReadError{bitbucketReadCredentialVariable + "=git-credential needs the twg client; bb cannot take a host read credential"}
		}
	default:
		selection.err = &bitbucketReadError{"unsupported " + bitbucketReadCredentialVariable + " value; set it to personal or git-credential, or unset it"}
	}
	return selection
}

func bitbucketSelectionCause(err error, source string) string {
	if errors.Is(err, errBitbucketClientDefaults) {
		return "Bitbucket client selection unavailable; the machine defaults file cannot be read safely"
	}
	return "unsupported " + bitbucketClientVariable + " value from " + source + "; set it to twg or bb, or unset it"
}

// fingerprint is the selection's identity for authenticated-context keys. It
// holds names only, never a credential.
func (s bitbucketReadSelection) fingerprint() string {
	failed := ""
	if s.err != nil {
		failed = "unusable"
	}
	return "bitbucket-read-v1\x00" + string(s.Client) + "\x00" + s.ClientSource + "\x00" + s.Credential + "\x00" + s.CredentialSource + "\x00" + failed
}

// accountKnown reports whether the selection names one stable, locally
// fingerprinted account. TWG does not expose its profile's account without
// an extra call, and a host credential is per repository, so observations
// through either stay private to the operation that made them.
func (s bitbucketReadSelection) accountKnown() bool {
	return s.err == nil && s.Client == bitbucketClientBB && s.Credential == bitbucketCredentialPersonal
}

type bitbucketReadKey struct{}

// bitbucketReadState is one operation's frozen selection and the host
// credentials it obtained, by repository.
type bitbucketReadState struct {
	selection bitbucketReadSelection
	mu        sync.Mutex
	tokens    map[string]*bitbucketRepoToken
}

type bitbucketRepoToken struct {
	once  sync.Once
	token string
	err   error
}

// bitbucketReadOperation attaches a read operation to ctx unless one is
// attached already. root as for selectedBitbucketRead.
func bitbucketReadOperation(ctx context.Context, root string) context.Context {
	if _, ok := ctx.Value(bitbucketReadKey{}).(*bitbucketReadState); ok {
		return ctx
	}
	return context.WithValue(ctx, bitbucketReadKey{}, &bitbucketReadState{selection: selectedBitbucketRead(root), tokens: map[string]*bitbucketRepoToken{}})
}

func bitbucketReadFor(ctx context.Context) *bitbucketReadState {
	if state, ok := ctx.Value(bitbucketReadKey{}).(*bitbucketReadState); ok {
		return state
	}
	return &bitbucketReadState{selection: selectedBitbucketRead(""), tokens: map[string]*bitbucketRepoToken{}}
}

// repoToken asks the host for one repository's read credential, at most once
// per operation. Concurrent readers of the same repository share the answer.
func (s *bitbucketReadState) repoToken(ctx context.Context, workspace, repo string) (string, error) {
	key := strings.ToLower(workspace + "/" + repo)
	s.mu.Lock()
	entry := s.tokens[key]
	if entry == nil {
		entry = &bitbucketRepoToken{}
		s.tokens[key] = entry
	}
	s.mu.Unlock()
	entry.once.Do(func() { entry.token, entry.err = gitCredentialFill(ctx, workspace, repo) })
	return entry.token, entry.err
}

// childEnv is the environment addition for one read child. A personal read
// adds only the TWG agent defaults; a host read adds that repository's
// token, with an empty user so TWG sends it as a bearer token.
func (s *bitbucketReadState) childEnv(ctx context.Context, workspace, repo string) ([]string, error) {
	if s.selection.err != nil {
		return nil, s.selection.err
	}
	if s.selection.Client == bitbucketClientBB {
		return nil, nil
	}
	env := twgChildEnv()
	if s.selection.Credential == bitbucketCredentialGit {
		token, err := s.repoToken(ctx, workspace, repo)
		if err != nil {
			return nil, err
		}
		env = append(env, "TWG_BBC_TOKEN="+token, "TWG_BBC_USER=")
	}
	return env, nil
}

// restBearer returns the host read token for one repository's REST reads, or
// "" when reads use the personal credential.
func (s *bitbucketReadState) restBearer(ctx context.Context, workspace, repo string) (string, bool, error) {
	if s.selection.err != nil {
		return "", false, s.selection.err
	}
	if s.selection.Credential != bitbucketCredentialGit {
		return "", false, nil
	}
	token, err := s.repoToken(ctx, workspace, repo)
	return token, true, err
}

// gitCredentialTimeout bounds one credential helper query.
var gitCredentialTimeout = 10 * time.Second

const (
	bitbucketCauseNoCredential   = "no host read credential for this repository; configure the host's git credential helper for it"
	bitbucketCauseCredentialFail = "host read credential unavailable; the git credential helper did not answer for this repository"
)

// gitCredentialFill asks git's configured helpers for one repository's
// credential, noninteractively. useHttpPath is forced so helpers see the
// repository path and can answer per repository. Only the password is used; the helper's
// username is ignored rather than interpreted as an authentication mode. The
// helper's stderr and stdout are never echoed. git is never asked to approve
// or reject, so no credential store is written.
func gitCredentialFill(parent context.Context, workspace, repo string) (string, error) {
	if !bitbucketSlugPart(workspace) || !bitbucketSlugPart(repo) {
		return "", &bitbucketReadError{"invalid Bitbucket repository slug"}
	}
	ctx, cancel := context.WithTimeout(parent, gitCredentialTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-c", "credential.interactive=false", "-c", "credential.useHttpPath=true", "credential", "fill")
	cmd.WaitDelay = 200 * time.Millisecond
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "SSH_ASKPASS=", "GCM_INTERACTIVE=never")
	cmd.Stdin = strings.NewReader("protocol=https\nhost=bitbucket.org\npath=" + workspace + "/" + repo + ".git\n\n")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = nil
	err := cmd.Run()
	if parent.Err() != nil {
		return "", parent.Err()
	}
	if ctx.Err() != nil {
		return "", &bitbucketReadError{fmt.Sprintf("host read credential unavailable; the git credential helper timed out after %s", gitCredentialTimeout)}
	}
	if err != nil {
		// git exits non-zero when no helper had an answer and prompting is off.
		return "", &bitbucketReadError{bitbucketCauseNoCredential}
	}
	token := ""
	for _, line := range strings.Split(stdout.String(), "\n") {
		if value, ok := strings.CutPrefix(line, "password="); ok {
			token = value
		}
	}
	if token == "" {
		return "", &bitbucketReadError{bitbucketCauseNoCredential}
	}
	if len(token) > 4096 || strings.ContainsAny(token, "\x00\r\n") {
		return "", &bitbucketReadError{bitbucketCauseCredentialFail}
	}
	return token, nil
}

func bitbucketSlugPart(value string) bool {
	if value == "" || value == "." || value == ".." || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}

func splitBitbucketSlug(slug string) (string, string, error) {
	workspace, repo, ok := strings.Cut(slug, "/")
	if !ok || !bitbucketSlugPart(workspace) || !bitbucketSlugPart(repo) {
		return "", "", &bitbucketReadError{"invalid Bitbucket repository slug"}
	}
	return workspace, repo, nil
}

// bitbucketReadTimeout bounds one read child.
var bitbucketReadTimeout = 30 * time.Second

// bitbucketReadRun runs one read child under ctx and a deadline. env is added
// to that child only. Errors carry the operation and a fixed cause, never the
// child's stderr: a TWG child holding a host token must not be able to echo
// anything into a diagnostic.
func bitbucketReadRun(ctx context.Context, env []string, operation string, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	child, cancel := context.WithTimeout(ctx, bitbucketReadTimeout)
	defer cancel()
	cmd := exec.CommandContext(child, args[0], args[1:]...)
	cmd.WaitDelay = 200 * time.Millisecond
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	cmd.Stderr = discardWriter{}
	out, err := forgeOutput(cmd)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if child.Err() != nil {
		return nil, fmt.Errorf("%s timed out after %s", operation, bitbucketReadTimeout)
	}
	if errors.Is(err, exec.ErrNotFound) {
		return out, fmt.Errorf("%s: %s is not installed", operation, commandLabel(args[0]))
	}
	return out, err
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

// bitbucketReadError wraps a classified failure with the operation name.
func bitbucketOperationError(operation string, err error) error {
	if err == nil {
		return nil
	}
	var classified *bitbucketReadError
	if errors.As(err, &classified) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w", operation, err)
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return fmt.Errorf("%s failed (exit status %d)", operation, exit.ExitCode())
	}
	return err
}

// twgRead runs one TWG read and returns its reply after the failure envelope
// and the exit status were checked.
func (s *bitbucketReadState) twgRead(ctx context.Context, workspace, repo, operation string, args ...string) ([]byte, error) {
	env, err := s.childEnv(ctx, workspace, repo)
	if err != nil {
		return nil, bitbucketOperationError(operation, err)
	}
	if err := (twgBitbucketReader{run: func(env []string, args ...string) ([]byte, error) {
		return bitbucketReadRun(ctx, env, "twg --version", args...)
	}}).checkVersion(); err != nil {
		return nil, bitbucketOperationError(operation, err)
	}
	full := append([]string{"twg"}, args...)
	full = append(full, "--workspace", workspace, "--repo", repo, "--output", "json", "--output-summary", "none")
	out, runErr := bitbucketReadRun(ctx, env, operation, full...)
	if failure, ok := twgFailure(out); ok {
		return nil, bitbucketOperationError(operation, failure)
	}
	if runErr != nil {
		return nil, bitbucketOperationError(operation, runErr)
	}
	return out, nil
}

// bitbucketReadPR reads one pull request as its Bitbucket resource, with the
// returned id and repository pinned to the request when TWG answered.
func bitbucketReadPR(ctx context.Context, slug, number string) ([]byte, error) {
	workspace, repo, err := splitBitbucketSlug(slug)
	if err != nil {
		return nil, err
	}
	if _, err := strconv.Atoi(number); err != nil {
		return nil, &bitbucketReadError{"invalid PR number"}
	}
	s := bitbucketReadFor(ctx)
	if s.selection.err != nil {
		return nil, bitbucketOperationError("Bitbucket PR read", s.selection.err)
	}
	if s.selection.Client == bitbucketClientBB {
		out, err := bitbucketReadRun(ctx, nil, "bb pr view", "bb", "pr", "view", number, "--workspace", workspace, "--repo", repo, "--json")
		return out, bitbucketOperationError("bb pr view", err)
	}
	const operation = "twg bitbucket pull-requests get"
	out, err := s.twgRead(ctx, workspace, repo, operation, "bitbucket", "pull-requests", "get", number, "--strict")
	if err != nil {
		return nil, err
	}
	data, err := twgSinglePullRequest(out, number)
	if err != nil {
		return nil, bitbucketOperationError(operation, err)
	}
	if err := twgPinPullRequest(data, workspace, repo, number); err != nil {
		return nil, bitbucketOperationError(operation, err)
	}
	return data, nil
}

// twgPinPullRequest checks a pull-request resource's id and repository.
func twgPinPullRequest(data []byte, workspace, repo, number string) error {
	var facts bitbucketRESTPullRequest
	if json.Unmarshal(data, &facts) != nil {
		return &bitbucketReadError{bitbucketCauseInvalidJSON}
	}
	if len(facts.Failures) > 0 {
		return &bitbucketReadError{bitbucketCauseIncomplete}
	}
	if string(bytes.TrimSpace(facts.ID)) != number {
		return &bitbucketReadError{bitbucketCauseIdentity}
	}
	if facts.Destination.Repository == nil || facts.Destination.Repository.FullName == "" {
		return &bitbucketReadError{bitbucketCauseMissingField}
	}
	if !strings.EqualFold(facts.Destination.Repository.FullName, workspace+"/"+repo) {
		return &bitbucketReadError{bitbucketCauseIdentity}
	}
	return nil
}

// bitbucketReadPRActivity reads a pull request's activity log as
// {"activities": [...]} for either client.
func bitbucketReadPRActivity(ctx context.Context, slug, number string) ([]byte, error) {
	workspace, repo, err := splitBitbucketSlug(slug)
	if err != nil {
		return nil, err
	}
	s := bitbucketReadFor(ctx)
	if s.selection.err != nil {
		return nil, bitbucketOperationError("Bitbucket PR activity read", s.selection.err)
	}
	if s.selection.Client == bitbucketClientBB {
		out, err := bitbucketReadRun(ctx, nil, "bb pr activity", "bb", "pr", "activity", number, "--workspace", workspace, "--repo", repo, "--json")
		return out, bitbucketOperationError("bb pr activity", err)
	}
	const operation = "twg bitbucket pull-requests activity"
	out, err := s.twgRead(ctx, workspace, repo, operation, "bitbucket", "pull-requests", "activity", number, "--strict", "--limit", "200")
	if err != nil {
		return nil, err
	}
	data, err := twgSingleItem(out, number)
	if err != nil {
		return nil, bitbucketOperationError(operation, err)
	}
	values, err := twgValues(data)
	if err != nil {
		return nil, bitbucketOperationError(operation, err)
	}
	if len(values) >= 200 {
		// A full page may hide the merge event; never read it as complete.
		return nil, bitbucketOperationError(operation, &bitbucketReadError{bitbucketCauseIncomplete})
	}
	for _, raw := range values {
		var item struct {
			PullRequest *struct {
				ID json.RawMessage `json:"id"`
			} `json:"pull_request"`
		}
		if json.Unmarshal(raw, &item) == nil && item.PullRequest != nil && len(item.PullRequest.ID) > 0 && string(bytes.TrimSpace(item.PullRequest.ID)) != number {
			return nil, bitbucketOperationError(operation, &bitbucketReadError{bitbucketCauseIdentity})
		}
	}
	return json.Marshal(map[string]any{"activities": values})
}

// bitbucketReadPRComments reads a pull request's comments in bb's collection
// wrapper ({"comments":[...],"count":n}) for either client. limit bounds the read; a reply that reaches it is
// incomplete, never a full list.
func bitbucketReadPRComments(ctx context.Context, slug, number string, limit int) ([]byte, error) {
	workspace, repo, err := splitBitbucketSlug(slug)
	if err != nil {
		return nil, err
	}
	s := bitbucketReadFor(ctx)
	if s.selection.err != nil {
		return nil, bitbucketOperationError("Bitbucket PR comments read", s.selection.err)
	}
	if s.selection.Client == bitbucketClientBB {
		args := []string{"bb", "--workspace", workspace, "--repo", repo, "pr", "comments", "list", number}
		if limit > 0 {
			args = append(args, "--limit", strconv.Itoa(limit))
		} else {
			args = append(args, "--all")
		}
		out, err := bitbucketReadRun(ctx, nil, "bb pr comments list", append(args, "--json")...)
		return out, bitbucketOperationError("bb pr comments list", err)
	}
	if limit <= 0 {
		limit = bitbucketReviewCommentLimit
	}
	const operation = "twg bitbucket pull-requests comment query"
	out, err := s.twgRead(ctx, workspace, repo, operation, "bitbucket", "pull-requests", "comment", "query", number, "--limit", strconv.Itoa(limit+1))
	if err != nil {
		return nil, err
	}
	data, err := twgSingleItem(out, number)
	if err != nil {
		return nil, bitbucketOperationError(operation, err)
	}
	values, err := twgValues(data)
	if err != nil {
		return nil, bitbucketOperationError(operation, err)
	}
	if len(values) > limit {
		return nil, bitbucketOperationError(operation, &bitbucketReadError{bitbucketCauseIncomplete})
	}
	for _, raw := range values {
		var item struct {
			PullRequest *struct {
				ID json.RawMessage `json:"id"`
			} `json:"pullrequest"`
		}
		if json.Unmarshal(raw, &item) == nil && item.PullRequest != nil && len(item.PullRequest.ID) > 0 && string(bytes.TrimSpace(item.PullRequest.ID)) != number {
			return nil, bitbucketOperationError(operation, &bitbucketReadError{bitbucketCauseIdentity})
		}
	}
	// The bb collection wrapper, so both clients feed one strict parser.
	return json.Marshal(map[string]any{"comments": values, "count": len(values)})
}

// bitbucketReadRepo reads a repository resource; the caller decides on
// is_private. The returned full_name must be the requested repository.
func bitbucketReadRepo(ctx context.Context, slug string) ([]byte, error) {
	workspace, repo, err := splitBitbucketSlug(slug)
	if err != nil {
		return nil, err
	}
	s := bitbucketReadFor(ctx)
	if s.selection.err != nil {
		return nil, bitbucketOperationError("Bitbucket repository read", s.selection.err)
	}
	if s.selection.Client == bitbucketClientBB {
		out, err := bitbucketReadRun(ctx, nil, "bb repo view", "bb", "repo", "view", slug, "--json")
		return out, bitbucketOperationError("bb repo view", err)
	}
	const operation = "twg bitbucket repo get"
	out, err := s.twgRead(ctx, workspace, repo, operation, "bitbucket", "repo", "get")
	if err != nil {
		return nil, err
	}
	top, err := twgObject(out)
	if err == nil {
		err = twgComplete(top)
	}
	if err != nil {
		return nil, bitbucketOperationError(operation, err)
	}
	data := out
	if raw, ok := top["data"]; ok {
		data = raw
	}
	var facts struct {
		FullName string `json:"full_name"`
	}
	if json.Unmarshal(data, &facts) != nil || !strings.EqualFold(facts.FullName, slug) {
		return nil, bitbucketOperationError(operation, &bitbucketReadError{bitbucketCauseIdentity})
	}
	return data, nil
}

// bitbucketReadOpenPRs lists open pull requests whose source branch is
// branch, as {"values":[...]}. Reaching limit is an error, never a full list.
func bitbucketReadOpenPRs(ctx context.Context, slug, branch string, limit int) ([]byte, error) {
	workspace, repo, err := splitBitbucketSlug(slug)
	if err != nil {
		return nil, err
	}
	s := bitbucketReadFor(ctx)
	if s.selection.err != nil {
		return nil, bitbucketOperationError("Bitbucket PR list", s.selection.err)
	}
	if s.selection.Client == bitbucketClientBB {
		out, err := bitbucketReadRun(ctx, nil, "bb pr list", "bb", "--workspace", workspace, "--repo", repo, "pr", "list", "--state", "OPEN", "--limit", strconv.Itoa(limit), "--json")
		return out, bitbucketOperationError("bb pr list", err)
	}
	const operation = "twg bitbucket pull-requests query"
	out, err := s.twgRead(ctx, workspace, repo, operation, "bitbucket", "pull-requests", "query", "--scope", "repo", "--state", "OPEN", "--source", branch, "--limit", strconv.Itoa(limit+1))
	if err != nil {
		return nil, err
	}
	top, err := twgObject(out)
	if err == nil {
		err = twgComplete(top)
	}
	if err != nil {
		var list []json.RawMessage
		if json.Unmarshal(out, &list) != nil {
			return nil, bitbucketOperationError(operation, err)
		}
		top = map[string]json.RawMessage{"values": out}
	}
	data := out
	if raw, ok := top["data"]; ok {
		data = raw
	} else if raw, ok := top["values"]; ok {
		data = raw
	}
	values, err := twgValues(data)
	if err != nil {
		return nil, bitbucketOperationError(operation, err)
	}
	if len(values) > limit {
		return nil, bitbucketOperationError(operation, &bitbucketReadError{bitbucketCauseIncomplete})
	}
	return json.Marshal(map[string]any{"values": values})
}

// BitbucketReadPR reads one pull request through the selected client for a
// command outside this package. root is the control root.
func BitbucketReadPR(root, slug, number string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return bitbucketReadPR(bitbucketReadOperation(ctx, root), slug, number)
}

// BitbucketReadOpenPRs lists open pull requests from branch, as
// {"values":[...]}, through the selected client.
func BitbucketReadOpenPRs(root, slug, branch string, limit int) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return bitbucketReadOpenPRs(bitbucketReadOperation(ctx, root), slug, branch, limit)
}

// twgSingleItem returns the data of a one-id reply, bare or as a one-item
// batch, with completeness checked at every level.
func twgSingleItem(out []byte, number string) ([]byte, error) {
	trimmed := bytes.TrimSpace(out)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		return trimmed, nil
	}
	top, err := twgObject(out)
	if err != nil {
		return nil, err
	}
	if err := twgComplete(top); err != nil {
		return nil, err
	}
	if _, batch := top["items"]; !batch {
		return out, nil
	}
	return twgSingleBatchData(out, number, true)
}

// twgValues returns a list reply's entries: a bare array, or an object's
// values (or items) with completeness checked. A paginated reply that names a
// next page is incomplete.
func twgValues(data []byte) ([]json.RawMessage, error) {
	var list []json.RawMessage
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		if json.Unmarshal(trimmed, &list) != nil {
			return nil, &bitbucketReadError{bitbucketCauseInvalidJSON}
		}
		return list, nil
	}
	object, err := twgObject(data)
	if err != nil {
		return nil, err
	}
	if err := twgComplete(object); err != nil {
		return nil, err
	}
	if next, ok := object["next"]; ok && strings.TrimSpace(string(next)) != "null" && strings.TrimSpace(string(next)) != `""` {
		return nil, &bitbucketReadError{bitbucketCauseIncomplete}
	}
	raw, ok := object["values"]
	if !ok {
		raw, ok = object["items"]
	}
	if !ok {
		return nil, &bitbucketReadError{bitbucketCauseMissingField}
	}
	if json.Unmarshal(raw, &list) != nil || list == nil {
		return nil, &bitbucketReadError{bitbucketCauseMalformed}
	}
	return list, nil
}

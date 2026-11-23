package wtc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"taba.sh/taba/internal/envcompat"
)

// Bitbucket transport selection and normalized reads (issue #127).
//
// The operation contract lives in docs/forges.md. This file holds the shared
// adapter: one normalized read result per operation, independent of the client
// that produced it. Since issue #471 TWG is the default client; the deprecated
// `bb` client runs only when it is selected explicitly. The default is a
// release decision, made once at upgrade: a failed or uncertain read through
// the selected client is reported as such and never retried through another
// client, because the two clients may act as different accounts.

type bitbucketClient string

const (
	bitbucketClientBB  bitbucketClient = "bb"
	bitbucketClientTWG bitbucketClient = "twg"
)

// bitbucketClientVariable selects the client for migrated Bitbucket reads.
// WTC_BITBUCKET_TRANSPORT is accepted as the legacy alias. The process
// environment wins, even when set empty; otherwise the machine defaults file
// (taba.env, or legacy wtc.env) in the control root supplies it, with the same
// precedence and source checks as the TWG site default.
const bitbucketClientVariable = "TABA_BITBUCKET_TRANSPORT"

// errBitbucketClientSelection is returned for a selection this version does
// not understand. It is never treated as "use the default".
var errBitbucketClientSelection = errors.New("unsupported Bitbucket transport selection")

// errBitbucketClientDefaults is returned when the machine defaults file that
// could hold the selection cannot be read safely. The selection is then
// unknown, never the default.
var errBitbucketClientDefaults = errors.New("Bitbucket transport selection unavailable")

// bitbucketClientSelection is one resolved selection and where it came from.
// Source is "environment", the defaults file name ("taba.env" or "wtc.env"),
// "machine defaults" when the defaults file could not even be identified, or
// "default". It never holds a path or a value.
type bitbucketClientSelection struct {
	Client bitbucketClient
	Source string
}

// selectedBitbucketClient resolves the selection when an operation runs, so a
// changed selection applies to the next operation and nothing observed
// through the previous client is reused. root is the control root; empty
// skips the defaults file.
func selectedBitbucketClient(root string) (bitbucketClientSelection, error) {
	value, source, err := bitbucketSetting(root, bitbucketClientVariable)
	if err != nil {
		return bitbucketClientSelection{Source: source}, errBitbucketClientDefaults
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "twg":
		return bitbucketClientSelection{Client: bitbucketClientTWG, Source: source}, nil
	case "bb":
		return bitbucketClientSelection{Client: bitbucketClientBB, Source: source}, nil
	}
	return bitbucketClientSelection{Source: source}, errBitbucketClientSelection
}

// bitbucketSetting resolves one Bitbucket setting: the process environment
// wins, even when set empty; otherwise the machine defaults file. source is
// "environment", the defaults file name, "machine defaults" or "default"; it
// never holds a path or a value.
func bitbucketSetting(root, variable string) (value, source string, err error) {
	if value, set := envcompat.LookupEnv(variable); set {
		return value, "environment", nil
	}
	return machineBitbucketSetting(root, variable)
}

// machineBitbucketSetting reads a setting from the machine defaults file,
// mirroring machineTWGSite: the file must sit inside the control root and be a
// regular file. A missing file or key is the default.
func machineBitbucketSetting(root, variable string) (value, source string, err error) {
	if root == "" {
		return "", "default", nil
	}
	path, err := MachineEnvFile(root)
	if err != nil {
		return "", "machine defaults", err
	}
	source = filepath.Base(path)
	canonicalRoot, err := canonicalPath(root)
	if err != nil {
		return "", source, err
	}
	if err := (ResolvedScope{Root: canonicalRoot}).validateSourcePath(path); err != nil {
		return "", source, err
	}
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return "", "default", nil
	}
	if err != nil {
		return "", source, err
	}
	if !info.Mode().IsRegular() {
		return "", source, errBitbucketClientDefaults
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", source, err
	}
	value, set := envcompat.LookupMap(variable, envcompat.Assignments(data))
	if !set {
		return "", "default", nil
	}
	return value, source, nil
}

// BitbucketClientReport describes the Bitbucket client selection for doctor.
// It reads only local configuration and, when TWG is selected, asks the
// selected executable for its version once; it never contacts a forge, reads
// credentials or probes an account. ok is false when a migrated read would be
// refused; the check is advisory.
func BitbucketClientReport(root string) (ok bool, detail string) {
	selection, err := selectedBitbucketClient(root)
	from := " from " + selection.Source
	switch {
	case errors.Is(err, errBitbucketClientDefaults):
		where := "machine defaults"
		if selection.Source != where {
			where += " (" + selection.Source + ")"
		}
		return false, "advisory: selection unavailable; " + where + " cannot be read safely"
	case err != nil:
		return false, "advisory: unsupported " + bitbucketClientVariable + " value" + from + "; set it to bb or twg, or unset it"
	case selection.Client == bitbucketClientBB:
		return true, "bb" + from + " (deprecated; unset it to use twg)"
	}
	if selection.Source == "default" {
		from = " (default)"
	}
	reader := twgBitbucketReader{run: func(env []string, args ...string) ([]byte, error) {
		return bitbucketCommand(bitbucketDoctorTimeout, env, args...)
	}}
	if err := reader.checkVersion(); err != nil {
		var classified *bitbucketReadError
		switch {
		case errors.Is(err, exec.ErrNotFound):
			return false, "advisory: twg" + from + ", but twg is not installed; migrated reads stay unavailable"
		case errors.As(err, &classified):
			return false, "advisory: twg" + from + ", but its version is unsupported (needs 1.x at or above 1.4.4)"
		default:
			return false, "advisory: twg" + from + ", but its version could not be read"
		}
	}
	return true, "twg" + from + "; version supported"
}

// bitbucketDoctorTimeout bounds doctor's one version subprocess.
var bitbucketDoctorTimeout = 10 * time.Second

// bitbucketPR is the normalized result of one pull-request read. State is one
// that catchUpValidPRState accepts (OPEN, DRAFT, MERGED, CLOSED, DECLINED or
// SUPERSEDED); anything else is refused before a value of this type exists.
type bitbucketPR struct {
	State       string
	Destination string
	Head        string
	MergeCommit string
}

// bitbucketReadError is a classified failure with a fixed, value-free cause.
// Causes never echo response bodies, stderr, URLs or account details; the only
// response value they may carry is a TWG error code that passed
// twgErrorCodePattern.
type bitbucketReadError struct {
	cause  string
	status int // the HTTP status a client reported, when it named one
}

func (e *bitbucketReadError) Error() string { return e.cause }

const (
	bitbucketCauseInvalidJSON  = "invalid JSON response; check forge CLI compatibility and retry"
	bitbucketCauseState        = "unrecognized PR state; check forge CLI compatibility and retry"
	bitbucketCauseIdentity     = "response does not match the requested repository or PR; check the forge CLI target and retry"
	bitbucketCauseIncomplete   = "incomplete or partial response; retry, and inspect the forge CLI locally if it persists"
	bitbucketCauseAuth         = "authentication or authorization failed; check CLI login and repository access, then retry"
	bitbucketCauseMissingField = "required field missing from response; check forge CLI compatibility and retry"
	bitbucketCauseMalformed    = "malformed or conflicting completeness metadata in response; check forge CLI compatibility and retry"
)

// bitbucketRunner runs one owned client process with a bounded deadline. env
// holds additions for that process only; the caller's environment is not
// changed.
type bitbucketRunner func(env []string, args ...string) ([]byte, error)

// bitbucketPRReader reads one pull request through one client.
type bitbucketPRReader interface {
	// Operation names the client operation for diagnostics.
	Operation() string
	PullRequest(workspace, repo, number string) (bitbucketPR, error)
}

func newBitbucketPRReader(transport bitbucketClient, run bitbucketRunner) bitbucketPRReader {
	if transport == bitbucketClientTWG {
		return twgBitbucketReader{run: run}
	}
	return bbBitbucketReader{run: run}
}

// bbBitbucketReader is the existing deprecated-compatibility client.
type bbBitbucketReader struct{ run bitbucketRunner }

func (bbBitbucketReader) Operation() string { return "bb pr view" }

func (r bbBitbucketReader) PullRequest(workspace, repo, number string) (bitbucketPR, error) {
	out, err := r.run(nil, "bb", "pr", "view", number, "--workspace", workspace, "--repo", repo, "--json")
	if err != nil {
		return bitbucketPR{}, err
	}
	var facts bitbucketRESTPullRequest
	if json.Unmarshal(out, &facts) != nil {
		return bitbucketPR{}, &bitbucketReadError{cause: bitbucketCauseInvalidJSON}
	}
	// Unlike the TWG reader, this reader does not check the response's PR id
	// or repository: it keeps the pre-adapter bb behavior unchanged.
	return facts.normalize()
}

// bitbucketRESTPullRequest is the Bitbucket Cloud pull-request resource. The
// bb shape is the one catch-up has always read; the TWG shape follows TWG
// 1.4.4 help and has not been checked against a live capture. Only the fields
// a read needs are decoded. _failures is TWG completeness metadata, not part
// of the Bitbucket resource.
type bitbucketRESTPullRequest struct {
	ID    json.RawMessage `json:"id"`
	State string          `json:"state"`
	Draft bool            `json:"draft"`
	Merge *struct {
		Hash string `json:"hash"`
	} `json:"merge_commit"`
	Source struct {
		Commit *struct {
			Hash string `json:"hash"`
		} `json:"commit"`
	} `json:"source"`
	Destination struct {
		Branch struct {
			Name string `json:"name"`
		} `json:"branch"`
		Repository *struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	} `json:"destination"`
	Failures []json.RawMessage `json:"_failures"`
}

func (p bitbucketRESTPullRequest) normalize() (bitbucketPR, error) {
	// Same acceptance as the pre-adapter bb reader, so the compatibility
	// client keeps its exact behavior.
	state := strings.ToUpper(p.State)
	if p.Draft && state == "OPEN" {
		state = "DRAFT"
	}
	if !catchUpValidPRState(state) {
		return bitbucketPR{}, &bitbucketReadError{cause: bitbucketCauseState}
	}
	pr := bitbucketPR{State: state, Destination: p.Destination.Branch.Name}
	if p.Source.Commit != nil {
		pr.Head = p.Source.Commit.Hash
	}
	if p.Merge != nil {
		pr.MergeCommit = p.Merge.Hash
	}
	return pr, nil
}

// twgBitbucketReader reads through TWG's native Bitbucket pull-request get.
// Contract verified against TWG 1.4.4 help (see docs/forges.md): explicit
// workspace and repository, no optional hydration, strict batch semantics,
// JSON on stdout without the agent summary envelope.
type twgBitbucketReader struct{ run bitbucketRunner }

func (twgBitbucketReader) Operation() string { return "twg bitbucket pull-requests get" }

// twgMinimumVersion is the oldest TWG whose contract this reader was verified
// against. A newer major version is not assumed to keep it.
var twgMinimumVersion = [3]int{1, 4, 4}

// twgVersionPattern accepts a bare semantic version as `twg --version` prints
// it, with optional pre-release (-...) and build (+...) suffixes.
var twgVersionPattern = regexp.MustCompile(`^v?([0-9]+)\.([0-9]+)\.([0-9]+)(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)

func twgVersionSupported(output string) bool {
	match := twgVersionPattern.FindStringSubmatch(strings.TrimSpace(output))
	if match == nil {
		return false
	}
	var got [3]int
	for i := range got {
		n, err := strconv.Atoi(match[i+1])
		if err != nil {
			return false
		}
		got[i] = n
	}
	if got[0] != twgMinimumVersion[0] {
		return false
	}
	for i := 1; i < 3; i++ {
		if got[i] != twgMinimumVersion[i] {
			return got[i] > twgMinimumVersion[i]
		}
	}
	// A pre-release of the minimum itself precedes it.
	return match[4] == ""
}

// twgVersionChecks remembers one version answer per resolved executable for
// the life of the process, so a sweep pays for at most one extra subprocess
// and only when TWG was selected and a migrated read is actually needed.
var twgVersionChecks sync.Map // resolved path and file identity -> error (nil when supported)

func (r twgBitbucketReader) checkVersion() error {
	path, err := exec.LookPath("twg")
	if err != nil {
		return err
	}
	// Key by the executable's identity too, so an upgrade in place is
	// asked again by a long-lived process.
	key := path
	if info, err := os.Stat(path); err == nil {
		key = fmt.Sprintf("%s\x00%d\x00%d", path, info.Size(), info.ModTime().UnixNano())
	}
	if cached, ok := twgVersionChecks.Load(key); ok {
		if cached == nil {
			return nil
		}
		return cached.(error)
	}
	out, err := r.run(twgChildEnv(), path, "--version")
	if err != nil {
		// Transient failures are not remembered; the next read asks again.
		return err
	}
	var result error
	if !twgVersionSupported(string(out)) {
		result = &bitbucketReadError{cause: fmt.Sprintf("unsupported TWG version; this reader needs TWG %d.x at or above %d.%d.%d", twgMinimumVersion[0], twgMinimumVersion[0], twgMinimumVersion[1], twgMinimumVersion[2])}
	}
	twgVersionChecks.Store(key, result)
	return result
}

// twgChildEnv opts the TWG child process into agent defaults. Explicit output
// flags are still passed, so the result does not depend on this alone.
func twgChildEnv() []string { return []string{"TWG_AGENT_DEFAULTS=1"} }

func (r twgBitbucketReader) PullRequest(workspace, repo, number string) (bitbucketPR, error) {
	if err := r.checkVersion(); err != nil {
		return bitbucketPR{}, err
	}
	out, runErr := r.run(twgChildEnv(), "twg", "bitbucket", "pull-requests", "get", number,
		"--workspace", workspace, "--repo", repo, "--strict",
		"--output", "json", "--output-summary", "none")
	// A failure envelope is the most specific evidence, whatever the exit status.
	if failure, ok := twgFailure(out); ok {
		return bitbucketPR{}, failure
	}
	if runErr != nil {
		return bitbucketPR{}, twgExitError(out, number, runErr)
	}
	data, err := twgSinglePullRequest(out, number)
	if err != nil {
		return bitbucketPR{}, err
	}
	var facts bitbucketRESTPullRequest
	if json.Unmarshal(data, &facts) != nil {
		return bitbucketPR{}, &bitbucketReadError{cause: bitbucketCauseInvalidJSON}
	}
	if len(facts.Failures) > 0 {
		return bitbucketPR{}, &bitbucketReadError{cause: bitbucketCauseIncomplete}
	}
	// The resource id is a JSON integer; a string or another number is not
	// the PR that was asked for.
	if string(bytes.TrimSpace(facts.ID)) != number {
		return bitbucketPR{}, &bitbucketReadError{cause: bitbucketCauseIdentity}
	}
	if facts.Destination.Repository == nil || facts.Destination.Repository.FullName == "" {
		return bitbucketPR{}, &bitbucketReadError{cause: bitbucketCauseMissingField}
	}
	if !strings.EqualFold(facts.Destination.Repository.FullName, workspace+"/"+repo) {
		return bitbucketPR{}, &bitbucketReadError{cause: bitbucketCauseIdentity}
	}
	pr, err := facts.normalize()
	if err != nil {
		return pr, err
	}
	if (pr.State == "OPEN" || pr.State == "DRAFT") && pr.Destination == "" {
		return bitbucketPR{}, &bitbucketReadError{cause: bitbucketCauseMissingField}
	}
	return pr, nil
}

// twgExitError chooses the error for a TWG process that did not succeed. A
// non-zero exit is never a usable answer, whatever stdout holds. Under
// --strict a failed batch read exits non-zero but keeps its envelope, so a
// classified item failure (incomplete, denied, identity) is preferred, except
// when stdout is not JSON at all: then the process error is more specific
// (timeout, missing CLI, exit status).
func twgExitError(out []byte, number string, runErr error) error {
	var exit *exec.ExitError
	if !errors.As(runErr, &exit) {
		return runErr
	}
	if _, err := twgSinglePullRequest(out, number); err != nil {
		var classified *bitbucketReadError
		if errors.As(err, &classified) && classified.cause != bitbucketCauseInvalidJSON {
			return err
		}
	}
	return runErr
}

// twgEnvelope is the failure envelope ({"ok":false,"error":{...}}). Batch and
// resource replies are read by twgSinglePullRequest.
type twgEnvelope struct {
	OK    *bool     `json:"ok"`
	Error *twgError `json:"error"`
}

type twgError struct {
	Code       string `json:"code"`
	Kind       string `json:"kind"`
	StatusCode int    `json:"statusCode"`
}

var twgErrorCodePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

// twgFailure reports a TWG failure envelope. Only error.code, error.kind and
// error.statusCode are inspected; the message, repair text and diagnostics are
// never echoed.
func twgFailure(out []byte) (*bitbucketReadError, bool) {
	var envelope twgEnvelope
	if len(bytes.TrimSpace(out)) == 0 || json.Unmarshal(out, &envelope) != nil {
		return nil, false
	}
	if envelope.OK == nil || *envelope.OK {
		return nil, false
	}
	return classifyTWGError(envelope.Error), true
}

func classifyTWGError(e *twgError) *bitbucketReadError {
	if e == nil {
		return &bitbucketReadError{cause: "TWG reported a failure without an error; inspect the operation locally and retry"}
	}
	envelope := struct{ Error *twgError }{e}
	text := strings.ToLower(envelope.Error.Code + " " + envelope.Error.Kind)
	if envelope.Error.StatusCode == 429 {
		return &bitbucketReadError{cause: "rate limited by Bitbucket (HTTP 429); retry later", status: 429}
	}
	if envelope.Error.StatusCode == 401 || envelope.Error.StatusCode == 403 {
		return &bitbucketReadError{cause: bitbucketCauseAuth, status: envelope.Error.StatusCode}
	}
	if envelope.Error.StatusCode == 404 {
		return &bitbucketReadError{cause: "not found: the credential cannot see the repository or pull request", status: 404}
	}
	for _, marker := range []string{"auth", "unauthori", "forbidden", "permission", "denied", "login", "credential", "scope"} {
		if strings.Contains(text, marker) {
			return &bitbucketReadError{cause: bitbucketCauseAuth}
		}
	}
	code := envelope.Error.Code
	if !twgErrorCodePattern.MatchString(code) {
		code = "unrecognized"
	}
	return &bitbucketReadError{cause: "TWG reported error " + code + "; inspect the operation locally and retry"}
}

// twgSinglePullRequest returns the one pull-request resource in a TWG reply.
// Completeness metadata is checked at every level that can carry it: the
// envelope, the batch item and the resource itself (twgComplete). A batch
// reply must hold exactly one item. Its ok decides the outcome: false is a
// classified TWG failure, a missing ok is incomplete, and any other non-true
// value is malformed. An item input given as a string must equal the
// requested number. PullRequest then pins the resource id and repository.
func twgSinglePullRequest(out []byte, number string) ([]byte, error) {
	return twgSingleBatchData(out, number, false)
}

// twgSingleBatchData is twgSinglePullRequest; list allows the item's data to
// be a JSON array (list operations such as activity and comments).
func twgSingleBatchData(out []byte, number string, list bool) ([]byte, error) {
	top, err := twgObject(out)
	if err != nil {
		return nil, err
	}
	if err := twgComplete(top); err != nil {
		return nil, err
	}
	rawItems, batch := top["items"]
	if !batch {
		return out, nil
	}
	var items []json.RawMessage
	if json.Unmarshal(rawItems, &items) != nil || items == nil {
		return nil, &bitbucketReadError{cause: bitbucketCauseMalformed}
	}
	if len(items) != 1 {
		return nil, &bitbucketReadError{cause: bitbucketCauseIncomplete}
	}
	item, err := twgObject(items[0])
	if err != nil {
		return nil, &bitbucketReadError{cause: bitbucketCauseMalformed}
	}
	switch strings.TrimSpace(string(item["ok"])) {
	case "true":
	case "false":
		var failure *twgError
		if json.Unmarshal(item["error"], &failure) != nil {
			failure = nil
		}
		return nil, classifyTWGError(failure)
	case "":
		return nil, &bitbucketReadError{cause: bitbucketCauseIncomplete}
	default:
		return nil, &bitbucketReadError{cause: bitbucketCauseMalformed}
	}
	if err := twgComplete(item); err != nil {
		return nil, err
	}
	if raw, ok := item["input"]; ok {
		var input string
		if json.Unmarshal(raw, &input) == nil && input != "" && input != number {
			return nil, &bitbucketReadError{cause: bitbucketCauseIdentity}
		}
	}
	data, ok := item["data"]
	if !ok {
		return nil, &bitbucketReadError{cause: bitbucketCauseIncomplete}
	}
	if trimmed := bytes.TrimSpace(data); list && len(trimmed) > 0 && trimmed[0] == '[' {
		return trimmed, nil
	}
	resource, err := twgObject(data)
	if err != nil {
		return nil, &bitbucketReadError{cause: bitbucketCauseIncomplete}
	}
	if err := twgComplete(resource); err != nil {
		return nil, err
	}
	return data, nil
}

// twgObject decodes one JSON object. null, arrays and scalars are refused.
func twgObject(raw []byte) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		return nil, &bitbucketReadError{cause: bitbucketCauseInvalidJSON}
	}
	if object == nil {
		return nil, &bitbucketReadError{cause: bitbucketCauseIncomplete}
	}
	return object, nil
}

// twgComplete fails closed on completeness metadata at one level of a reply.
// Only the explicitly complete forms pass: partial absent or false, _failures
// absent, null or empty, ok absent or true, and error absent or null. A true
// partial or a non-empty _failures is incomplete; a wrong type, or an error
// beside a success, is malformed. An ok false that reaches this check is
// reported as incomplete. Envelope failures are normally classified first by
// twgFailure and batch-item failures by twgSinglePullRequest; what still
// arrives here is a resource-level ok false, or an envelope ok false whose
// error object does not decode.
func twgComplete(object map[string]json.RawMessage) error {
	if raw, ok := object["partial"]; ok {
		switch strings.TrimSpace(string(raw)) {
		case "false":
		case "true":
			return &bitbucketReadError{cause: bitbucketCauseIncomplete}
		default:
			return &bitbucketReadError{cause: bitbucketCauseMalformed}
		}
	}
	if raw, ok := object["_failures"]; ok && strings.TrimSpace(string(raw)) != "null" {
		var failures []json.RawMessage
		if json.Unmarshal(raw, &failures) != nil {
			return &bitbucketReadError{cause: bitbucketCauseMalformed}
		}
		if len(failures) > 0 {
			return &bitbucketReadError{cause: bitbucketCauseIncomplete}
		}
	}
	if raw, ok := object["ok"]; ok {
		switch strings.TrimSpace(string(raw)) {
		case "true":
		case "false":
			return &bitbucketReadError{cause: bitbucketCauseIncomplete}
		default:
			return &bitbucketReadError{cause: bitbucketCauseMalformed}
		}
	}
	if raw, ok := object["error"]; ok && strings.TrimSpace(string(raw)) != "null" {
		return &bitbucketReadError{cause: bitbucketCauseMalformed}
	}
	return nil
}

// bitbucketCommand runs one owned client process under a deadline. Cancelling
// the deadline kills that process; descendants it starts are its own concern.
func bitbucketCommand(timeout time.Duration, env []string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.WaitDelay = 200 * time.Millisecond
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	out, err := forgeOutput(cmd)
	if ctx.Err() != nil {
		return out, fmt.Errorf("%s timed out after %s", commandLabel(args[0]), timeout)
	}
	return out, err
}

// commandLabel keeps a resolved executable path out of diagnostics.
func commandLabel(name string) string {
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		return name[i+1:]
	}
	return name
}

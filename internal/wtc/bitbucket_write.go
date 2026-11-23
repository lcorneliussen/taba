package wtc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Bitbucket writes (issue #471).
//
// A write takes exactly one route, chosen before anything is sent:
//
//   - On a gateway whose Bitbucket plugin keeps personal logins away from
//     commands (BITBUCKET_PUBLISH=gateway), writes go to the person through
//     the plugin's confirmed pull request writes; nothing here sends them.
//   - Explicit personal REST credentials in the environment (BB_API_USER and
//     BB_API_PASS, or BB_USERNAME and BB_API_TOKEN) keep the existing REST
//     path for comments.
//   - The deprecated bb client, when it is selected explicitly.
//   - Otherwise the person's own TWG login.
//
// The host read credential is never used for a write, and a failed or
// uncertain write is never retried, through the same route or another.

type bitbucketWriteRoute string

const (
	bitbucketWriteBroker bitbucketWriteRoute = "gateway"
	bitbucketWriteREST   bitbucketWriteRoute = "rest"
	bitbucketWriteBB     bitbucketWriteRoute = "bb"
	bitbucketWriteTWG    bitbucketWriteRoute = "twg"
)

// errBitbucketBrokered is returned by a direct write on a gateway, where the
// person carries writes out through the plugin instead.
var errBitbucketBrokered = errors.New("Bitbucket writes on this gateway are made by the person who confirms them in the Control UI; this command cannot write directly")

func bitbucketGateway() bool { return os.Getenv("BITBUCKET_PUBLISH") == "gateway" }

func bitbucketRESTWriteCredentials() (string, string, bool) {
	for _, pair := range [][2]string{{"BB_API_USER", "BB_API_PASS"}, {"BB_USERNAME", "BB_API_TOKEN"}} {
		if user, pass := os.Getenv(pair[0]), os.Getenv(pair[1]); user != "" && pass != "" {
			return user, pass, true
		}
	}
	return "", "", false
}

// selectedBitbucketWrite resolves the route for a comment write (rest true)
// or for an action REST does not cover (rest false).
func selectedBitbucketWrite(root string, rest bool) (bitbucketWriteRoute, error) {
	if bitbucketGateway() {
		return bitbucketWriteBroker, nil
	}
	if _, _, ok := bitbucketRESTWriteCredentials(); rest && ok {
		return bitbucketWriteREST, nil
	}
	if root == "" {
		root, _ = os.LookupEnv("TABA_CONFIG_ROOT")
	}
	selection, err := selectedBitbucketClient(root)
	if err != nil {
		return "", &bitbucketReadError{bitbucketSelectionCause(err, selection.Source)}
	}
	if selection.Client == bitbucketClientBB {
		return bitbucketWriteBB, nil
	}
	return bitbucketWriteTWG, nil
}

// bitbucketWriteTimeout bounds one write child. A write that times out has an
// unknown outcome; callers keep it pending.
var bitbucketWriteTimeout = 60 * time.Second

// twgWrite runs one TWG write as the person. Only the TWG agent defaults are
// added to its environment: never a host read credential.
func twgWrite(workspace, repo, operation string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), bitbucketWriteTimeout)
	defer cancel()
	reader := twgBitbucketReader{run: func(env []string, args ...string) ([]byte, error) {
		return bitbucketReadRun(ctx, env, "twg --version", args...)
	}}
	if err := reader.checkVersion(); err != nil {
		return nil, bitbucketOperationError(operation, err)
	}
	full := append([]string{"twg"}, args...)
	full = append(full, "--workspace", workspace, "--repo", repo, "--output", "json", "--output-summary", "none")
	cmd := exec.CommandContext(ctx, full[0], full[1:]...)
	cmd.WaitDelay = 200 * time.Millisecond
	// The person's own environment and TWG's agent defaults; a host read
	// credential is never in either.
	cmd.Env = append(os.Environ(), twgChildEnv()...)
	stderr := &boundedBuffer{limit: 8192}
	cmd.Stderr = stderr
	out, err := forgeOutput(cmd)
	if ctx.Err() != nil {
		return nil, fmt.Errorf("%s timed out after %s; its outcome is unknown", operation, bitbucketWriteTimeout)
	}
	if failure, ok := twgFailure(out); ok {
		return nil, bitbucketOperationError(operation, failure)
	}
	if errors.Is(err, exec.ErrNotFound) {
		return nil, fmt.Errorf("%s: twg is not installed", operation)
	}
	if err != nil {
		return nil, bitbucketOperationError(operation, err)
	}
	return out, nil
}

// twgWriteResource unwraps a successful TWG write reply to the Bitbucket
// resource it returned: bare, {"data": …}, or a one-item batch.
func twgWriteResource(out []byte) ([]byte, error) {
	top, err := twgObject(out)
	if err != nil {
		return nil, err
	}
	if err := twgComplete(top); err != nil {
		return nil, err
	}
	if raw, ok := top["items"]; ok {
		var items []json.RawMessage
		if json.Unmarshal(raw, &items) != nil || len(items) != 1 {
			return nil, &bitbucketReadError{bitbucketCauseMalformed}
		}
		item, err := twgObject(items[0])
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(string(item["ok"])) != "true" {
			return nil, &bitbucketReadError{bitbucketCauseIncomplete}
		}
		out = item["data"]
	} else if raw, ok := top["data"]; ok {
		out = raw
	}
	if _, err := twgObject(out); err != nil {
		return nil, &bitbucketReadError{bitbucketCauseMissingField}
	}
	return out, nil
}

// twgCommentCreate creates a comment (summary, inline, or reply) as the
// person and returns its exact id. Any uncertainty is an error, so the
// caller keeps the create pending.
func twgCommentCreate(m ReviewManifest, body, file string, line int, parent string) (ReviewPostResult, error) {
	workspace, repo, err := splitBitbucketSlug(m.Slug)
	if err != nil {
		return ReviewPostResult{}, err
	}
	args := []string{"bitbucket", "pull-requests", "comment", "create", "--pull-request", m.PR, "--text=" + body}
	if file != "" {
		args = append(args, "--path", file, "--line", strconv.Itoa(line))
	}
	if parent != "" {
		args = append(args, "--reply-to", parent)
	}
	out, err := twgWrite(workspace, repo, "twg bitbucket pull-requests comment create", args...)
	if err != nil {
		return ReviewPostResult{}, err
	}
	resource, err := twgWriteResource(out)
	if err != nil {
		return ReviewPostResult{}, fmt.Errorf("twg bitbucket pull-requests comment create: %w; the comment may exist", err)
	}
	return parseBitbucketCreatedComment(resource, m.PR, "")
}

func twgCommentUpdate(m ReviewManifest, id, body string) (ReviewPostResult, error) {
	workspace, repo, err := splitBitbucketSlug(m.Slug)
	if err != nil {
		return ReviewPostResult{}, err
	}
	out, err := twgWrite(workspace, repo, "twg bitbucket pull-requests comment update",
		"bitbucket", "pull-requests", "comment", "update", "--pull-request", m.PR, "--comment", id, "--text="+body)
	if err != nil {
		return ReviewPostResult{}, err
	}
	if resource, err := twgWriteResource(out); err == nil {
		// When TWG returns the comment, it must be the one updated.
		if result, err := parseBitbucketCreatedComment(resource, m.PR, id); err == nil {
			return result, nil
		} else if !strings.Contains(err.Error(), "has no id") {
			return ReviewPostResult{}, err
		}
	}
	return ReviewPostResult{CommentID: id, URL: m.URL}, nil
}

func twgCommentResolve(m ReviewManifest, id string) error {
	workspace, repo, err := splitBitbucketSlug(m.Slug)
	if err != nil {
		return err
	}
	_, err = twgWrite(workspace, repo, "twg bitbucket pull-requests comment resolve",
		"bitbucket", "pull-requests", "comment", "resolve", "--pull-request", m.PR, "--comment", id)
	return err
}

// BitbucketMarkReady un-drafts a pull request through the selected write
// route. root is the control root; head is the reviewed PR head, which a
// gateway's confirmed write pins.
func BitbucketMarkReady(root, worktree, slug, number, head string) error {
	route, err := selectedBitbucketWrite(root, false)
	if err != nil {
		return err
	}
	workspace, repo, err := splitBitbucketSlug(slug)
	if err != nil {
		return err
	}
	switch route {
	case bitbucketWriteBroker:
		receipt, err := brokeredWrites(worktree, slug, number, head, "taba-ready-"+brokeredHash("ready-v1", slug, number, head), []brokeredOperation{{Op: "pr.ready"}})
		if err != nil {
			return err
		}
		if receipt.Results[0].Status != "done" {
			return fmt.Errorf("the confirmed un-draft of PR #%s ended %s; check the pull request", number, receipt.Results[0].Status)
		}
		return nil
	case bitbucketWriteBB:
		cmd := exec.Command("bb", "pr", "ready", number)
		cmd.Dir = worktree
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
		return cmd.Run()
	}
	_, err = twgWrite(workspace, repo, "twg bitbucket pull-requests update",
		"bitbucket", "pull-requests", "update", "--pull-request", number, "--ready")
	return err
}

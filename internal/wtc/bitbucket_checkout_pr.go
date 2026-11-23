package wtc

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"taba.sh/taba/internal/envcompat"
	"time"
)

// BitbucketCheckoutPR is a branch's pull request as a gateway's Collection
// card and its pull request news see it (#471).
type BitbucketCheckoutPR struct {
	Number      int
	URL         string
	State       string // open, merged or closed (declined or superseded)
	Draft       bool
	Title       string
	Checks      string // success, failure, pending, none, or unknown when not read
	Review      string // approved, changes_requested, or empty
	Head        string
	MergeCommit string
}

// BitbucketBranchPullRequest finds branch's pull request in slug — the open
// one, else the latest merged, declined or superseded one — and reads its
// state, review and head checks through the selected client and read
// credential, in one read operation. root is the control root. nil with a
// nil error means the branch has no pull request; an error means it could
// not be read, which is never reported as "none".
func BitbucketBranchPullRequest(root, slug, branch string) (*BitbucketCheckoutPR, error) {
	read := BitbucketBranchRead(root, slug, branch)
	return read.PR, read.Err
}

// BitbucketBranchReadResult is one branch lookup with the non-secret identity
// of the read context that made it and when it completed, so news can tell
// one read identity from another and drop a result older than one already
// seen (#471).
type BitbucketBranchReadResult struct {
	PR         *BitbucketCheckoutPR
	Err        error
	Identity   string // "k:<digest>" for a fingerprinted context, "u:<digest>" for an unknown one
	ObservedAt time.Time
}

// BitbucketBranchRead is BitbucketBranchPullRequest with its read identity.
func BitbucketBranchRead(root, slug, branch string) BitbucketBranchReadResult {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	ctx = statusAuthOperation(bitbucketReadOperation(ctx, root))
	pr, err := bitbucketBranchPullRequest(ctx, slug, branch)
	return BitbucketBranchReadResult{PR: pr, Err: err, Identity: bitbucketReadIdentity(ctx, slug), ObservedAt: time.Now().UTC()}
}

// bitbucketReadIdentity names the read context of slug without revealing
// it. A context that authenticates with a value taba sees (a host token, or
// TWG_BBC_TOKEN in the environment) is "k:" plus an HMAC of the credential
// type, repository, user and token under the read identity key: stable while
// that credential is unchanged, whatever happens to the disposable status
// cache, and different after rotation, for another principal, repository or
// control root. Any other context (a TWG profile login, an unavailable
// credential or key) is "u:" plus a digest of the selection only, which never
// equals a "k:" identity. The helper answer is the operation's memoized one.
func bitbucketReadIdentity(ctx context.Context, slug string) string {
	read := bitbucketReadFor(ctx)
	selection := read.selection
	digest := ""
	switch {
	case selection.err != nil:
	case selection.hostCredential():
		if workspace, repo, err := splitBitbucketSlug(slug); err == nil {
			if user, token, err := read.repoCredential(ctx, workspace, repo); err == nil && token != "" {
				digest = bitbucketReadIdentityDigest("bitbucket-host-read-v1", selection.Credential, strings.ToLower(slug), user, token)
			}
		}
	case selection.Client == bitbucketClientTWG:
		if token := os.Getenv("TWG_BBC_TOKEN"); token != "" {
			digest = bitbucketReadIdentityDigest("bitbucket-twg-env-v1", strings.ToLower(slug), os.Getenv("TWG_BBC_USER"), token, os.Getenv("TWG_SITE"))
		}
	}
	if digest != "" {
		return "k:" + digest[:16]
	}
	sum := sha256.Sum256([]byte("read-identity-v1\x00unknown\x00" + selection.fingerprint()))
	return "u:" + hex.EncodeToString(sum[:])[:16]
}

// bitbucketReadIdentityKeyName is the read identity key's file: 32 random
// bytes, mode 0600, created on first use in the control root, or in the
// user's configuration directory (taba/) without one. It lives as long as
// that directory; it is not part of the status cache and is never needed to
// read anything. Deleting it only makes every refusal news once more.
const bitbucketReadIdentityKeyName = ".bitbucket-read-identity-key-v1"

func bitbucketReadIdentityKeyPath() string {
	if root, ok := envcompat.LookupEnv("TABA_CONFIG_ROOT"); ok && root != "" {
		return filepath.Join(root, bitbucketReadIdentityKeyName)
	}
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, "taba", bitbucketReadIdentityKeyName)
}

func bitbucketReadIdentityKey() []byte {
	path := bitbucketReadIdentityKeyPath()
	if path == "" {
		return nil
	}
	if key := statusAuthReadCacheKey(path); key != nil {
		return key
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		return nil // present but not a valid key: never overwritten
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		// Another process created it first: use that one.
		return statusAuthReadCacheKey(path)
	}
	_, err = f.Write(key)
	if closeErr := f.Close(); err != nil || closeErr != nil {
		_ = os.Remove(path)
		return nil
	}
	return key
}

func bitbucketReadIdentityDigest(label string, values ...string) string {
	key := bitbucketReadIdentityKey()
	if len(key) == 0 {
		return ""
	}
	mac := hmac.New(sha256.New, key)
	_, _ = io.WriteString(mac, label)
	for _, value := range values {
		_, _ = mac.Write([]byte{0})
		_, _ = io.WriteString(mac, value)
	}
	return hex.EncodeToString(mac.Sum(nil))
}

func bitbucketBranchPullRequest(ctx context.Context, slug, branch string) (*BitbucketCheckoutPR, error) {
	if bitbucketReadFor(ctx).selection.Client == bitbucketClientBB {
		// bb lists pull requests without a source-branch filter: a lookup
		// through it could only guess, so it is reported, never "no PR".
		return nil, &bitbucketReadError{cause: bitbucketCauseBranchLookup}
	}
	number := ""
	for _, state := range []string{"OPEN", "MERGED", "DECLINED", "SUPERSEDED"} {
		raw, err := bitbucketReadPRsByState(ctx, slug, branch, state, 10)
		if err != nil {
			return nil, err
		}
		var list struct {
			Values []struct {
				ID     json.Number `json:"id"`
				Source struct {
					Branch struct {
						Name string `json:"name"`
					} `json:"branch"`
				} `json:"source"`
			} `json:"values"`
		}
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.UseNumber()
		if decoder.Decode(&list) != nil {
			return nil, &bitbucketReadError{cause: bitbucketCauseInvalidJSON}
		}
		best := 0
		for _, pr := range list.Values {
			// The query filters by source branch; the answer must agree.
			if pr.Source.Branch.Name != "" && pr.Source.Branch.Name != branch {
				return nil, &bitbucketReadError{cause: bitbucketCauseIdentity}
			}
			if n, err := strconv.Atoi(pr.ID.String()); err == nil && n > best {
				best = n
			}
		}
		if best > 0 {
			number = strconv.Itoa(best)
			break
		}
	}
	if number == "" {
		return nil, nil
	}
	raw, err := bitbucketReadPR(ctx, slug, number)
	if err != nil {
		return nil, err
	}
	detail, err := statusBBDetail(raw, PRRecord{Number: number})
	if err != nil {
		return nil, err
	}
	n, _ := strconv.Atoi(number)
	pr := &BitbucketCheckoutPR{Number: n, URL: "https://bitbucket.org/" + slug + "/pull-requests/" + number, Title: detail.Title,
		Head: detail.HeadCommit, MergeCommit: detail.MergeCommit, Checks: "unknown"}
	switch detail.State {
	case "DRAFT":
		pr.State, pr.Draft = "open", true
	case "DECLINED", "SUPERSEDED", "CLOSED":
		// The card's word for a pull request closed without merging.
		pr.State = "closed"
	default:
		pr.State = strings.ToLower(detail.State)
	}
	switch detail.Review {
	case "approved":
		pr.Review = "approved"
	case "changes":
		pr.Review = "changes_requested"
	}
	if pr.State == "open" && detail.HeadCommit != "" {
		evidence, err := statusBBHeadChecksContext(ctx, StatusRefreshBackground, slug, detail.HeadCommit, false)
		if err == nil && evidence.Complete {
			switch evidence.Result {
			case "SUCCESS":
				pr.Checks = "success"
			case "FAILURE":
				pr.Checks = "failure"
			case "PENDING":
				pr.Checks = "pending"
			case "NONE":
				pr.Checks = "none"
			}
		}
	}
	return pr, nil
}

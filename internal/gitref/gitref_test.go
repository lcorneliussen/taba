package gitref

import (
	"os/exec"
	"strings"
	"testing"
)

func TestConstructorsQualify(t *testing.T) {
	for _, c := range []struct {
		ref                 Ref
		full, short, branch string
	}{
		{LocalBranch("main"), "refs/heads/main", "main", "main"},
		{LocalBranch("feature/x"), "refs/heads/feature/x", "feature/x", "feature/x"},
		{RemoteBranch("origin", "release/1"), "refs/remotes/origin/release/1", "origin/release/1", "release/1"},
	} {
		if c.ref.String() != c.full || c.ref.Short() != c.short || c.ref.Branch() != c.branch {
			t.Errorf("%s: short %q branch %q", c.ref, c.ref.Short(), c.ref.Branch())
		}
	}
	if got := RemoteBranch("origin", "main").FetchSpec(); got != "+refs/heads/main:refs/remotes/origin/main" {
		t.Errorf("fetch spec %q", got)
	}
	if !(Ref{}).IsZero() || LocalBranch("main").IsZero() {
		t.Error("IsZero")
	}
}

func TestParse(t *testing.T) {
	for in, want := range map[string]Ref{
		"origin/main":                RemoteBranch("origin", "main"),
		"origin/feature/x":           RemoteBranch("origin", "feature/x"),
		"refs/remotes/upstream/main": RemoteBranch("upstream", "main"),
		"refs/heads/main":            LocalBranch("main"),
	} {
		if got, err := Parse(in); err != nil || got != want {
			t.Errorf("Parse(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if tag, err := Parse("refs/tags/v1"); err != nil || tag.String() != "refs/tags/v1" || tag.Short() != "v1" || tag.Branch() != "" || tag.IsLocalBranch() {
		t.Errorf("Parse(refs/tags/v1) = %v, %v", tag, err)
	}
	for _, in := range []string{"", "main", "upstream/main", "refs/heads/", "refs/remotes/origin", "refs/remotes/origin/",
		"origin/", "origin/-x", "origin/a..b", "origin/a b", "origin/a~1", "origin/a^", "origin/a:b", "origin/x.lock", "origin/@{u}", "HEAD"} {
		if got, err := Parse(in); err == nil {
			t.Errorf("Parse(%q) = %v, want an error", in, got)
		}
	}
}

// A tag of the same name wins over a branch in Git's short-name search. The
// qualified ref still names the branch.
func TestQualifiedRefIsNotShadowedByTag(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "main")
	git("-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-q", "--allow-empty", "-m", "one")
	git("tag", "main")
	git("-c", "user.name=fixture", "-c", "user.email=fixture@example.invalid", "commit", "-q", "--allow-empty", "-m", "two")
	tip := git("rev-parse", "HEAD")
	if short := git("rev-parse", "--verify", "--quiet", "main^{commit}"); short == tip {
		t.Fatalf("git resolved the short name to the branch; the fixture no longer shows the hazard")
	}
	if got := git("rev-parse", "--verify", LocalBranch("main").String()+"^{commit}"); got != tip {
		t.Fatalf("%s resolved to %s, want the branch tip %s", LocalBranch("main"), got, tip)
	}
}

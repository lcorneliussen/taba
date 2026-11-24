// Package gitref names Git refs. A Ref is always fully qualified, so Git
// never resolves it by its short-name search order, in which a tag named
// main wins over the branch main, and origin/main can mean a local branch.
//
// Code builds a Ref with LocalBranch or RemoteBranch, and reads one from
// configuration, a command line or Git output with Parse. Short is for people
// and configuration files, never for Git. Two readers are lenient on purpose:
// OriginBranch accepts a bare branch name of origin, and Revision passes any
// revision Parse refuses to Git unchanged, a bare name included.
package gitref

import (
	"fmt"
	"strings"
)

const (
	heads   = "refs/heads/"
	remotes = "refs/remotes/"
	tags    = "refs/tags/"
)

// Ref is a fully qualified Git ref. The zero Ref names nothing.
type Ref struct{ name string }

// LocalBranch is refs/heads/<branch>.
func LocalBranch(branch string) Ref { return Ref{heads + branch} }

// RemoteBranch is refs/remotes/<remote>/<branch>, the remote-tracking ref a
// fetch from remote writes for its branch.
func RemoteBranch(remote, branch string) Ref { return Ref{remotes + remote + "/" + branch} }

// Parse reads a ref from configuration, a command line or Git output: a fully
// qualified refs/heads/, refs/remotes/<remote>/ or refs/tags/ name, or
// origin/<branch>, the form registries and repository bases use for a branch
// of origin. A bare name is refused, since Git would resolve it by its search
// order.
func Parse(s string) (Ref, error) {
	r := Ref{s}
	switch {
	case strings.HasPrefix(s, heads), strings.HasPrefix(s, tags):
	case strings.HasPrefix(s, remotes):
		if remote, branch, ok := strings.Cut(strings.TrimPrefix(s, remotes), "/"); !ok || remote == "" || branch == "" {
			return Ref{}, fmt.Errorf("%q names no remote branch", s)
		}
	case strings.HasPrefix(s, "origin/"):
		r = RemoteBranch("origin", strings.TrimPrefix(s, "origin/"))
	default:
		return Ref{}, fmt.Errorf("%q is not origin/<branch> or a fully qualified ref", s)
	}
	if r.Short() == "" || !valid(r.name) {
		return Ref{}, fmt.Errorf("%q is not a valid ref name", s)
	}
	return r, nil
}

// OriginBranch is the branch of origin that s names as origin/<branch> or
// as the bare branch name, the two forms command lines and registries accept
// for a base. It does not validate the name. A bare name is taken to be a
// branch of origin; build the Ref to give Git with RemoteBranch.
func OriginBranch(s string) string { return strings.TrimPrefix(s, "origin/") }

// Revision is what to give Git for a revision read from a command line or
// configuration: the fully qualified ref when Parse accepts s, else s as
// given, such as a commit ID or HEAD~1. A bare name such as main also reaches
// Git as given, so Git's search order applies to it: a tag named main wins.
func Revision(s string) string {
	if r, err := Parse(s); err == nil {
		return r.name
	}
	return s
}

// valid applies the rules of git check-ref-format that Parse's input could
// break.
func valid(name string) bool {
	if strings.Contains(name, "..") || strings.Contains(name, "@{") || strings.Contains(name, "//") ||
		strings.HasSuffix(name, "/") || strings.HasSuffix(name, ".") || strings.HasSuffix(name, ".lock") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if strings.HasPrefix(part, ".") || strings.HasPrefix(part, "-") {
			return false
		}
	}
	return !strings.ContainsFunc(name, func(r rune) bool {
		return r < 0x20 || r == 0x7f || strings.ContainsRune(" ~^:?*[\\", r)
	})
}

// String is the fully qualified name, the form to give Git.
func (r Ref) String() string { return r.name }

// IsZero reports whether r names nothing.
func (r Ref) IsZero() bool { return r.name == "" }

// Short is the name for people and configuration: main for a local branch,
// origin/main for a remote one, v1 for a tag. Git may resolve it to another
// ref, so it is never given to Git.
func (r Ref) Short() string {
	for _, prefix := range []string{heads, remotes, tags} {
		if strings.HasPrefix(r.name, prefix) {
			return strings.TrimPrefix(r.name, prefix)
		}
	}
	return r.name
}

// Branch is the branch a local or remote branch ref names, or "".
func (r Ref) Branch() string {
	switch {
	case strings.HasPrefix(r.name, heads):
		return strings.TrimPrefix(r.name, heads)
	case strings.HasPrefix(r.name, remotes):
		_, branch, _ := strings.Cut(strings.TrimPrefix(r.name, remotes), "/")
		return branch
	}
	return ""
}

// IsLocalBranch reports whether r is refs/heads/<branch>.
func (r Ref) IsLocalBranch() bool { return strings.HasPrefix(r.name, heads) }

// RemotePrefix is refs/remotes/<remote>/, the for-each-ref pattern for all
// of remote's branches, also those with a slash in their name.
func RemotePrefix(remote string) string { return remotes + remote + "/" }

// Refspec is the forced refspec +<src>:<dst>.
func Refspec(src, dst Ref) string { return "+" + src.name + ":" + dst.name }

// FetchSpec is the forced refspec that fetches remote branch r from its
// remote: +refs/heads/<branch>:refs/remotes/<remote>/<branch>. A * branch
// maps the whole namespace. r must be a remote branch.
func (r Ref) FetchSpec() string { return Refspec(LocalBranch(r.Branch()), r) }

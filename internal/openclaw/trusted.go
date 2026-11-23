package openclaw

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// Git runs here as the gateway's user, in checkouts an agent can write to,
// including their repository's config. Settings in that config can make git
// run programs: a filter driver while status compares contents, a signature
// program, fsmonitor, hooks, an ssh command, an upload-pack. So every command
// gets the overrides below, and refuses a checkout whose repository or
// worktree config sets anything beyond a short list of keys that only
// describe the checkout. The gateway's own global and system config stay
// trusted: they are the admin's.
var trustedArgs = []string{
	"-c", "core.fsmonitor=false",
	"-c", "core.hooksPath=/dev/null",
	"-c", "log.showSignature=false",
	"-c", "commit.gpgSign=false",
	"-c", "tag.gpgSign=false",
	"-c", "gpg.program=false",
	"-c", "gpg.ssh.program=false",
	"-c", "gpg.x509.program=false",
	// A submodule is a repository of its own, whose config is not checked:
	// git must not run in it on wtc's behalf. These are only defaults, which
	// a committed .gitmodules can override per submodule, so the commands
	// that would recurse also say --no-recurse-submodules or
	// --ignore-submodules.
	"-c", "submodule.recurse=false",
	"-c", "fetch.recurseSubmodules=false",
	"-c", "status.submoduleSummary=false",
	"-c", "diff.ignoreSubmodules=dirty",
}

// The repository and worktree config keys wtc runs git with. The ones the
// overrides above neutralise are listed too: a hooks path set by a JavaScript
// toolchain is common and harmless once hooks are off.
var allowedRepoKey = regexp.MustCompile(`^(` + strings.Join([]string{
	`core\.(repositoryformatversion|filemode|bare|logallrefupdates|ignorecase|precomposeunicode|symlinks|autocrlf|eol|safecrlf|sparsecheckout|sparsecheckoutcone|hookspath|fsmonitor)`,
	`extensions\.[a-z]+`,
	`remote\..+\.(url|pushurl|fetch|tagopt|prune|promisor|partialclonefilter|mirror)`,
	`branch\..+\.(remote|pushremote|merge|rebase|description|` + baseKey + `)`,
	`submodule\..+\.(url|active|branch)`,
	`user\.(name|email)`,
	`(log\.showsignature|commit\.gpgsign|tag\.gpgsign|gpg\.format|gpg\.program|gpg\.ssh\.program|gpg\.x509\.program)`,
	`lfs\.repositoryformatversion`,
	`(fetch\.prune|pull\.rebase|pull\.ff|merge\.conflictstyle|rerere\.enabled|init\.defaultbranch|maintenance\.auto|maintenance\.strategy|gc\.auto)`,
	`(advice|color|index|pack|feature)\..+`,
}, "|") + `)$`)

// UntrustedConfigError is a checkout wtc runs no git in.
type UntrustedConfigError struct{ msg string }

func (e *UntrustedConfigError) Error() string { return e.msg }

// untrustedConfig says which key, if any, of dir's repository or worktree
// config wtc will not run git with. Listing config runs nothing. It is read
// before every command, as an agent can change it between two.
func untrustedConfig(dir string) error {
	var result error
	out, err := exec.Command("git", "-C", dir, "config", "--list", "--show-scope", "-z").Output()
	if err == nil {
		// -z prints scope NUL key LF value NUL per entry.
		fields := strings.Split(string(out), "\x00")
		for i := 0; i+1 < len(fields); i += 2 {
			scope := fields[i]
			if scope != "local" && scope != "worktree" {
				continue
			}
			key, _, _ := strings.Cut(fields[i+1], "\n")
			if !allowedRepoKey.MatchString(strings.ToLower(key)) {
				flag := ""
				if scope == "worktree" {
					flag = " --worktree"
				}
				result = &UntrustedConfigError{fmt.Sprintf("%s: its %s git config sets %s, which can make git run a program, so wtc runs no git there. Remove it with `git config%s --unset-all %s` if you did not set it on purpose", dir, scope, key, flag, key)}
				break
			}
		}
	}
	return result
}

// submoduleArgs pins every submodule .gitmodules names, in the worktree,
// the index or HEAD: never fetched into, and its working tree never looked
// at. A tracked .gitmodules can override the general defaults above for its
// submodules, and git's own subcommands (merge runs stash create) would then
// run git inside them; config for the submodule's name beats .gitmodules.
// Reading .gitmodules runs nothing, given the overrides: the index read
// for :.gitmodules would otherwise ask fsmonitor.
func submoduleArgs(dir string) ([]string, error) {
	top, err := exec.Command("git", append(append([]string{"-C", dir}, trustedArgs...), "rev-parse", "--show-toplevel")...).Output()
	if err != nil {
		return nil, nil
	}
	names := map[string]bool{}
	for _, source := range [][]string{
		{"--file", strings.TrimSpace(string(top)) + "/.gitmodules"},
		{"--blob", ":.gitmodules"},
		{"--blob", "HEAD:.gitmodules"},
	} {
		args := append(append(append(append([]string{"-C", dir}, trustedArgs...), "config"), source...), "--name-only", "--get-regexp", `^submodule\..*\.(path|url)$`)
		out, err := exec.Command("git", args...).Output()
		if err != nil {
			continue
		}
		for _, key := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if name, ok := strings.CutPrefix(key, "submodule."); ok && key != "" {
				names[name[:strings.LastIndex(name, ".")]] = true
			}
		}
	}
	var pins []string
	for name := range names {
		// -c splits at the first =, so such a name could not be pinned.
		if strings.ContainsAny(name, "=\n") {
			return nil, &UntrustedConfigError{fmt.Sprintf("%s: .gitmodules names a submodule %q, which wtc cannot keep git out of, so wtc runs no git there", dir, name)}
		}
		pins = append(pins, "-c", "submodule."+name+".ignore=dirty", "-c", "submodule."+name+".fetchRecurseSubmodules=false")
	}
	return pins, nil
}

// gitCommand is the git command wtc runs in dir: refused when dir's config
// is not one to trust, and with the overrides otherwise.
func gitCommand(ctx context.Context, dir string, args ...string) (*exec.Cmd, error) {
	if err := untrustedConfig(dir); err != nil {
		return nil, err
	}
	pins, err := submoduleArgs(dir)
	if err != nil {
		return nil, err
	}
	full := append(append(append([]string{"-C", dir}, trustedArgs...), pins...), args...)
	return exec.CommandContext(ctx, "git", full...), nil
}

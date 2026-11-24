//go:build ruleguard

// Package lint holds the gocritic ruleguard rules .golangci.yml loads. The
// build tag keeps it out of every build.
package lint

import "github.com/quasilyte/go-ruleguard/dsl"

// gitRefLiteral rejects a Git ref built or taken apart by hand: a string
// literal that is a ref or refspec (origin/main, refs/heads/main), ends in a
// ref prefix to be concatenated, or formats one with %s. Git resolves a short
// name by its own search order, in which a tag named main wins over the branch
// main, and code that adds or trims "origin/" by hand drifts between the
// local, remote and qualified forms. internal/gitref builds every ref.
//
// gogrep matches a literal in a list only at a fixed position, so each list
// position is a pattern of its own. The patterns cover positions 0 to 9 of a
// call or composite literal; a ref literal at position 10 or later is not
// flagged.
func gitRefLiteral(m dsl.Matcher) {
	m.Match(
		`$s + $_`, `$_ + $s`,
		`$_($s, $*_)`, `$_($_, $s, $*_)`, `$_($_, $_, $s, $*_)`, `$_($_, $_, $_, $s, $*_)`, `$_($_, $_, $_, $_, $s, $*_)`,
		`$_($_, $_, $_, $_, $_, $s, $*_)`, `$_($_, $_, $_, $_, $_, $_, $s, $*_)`, `$_($_, $_, $_, $_, $_, $_, $_, $s, $*_)`, `$_($_, $_, $_, $_, $_, $_, $_, $_, $s, $*_)`, `$_($_, $_, $_, $_, $_, $_, $_, $_, $_, $s, $*_)`,
		`$_{$s, $*_}`, `$_{$_, $s, $*_}`, `$_{$_, $_, $s, $*_}`, `$_{$_, $_, $_, $s, $*_}`, `$_{$_, $_, $_, $_, $s, $*_}`,
		`$_{$_, $_, $_, $_, $_, $s, $*_}`, `$_{$_, $_, $_, $_, $_, $_, $s, $*_}`, `$_{$_, $_, $_, $_, $_, $_, $_, $s, $*_}`, `$_{$_, $_, $_, $_, $_, $_, $_, $_, $s, $*_}`, `$_{$_, $_, $_, $_, $_, $_, $_, $_, $_, $s, $*_}`,
		`$_{$_: $s, $*_}`, `$_{$_, $_: $s, $*_}`, `$_{$_, $_, $_: $s, $*_}`, `$_{$_, $_, $_, $_: $s, $*_}`, `$_{$_, $_, $_, $_, $_: $s, $*_}`,
		`$_{$_, $_, $_, $_, $_, $_: $s, $*_}`, `$_{$_, $_, $_, $_, $_, $_, $_: $s, $*_}`, `$_{$_, $_, $_, $_, $_, $_, $_, $_: $s, $*_}`, `$_{$_, $_, $_, $_, $_, $_, $_, $_, $_: $s, $*_}`, `$_{$_, $_, $_, $_, $_, $_, $_, $_, $_, $_: $s, $*_}`,
		`$_ = $s`, `$_ := $s`, `var $_ = $s`, `var $_ $_ = $s`, `const $_ = $s`, `const $_ $_ = $s`,
		`return $s, $*_`, `return $_, $s, $*_`, `return $_, $_, $s, $*_`, `return $_, $_, $_, $s, $*_`, `return $s`,
		`$s == $_`, `$_ == $s`, `$s != $_`, `$_ != $s`,
		`switch $_ { case $s: $*_ }`,
	).
		Where(m["s"].Node.Is(`BasicLit`) && m["s"].Text.Matches("(origin|refs/(heads|remotes|tags))/([\"`]$|%)|^[\"`][+^]?(origin|refs/(heads|remotes|tags))/[^ \"`]*[\"`]$")).
		At(m["s"]).
		Report(`build Git refs with gitref.LocalBranch or gitref.RemoteBranch (String for Git, Short for people); read them with gitref.Parse or gitref.OriginBranch`)
}

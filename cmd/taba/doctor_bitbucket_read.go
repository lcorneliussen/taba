package main

import (
	"os/exec"
	"sort"
	"strings"

	"taba.sh/taba/internal/openclaw"
	"taba.sh/taba/internal/wtc"
)

// doctorBitbucketReadProbe is doctor --bitbucket-read-probe: one bounded read
// per Bitbucket repository through the selected client and credential. Doctor
// never contacts Bitbucket or a credential helper without it (#471).
var doctorBitbucketReadProbe bool

func bitbucketReadRow(d wtc.BitbucketReadDiagnosis) map[string]any {
	return map[string]any{"name": "bitbucket-read " + d.Repo, "ok": d.OK,
		"detail": d.Slug + ": " + d.Client + ", " + d.Credential + "; " + d.Detail, "probed": d.Probed}
}

// bitbucketReadChecks are the collection's rows: one per checked-out
// Bitbucket repository. Advisory: doctor's result does not depend on them.
func bitbucketReadChecks(c *wtc.Context) []map[string]any {
	var rows []map[string]any
	seen := map[string]bool{}
	for _, r := range c.Registry.Repos {
		if seen[r.Name] {
			continue
		}
		seen[r.Name] = true
		_, slug, forge, err := c.ReviewRepo(r.Name)
		if err != nil || forge != "bitbucket" {
			continue
		}
		rows = append(rows, bitbucketReadRow(wtc.BitbucketReadDiagnose(c.ConfigRoot, r.Name, slug, doctorBitbucketReadProbe)))
	}
	return rows
}

// gatewayBitbucketReadChecks are the gateway's rows: one per Bitbucket
// repository among its managed checkouts.
func gatewayBitbucketReadChecks() []doctorCheck {
	all, err := openclaw.ManagedCollections(gatewayStateDir())
	if err != nil {
		return nil
	}
	slugs := map[string]string{}
	for _, paths := range all {
		for _, path := range paths {
			out, err := exec.Command("git", "-C", path, "remote", "get-url", "origin").Output()
			if err != nil {
				continue
			}
			origin := openclaw.ParseOrigin(strings.TrimSpace(string(out)))
			if origin.Forge == openclaw.ForgeBitbucket {
				slugs[origin.Owner+"/"+origin.Repo] = origin.Repo
			}
		}
	}
	keys := make([]string, 0, len(slugs))
	for slug := range slugs {
		keys = append(keys, slug)
	}
	sort.Strings(keys)
	var checks []doctorCheck
	for _, slug := range keys {
		d := wtc.BitbucketReadDiagnose("", slugs[slug], slug, doctorBitbucketReadProbe)
		row := bitbucketReadRow(d)
		checks = append(checks, doctorCheck{Name: row["name"].(string), OK: d.OK, Detail: row["detail"].(string)})
	}
	return checks
}

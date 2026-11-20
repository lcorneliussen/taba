package main

import "taba.sh/taba/internal/wtc"

// bitbucketClientCheck reports the Bitbucket client selection for migrated
// reads. It is advisory: doctor's result does not depend on it. It reads local
// configuration and, only when TWG is selected, the TWG version; it makes no
// forge, account or credential request.
func bitbucketClientCheck(c *wtc.Context) map[string]any {
	ok, detail := wtc.BitbucketClientReport(c.ConfigRoot)
	return map[string]any{"name": "bitbucket-client", "ok": ok, "detail": detail}
}

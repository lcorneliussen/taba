# Branch-discovered PRs in status

When `taba status` finds an open or draft pull request for a checked-out branch,
that PR appears both beside the repository and in the PR inventory. A PR absent
from the collection's enlistment is marked **not enlisted**. The inventory shows
an explicit command, for example:

```sh
taba pr enlist 'widget' 7 --branch 'topic'
```

Run that command to add the PR to the collection's durable local follow-up list.
Status discovery itself does not enlist it, record its merge, or authorize a
checkout change. A discovered row is transient: after switching away from its
branch, it may disappear. Enlist PRs you want the collection to keep following.

Enlisted and discovered entries for the same forge, repository and PR number
share one inventory row. An identical number in another repository or forge is
still a separate PR. Discovery retains the existing branch identity checks,
state freshness and uncertainty; it does not make cached facts current or infer
an open PR from a failed lookup.

The inventory uses the facts already collected for the repository row. This
change adds no automatic enlistment policy or additional metadata lookup. It
does not complete the separate metadata and automatic enlistment work in #378.

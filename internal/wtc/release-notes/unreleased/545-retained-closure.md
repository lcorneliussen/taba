## Status

- **A known closure survives a deferred refresh.** A GitHub PR status had seen
  closed, or a Bitbucket PR it had seen declined, showed only its refresh age
  (such as `d:7m`) in the STATE column once a later refresh was deferred,
  had gone stale or had failed, and looked like an open PR. Status now keeps
  the last known `closed`/`declined` label and strikethrough, with the refresh
  age before the title, until a newer read sees the PR open again. Closed rows
  no longer show check, merge or review signals. A PR that was never observed
  never shows as closed, and closure still grants no actions.
  ([#545](https://github.com/lcorneliussen/taba/issues/545)).

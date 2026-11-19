## Review

- **Trust posted Bitbucket reviews.** `review post` reads the comment id from
  the `bb` `comments add --json` envelope, and `review status` keeps comment ids
  exact and recognises `#comment-<n>` links, so a current passing review now
  opens `review ready`. Responses with missing, conflicting or malformed ids
  stay pending.
  ([#444](https://github.com/lcorneliussen/taba/issues/444)).

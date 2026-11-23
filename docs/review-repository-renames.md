# Review posting after a GitHub repository rename

After a GitHub repository is renamed, reads through its old name keep working,
but GitHub answers a comment write to the old REST path with a redirect that
`gh` does not replay for `POST` or `PATCH` (HTTP 307). A collection whose
registry still names the old repository could build and check a review and
then fail to post it ([#458](https://github.com/lcorneliussen/taba/issues/458)).

## What review posting does

- **Records the repository.** A new GitHub review bundle stores the
  repository's immutable ID (`repository_id` in `manifest.json`) when it is
  built. If the ID cannot be read, the bundle is still built and says so; it
  cannot post after a rename.
- **Verifies before writing.** `review post`, and `review resolve --reply`,
  ask GitHub which repository the configured name and the PR belong to before
  any write or `comment.create-pending` marker. The configured name, the
  repository GitHub reports and the PR's base repository must be one immutable
  ID, and that must be the ID the bundle recorded.
- **Writes under the verified name.** When the repository was renamed under
  the same owner, comments are written to the canonical name and the command
  prints the rename once. Update the registry `remote` to stop the lookup; see
  [Renaming a repository](renaming-repositories.md).
- **Keeps the binding.** The bundle, its head and base, audience and scope are
  not edited. Posting receipts and the trusted review gate stay keyed by the
  configured name, so the gate reads the receipt the post wrote.

## When it refuses

Nothing is written, and no intent marker is created, when:

- the bundle predates recorded repository IDs and the repository was renamed:
  build a fresh review, which records the ID and then posts under the verified
  name;
- the configured name now resolves to a different repository ID than the
  bundle recorded (a reused name), the canonical name is another repository,
  or the PR belongs to another repository;
- the repository moved to another owner: verify the transfer and update the
  registry `remote`;
- GitHub cannot be reached, the credential is refused, or the answer is
  incomplete: check `gh auth status` and network access and retry.

A redirect, a URL, a repository basename, a PR number or matching commits are
never treated as identity. Identity checks, review writes (comments, inline
comments, replies and thread resolution) and the thread lookup before resolving
go to `github.com`, whatever `GH_HOST` says; other review reads keep using
`gh`'s default host.

## Uncertain creates

An existing `comment.create-pending` marker still blocks another create,
renamed repository or not. It records a create whose outcome is unknown: check
the PR's comments for the review, and save its ID in `comment.id` if it exists,
before removing the marker. Review posting never removes it for you and never
retries an uncertain `POST`.

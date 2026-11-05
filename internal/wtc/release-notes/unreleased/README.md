# Unreleased release notes

One file per change, named `<issue>-<slug>.md` after the issue the change
closes (for example `90-release-note-fragments.md`). A branch adds its own
fragment and never edits another's, so branches in flight do not conflict.

A fragment holds one or more topic sections, each with at least one entry:

```md
## Review

- **`wtc review run` works without harness prompt files.** …
  ([#46](https://github.com/lcorneliussen/taba/issues/46)).
```

Use `## <topic>` headings only; `# taba/wtc <version>` is added when the notes are
assembled. Start entries at the left margin and use inline links: reference
definitions are document-wide and would collide with other fragments' labels. `wtc release-notes unreleased` shows the fragments assembled:
sections with the same heading are merged, in issue order. Cutting a release
turns them into `<version>.md` and deletes them; see `docs/release-notes.md`.

This README is not a fragment. It keeps the directory present when nothing is
pending.

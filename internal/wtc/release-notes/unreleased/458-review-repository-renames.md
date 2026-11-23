## Review

- **Post reviews after a GitHub repository rename.** Review posting verifies
  the repository's immutable ID and writes under its new name instead of
  failing with HTTP 307; bundles built before this release must be rebuilt to
  post after a rename, and reused names, other repositories or owners, and
  unverifiable identities are refused before anything is written.
  ([#458](https://github.com/lcorneliussen/taba/issues/458)).

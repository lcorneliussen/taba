## Status

- **Separate authenticated status observations.** Switching forge credentials
  no longer reuses another context's cached metadata. Unresolved credentials
  keep live status within one operation; explicit offline previews remain local
  records rather than current permission evidence.
  ([#441](https://github.com/lcorneliussen/taba/issues/441),
  [#443](https://github.com/lcorneliussen/taba/pull/443)).

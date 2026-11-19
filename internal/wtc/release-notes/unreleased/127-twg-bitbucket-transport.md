## Catch-up

- **Opt-in TWG reads for Bitbucket catch-up.** With
  `TABA_BITBUCKET_TRANSPORT=twg`, catch-up reads enlisted Bitbucket PR state
  through TWG 1.x (1.4.4 or later); unset or `bb` keeps the `bb` CLI. TWG failures
  stay unavailable and never fall back to `bb`. Operation contract:
  [docs/forges.md](https://github.com/lcorneliussen/taba/blob/main/docs/forges.md)
  ([#127](https://github.com/lcorneliussen/taba/issues/127)).

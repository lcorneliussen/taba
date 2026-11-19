## Catch-up

- **Choose the Bitbucket client once per machine.** `TABA_BITBUCKET_TRANSPORT`
  can now be set in the control root's `taba.env`, and the environment still
  overrides it. `taba doctor` shows the selected client, where the setting
  came from and, for TWG, whether the installed version is supported; that row
  is advisory and never fails doctor. The default stays `bb`.
  ([#127](https://github.com/lcorneliussen/taba/issues/127)).

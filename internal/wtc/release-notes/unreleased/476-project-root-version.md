## Collections

- **Run the project's CLI from the project root.** `taba env root` derives a
  root tool selection from the harness pin at the harness's declared default
  ref, with no global default and no second pin to maintain. `taba new` then
  works from the root, and `--check` reports the source, the root selection and
  collection selections. See `docs/project-root-version.md`
  ([#476](https://github.com/lcorneliussen/taba/issues/476)).

## Configuration

- **Inspect requirements from canonical dotenv examples.** Doctor, `config list`
  and explicit `secrets list --requirements` share a value-free inventory, while
  the default secrets listing keeps its filename/link-state contract.
  `config lint` validates schemas and provided owned key names without requiring
  credentials; process inspection is explicit.
  ([#263](https://github.com/lcorneliussen/taba/issues/263),
  [#436](https://github.com/lcorneliussen/taba/pull/436)).

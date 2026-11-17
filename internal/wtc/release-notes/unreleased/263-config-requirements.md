- Doctor diagnoses configuration and secret requirements from opted-in canonical
  dotenv examples. Config listing and explicit `secrets list --requirements`
  share the same value-free inventory; the default secrets listing still reads
  names and link state only.
- `config lint` validates schemas and provided owned inputs without requiring
  credentials, with bounded tracked-file discovery and explicit source selection.
  Process inspection is opt-in. Executable custom checks remain separate work.

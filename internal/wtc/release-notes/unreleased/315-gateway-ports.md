## OpenClaw

- Repair missing repository port metadata in gateway collections on status
  refresh, using the selected project's verified repository identities. Preserve
  explicit local port/alias settings, legacy port names and the allocated port
  block; update only the generated environment without restarting services.
  See [gateway ports](docs/openclaw-gateway-ports.md) (#315).

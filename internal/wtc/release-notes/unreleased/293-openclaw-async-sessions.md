## OpenClaw

- OpenClaw collection discovery, PR watching and sandbox git authorization now
  await the asynchronous session SDK. The plugin keeps support for older hosts
  by selecting legacy readers only when async methods are absent, and never
  retries a failed async read through the deprecated API.
  ([#293](https://github.com/lcorneliussen/taba/issues/293)).

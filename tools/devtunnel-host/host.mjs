// Receives an already-authorized, exact registration and host capability. It
// neither logs in nor creates a persistent tunnel. Management remains separate.
export function validateInput(input) {
  if (!input || typeof input !== "object" || Array.isArray(input) ||
      Object.keys(input).sort().join(",") !== "clusterId,hostId,hostToken,portNumber,schema,tunnelId" || input.schema !== 1 ||
      typeof input.clusterId !== "string" || !/^[a-z0-9]{1,16}$/.test(input.clusterId) ||
      typeof input.tunnelId !== "string" || !/^[a-z0-9][a-z0-9-]{2,59}$/.test(input.tunnelId) ||
      typeof input.hostId !== "string" || !/^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/.test(input.hostId) ||
      !Number.isInteger(input.portNumber) || input.portNumber < 1 || input.portNumber > 65535 ||
      typeof input.hostToken !== "string" || !input.hostToken || input.hostToken.length > 16384 || /[\r\n\0]/.test(input.hostToken)) {
    throw new Error("invalid host input");
  }
  return input;
}

export async function createSdkHost(input, { httpAdapter, streamFactory } = {}) {
  validateInput(input);
  const { TunnelRelayTunnelHost, MultiModeTunnelHost, ConnectionStatus } = await import("@microsoft/dev-tunnels-connections");
  const { TunnelManagementHttpClient } = await import("@microsoft/dev-tunnels-management");
  // The SDK accepts this per-process identity. Never advertise the OS hostname.
  MultiModeTunnelHost.hostId = input.hostId;
  const manager = new TunnelManagementHttpClient("taba-devtunnel-host/1", "2023-09-27-preview", undefined, undefined, undefined, httpAdapter);
  const host = new TunnelRelayTunnelHost(manager, () => {});
  if (streamFactory) host.streamFactory = streamFactory;
  const tunnel = {
    clusterId: input.clusterId, tunnelId: input.tunnelId,
    ports: [{ portNumber: input.portNumber, protocol: "http" }],
    accessTokens: { host: input.hostToken },
  };
  let connected = false;
  let closed;
  const disconnected = new Promise(resolve => { closed = resolve; });
  const subscription = host.connectionStatusChanged(e => {
    if (connected && e.status === ConnectionStatus.Disconnected) closed();
  });
  return {
    async connect() {
      await host.connect(tunnel, { enableReconnect: false, enableRetry: false });
      connected = true;
    },
    disconnected,
    async dispose() { subscription.dispose(); await host.dispose(); },
  };
}

export async function runHost(raw, { signal, emit, factory = createSdkHost }) {
  const input = validateInput(raw);
  if (signal.aborted) throw new Error("host cancelled");
  const host = await factory(input);
  let abort;
  const stopped = new Promise(resolve => { abort = resolve; });
  const onAbort = () => abort();
  signal.addEventListener("abort", onAbort, { once: true });
  if (signal.aborted) abort();
  let started = false;
  try {
    const result = await Promise.race([host.connect().then(() => "connected"), stopped.then(() => "cancelled")]);
    if (result !== "connected" || signal.aborted) throw new Error("host cancelled");
    started = true;
    emit({ schema: 1, event: "started" });
    await Promise.race([stopped, host.disconnected]);
  } finally {
    signal.removeEventListener("abort", onAbort);
    // A failed/uncertain dispose must not announce successful shutdown. The
    // owning journal retains the pending cleanup, including stopped entries.
    await host.dispose();
  }
  if (started) emit({ schema: 1, event: "stopped" });
}

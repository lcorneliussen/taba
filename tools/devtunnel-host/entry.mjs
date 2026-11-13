import { runHost, createSdkHost } from "./host.mjs";
export { runHost, createSdkHost };

async function main() {
  if (Number(process.versions.node.split(".")[0]) < 22) throw new Error("unsupported Node");
  if (process.argv.length === 3 && process.argv[2] === "--check") {
    process.stdout.write('{"schema":1,"helper":"devtunnel-host","sdk":"1.3.62","nodeMajor":22}\n');
    return;
  }
  if (process.argv.length !== 2) throw new Error("invalid arguments");
  const controller = new AbortController();
  const stop = () => controller.abort();
  process.once("SIGTERM", stop);
  process.once("SIGINT", stop);
  try {
    let bytes = 0;
    const chunks = [];
    for await (const chunk of process.stdin) {
      bytes += chunk.length;
      if (bytes > 65536) throw new Error("oversized input");
      chunks.push(chunk);
    }
    const input = JSON.parse(Buffer.concat(chunks).toString("utf8"));
    await runHost(input, { signal: controller.signal, emit: event => process.stdout.write(JSON.stringify(event) + "\n") });
  } finally {
    process.removeListener("SIGTERM", stop);
    process.removeListener("SIGINT", stop);
  }
}

// The bundle is also importable for fake-transport contract tests. No provider
// connection is made by importing it or probing --check.
if (typeof require === "undefined" || require.main === module) {
  main().catch(() => {
    // SDK/JSON errors may contain tokens, request headers or input data.
    process.stderr.write("devtunnel-host: operation failed; reconcile owned state\n");
    process.exitCode = 1;
  });
}

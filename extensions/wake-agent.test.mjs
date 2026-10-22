import assert from "node:assert/strict";
import { existsSync, mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import wakeAgent from "./wake-agent.ts";

const runtimeDir = mkdtempSync(join(tmpdir(), "wake-agent-extension-"));
const sentinel = join(runtimeDir, "sentinel");
process.env.WAKE_AGENT_RUNTIME_DIR = runtimeDir;

const handlers = new Map();
wakeAgent({ on: (event, handler) => handlers.set(event, handler) });
let status;
const ctx = { ui: { setStatus: (_key, value) => { status = value; } } };

writeFileSync(sentinel, "stage=waiting\n");
process.env.UNIT_ACTIVE = "1";
await handlers.get("session_start")({}, ctx);
assert.equal(status, "wake-agent: running");
await handlers.get("session_shutdown")({}, ctx);

writeFileSync(sentinel, "stage=waiting\n");
process.env.UNIT_ACTIVE = "0";
await handlers.get("session_start")({}, ctx);
assert.equal(status, undefined);
assert.equal(existsSync(sentinel), false);
await handlers.get("session_shutdown")({}, ctx);

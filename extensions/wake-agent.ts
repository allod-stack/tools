import { readFileSync, unlinkSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";
import { spawnSync } from "node:child_process";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

const activeStages = new Set([
  "waiting",
  "confirming",
  "running",
  "retrying",
]);

function sentinelPath(): string {
  if (process.env.WAKE_AGENT_RUNTIME_DIR) {
    return join(process.env.WAKE_AGENT_RUNTIME_DIR, "sentinel");
  }
  if (process.env.XDG_RUNTIME_DIR) {
    return join(process.env.XDG_RUNTIME_DIR, "wake-agent", "sentinel");
  }
  if (process.env.WAKE_AGENT_STATE_DIR) {
    return join(process.env.WAKE_AGENT_STATE_DIR, "sentinel");
  }
  return join(homedir(), ".local", "state", "wake-agent", "sentinel");
}

function stage(): string | undefined {
  try {
    const match = readFileSync(sentinelPath(), "utf8").match(/^stage=(.+)$/m);
    return match?.[1];
  } catch {
    return undefined;
  }
}

function unitIsActive(): boolean {
  return spawnSync(
    "systemctl",
    ["--user", "is-active", "--quiet", "wake-agent.service"],
    { stdio: "ignore" },
  ).status === 0;
}

export default function (pi: ExtensionAPI) {
  let timer: ReturnType<typeof setInterval> | undefined;

  pi.on("session_start", async (_event, ctx) => {
    const refresh = () => {
      const sentinel = sentinelPath();
      const currentStage = stage();
      const active = activeStages.has(currentStage ?? "") && unitIsActive();
      if (!active && activeStages.has(currentStage ?? "")) {
        try {
          unlinkSync(sentinel);
        } catch {}
      }
      ctx.ui.setStatus(
        "wake-agent",
        active ? "wake-agent: running" : undefined,
      );
    };
    refresh();
    timer = setInterval(refresh, 2000);
    timer.unref();
  });

  pi.on("session_shutdown", async (_event, ctx) => {
    if (timer) clearInterval(timer);
    ctx.ui.setStatus("wake-agent", undefined);
  });
}

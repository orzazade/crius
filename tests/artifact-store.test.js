import test from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, readFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { ArtifactStore } from "../src/store/artifact-store.js";

test("ArtifactStore initializes a run directory with core artifacts", async () => {
  const root = await mkdtemp(path.join(os.tmpdir(), "crius-store-"));
  const store = new ArtifactStore(root);

  const contract = {
    runId: "run-1",
    currentStage: "planning",
    status: "draft",
    revisionCount: 0,
    workspacePath: "/tmp/workspace",
    worktreePath: "/tmp/worktree",
    updatedAt: "2026-03-28T00:00:00.000Z"
  };

  const runDir = await store.initializeRun(contract, "# Spec");

  assert.equal(runDir, path.join(root, "run-1"));
  assert.equal(JSON.parse(await readFile(path.join(runDir, "contract.json"), "utf8")).runId, "run-1");
  assert.equal(JSON.parse(await readFile(path.join(runDir, "state.json"), "utf8")).status, "draft");
  assert.match(await readFile(path.join(runDir, "spec.md"), "utf8"), /# Spec/);
});


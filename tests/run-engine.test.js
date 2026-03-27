import test from "node:test";
import assert from "node:assert/strict";
import { mkdtemp } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { RunEngine } from "../src/runner/run-engine.js";

test("RunEngine persists the full happy-path lifecycle", async () => {
  const artifactRoot = await mkdtemp(path.join(os.tmpdir(), "crius-run-"));
  const engine = new RunEngine({ artifactRoot });

  const { contract } = await engine.bootstrapRun({
    goal: "Build isolated harness",
    workspacePath: "/tmp/workspace",
    worktreePath: "/tmp/worktree",
    acceptanceCriteria: ["Every run has a contract"],
    deliverables: ["contract", "state", "spec"],
    evaluatorChecks: ["Contract is written"]
  }, new Date("2026-03-28T00:00:00Z"));

  await engine.recordPlan(contract.runId, "1. Create plan", new Date("2026-03-28T00:01:00Z"));
  await engine.recordImplementation(contract.runId, "Implemented core artifacts", new Date("2026-03-28T00:02:00Z"));
  await engine.recordEvaluation(contract.runId, { passed: true, notes: ["Artifacts exist"] }, new Date("2026-03-28T00:03:00Z"));
  await engine.recordHandoff(contract.runId, "Ready for review", new Date("2026-03-28T00:04:00Z"));

  const finalContract = await engine.artifactStore.readJson(contract.runId, "contract.json");
  const finalState = await engine.artifactStore.readJson(contract.runId, "state.json");

  assert.equal(finalContract.status, "completed");
  assert.equal(finalContract.currentStage, "completed");
  assert.equal(finalState.status, "completed");
});

test("RunEngine sends failed evaluation back to implementation", async () => {
  const artifactRoot = await mkdtemp(path.join(os.tmpdir(), "crius-run-"));
  const engine = new RunEngine({ artifactRoot });

  const { contract } = await engine.bootstrapRun({
    goal: "Build isolated harness",
    workspacePath: "/tmp/workspace",
    worktreePath: "/tmp/worktree",
    acceptanceCriteria: ["Every run has a contract"],
    deliverables: ["contract", "state", "spec"],
    evaluatorChecks: ["Contract is written"]
  });

  await engine.recordPlan(contract.runId, "1. Create plan");
  await engine.recordImplementation(contract.runId, "Implemented core artifacts");
  await engine.recordEvaluation(contract.runId, { passed: false, notes: ["Missing handoff"] });

  const updatedContract = await engine.artifactStore.readJson(contract.runId, "contract.json");

  assert.equal(updatedContract.currentStage, "implementation");
  assert.equal(updatedContract.status, "needs_revision");
  assert.equal(updatedContract.revisionCount, 1);
});

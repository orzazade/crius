import test from "node:test";
import assert from "node:assert/strict";
import { createRunContract, createRunId, slugifyGoal } from "../src/contracts/run-contract.js";

test("slugifyGoal normalizes the goal into a stable slug", () => {
  assert.equal(slugifyGoal("Build planner / evaluator loop"), "build-planner-evaluator-loop");
});

test("createRunId combines timestamp and slug", () => {
  const runId = createRunId("Build harness", new Date("2026-03-28T00:00:00Z"));
  assert.equal(runId, "20260328000000-build-harness");
});

test("createRunContract builds a valid contract with defaults", () => {
  const contract = createRunContract({
    goal: "Build harness",
    workspacePath: "/tmp/workspace",
    worktreePath: "/tmp/worktree",
    acceptanceCriteria: ["Has a contract"],
    deliverables: ["spec"],
    evaluatorChecks: ["contract exists"]
  }, new Date("2026-03-28T00:00:00Z"));

  assert.equal(contract.branchName, "codex/build-harness");
  assert.equal(contract.currentStage, "planning");
  assert.equal(contract.status, "draft");
  assert.deepEqual(contract.constraints, ["Do not operate on the live working tree."]);
});

test("createRunContract rejects missing acceptance criteria", () => {
  assert.throws(() => createRunContract({
    goal: "Build harness",
    workspacePath: "/tmp/workspace",
    worktreePath: "/tmp/worktree",
    acceptanceCriteria: [],
    evaluatorChecks: ["contract exists"]
  }));
});


import test from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, readFile, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { execFileSync } from "node:child_process";
import { ProjectImprover } from "../src/improver/project-improver.js";

function git(cwd, args) {
  return execFileSync("git", ["-C", cwd, ...args], { encoding: "utf8" }).trim();
}

async function createTempRepository() {
  const root = await mkdtemp(path.join(os.tmpdir(), "crius-exec-repo-"));
  git(root, ["init", "-b", "main"]);
  git(root, ["config", "user.name", "Crius Test"]);
  git(root, ["config", "user.email", "crius@example.com"]);
  await writeFile(path.join(root, "README.md"), "# Temp Repo\n", "utf8");
  git(root, ["add", "README.md"]);
  git(root, ["commit", "-m", "init"]);
  return root;
}

class FakeProviderRunner {
  async run({ stage, cwd }) {
    if (stage === "planning") {
      return {
        provider: "fake",
        responsePath: path.join(cwd, "planning-output.txt"),
        finishedAt: new Date("2026-03-28T00:01:00Z").toISOString(),
        durationMs: 10,
        output: [
          "# Improvement",
          "Add an implementation marker.",
          "",
          "# Rationale",
          "Proves the stage pipeline can modify the isolated worktree.",
          "",
          "# Files To Touch",
          "- README.md",
          "",
          "# Verification",
          "- Inspect README.md",
          "",
          "# Risks",
          "- Minimal test fixture only"
        ].join("\n")
      };
    }

    if (stage === "implementation") {
      await writeFile(path.join(cwd, "README.md"), "# Temp Repo\n\nImproved by Crius.\n", "utf8");
      return {
        provider: "fake",
        responsePath: path.join(cwd, "implementation-output.txt"),
        finishedAt: new Date("2026-03-28T00:02:00Z").toISOString(),
        durationMs: 10,
        output: [
          "# Summary",
          "Updated README.md in the isolated worktree.",
          "",
          "# Files Changed",
          "- README.md",
          "",
          "# Verification",
          "- README.md contains the marker text",
          "",
          "# Risks",
          "- None"
        ].join("\n")
      };
    }

    if (stage === "evaluation") {
      return {
        provider: "fake",
        responsePath: path.join(cwd, "evaluation-output.json"),
        finishedAt: new Date("2026-03-28T00:03:00Z").toISOString(),
        durationMs: 10,
        output: {
          passed: true,
          summary: "The change matches the plan.",
          issues: [],
          evidence: ["README.md contains the marker text"],
          suggestedNextStep: "Open a PR."
        }
      };
    }

    throw new Error(`Unexpected stage: ${stage}`);
  }
}

test("ProjectImprover can execute a full staged run with a provider runner", async () => {
  const repositoryPath = await createTempRepository();
  const artifactRoot = await mkdtemp(path.join(os.tmpdir(), "crius-exec-artifacts-"));
  const worktreeRoot = await mkdtemp(path.join(os.tmpdir(), "crius-exec-worktrees-"));
  const improver = new ProjectImprover({
    artifactRoot,
    worktreeRoot,
    providerRunner: new FakeProviderRunner()
  });

  const { contract } = await improver.initializeRun({
    repositoryPath,
    goal: "Add a marker change",
    implementationProvider: "codex",
    evaluatorProvider: "claude"
  }, new Date("2026-03-28T00:00:00Z"));

  const result = await improver.executeRun(contract.runId, { maxRevisions: 1 });
  const finalContract = await improver.artifactStore.readJson(contract.runId, "contract.json");
  const finalState = await improver.artifactStore.readJson(contract.runId, "state.json");
  const handoff = await improver.artifactStore.readText(contract.runId, "handoff.md");
  const diffPatch = await improver.artifactStore.readText(contract.runId, "implementation-attempt-1.diff.patch");
  const worktree = await improver.artifactStore.readJson(contract.runId, "worktree.json");
  const worktreeReadme = await readFile(path.join(worktree.worktreePath, "README.md"), "utf8");

  assert.equal(result.status, "completed");
  assert.equal(finalContract.currentStage, "completed");
  assert.equal(finalState.status, "completed");
  assert.equal(finalState.attempts.planning, 1);
  assert.equal(finalState.attempts.implementation, 1);
  assert.equal(finalState.attempts.evaluation, 1);
  assert.match(handoff, /Open a PR/);
  assert.match(diffPatch, /Improved by Crius/);
  assert.match(worktreeReadme, /Improved by Crius/);
});

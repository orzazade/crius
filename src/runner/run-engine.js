import path from "node:path";
import { ArtifactStore } from "../store/artifact-store.js";
import { assertRunContract, createRunContract } from "../contracts/run-contract.js";

function renderSpec(contract) {
  const lines = [
    `# Run ${contract.runId}`,
    "",
    `## Goal`,
    contract.goal,
    "",
    `## Workspace`,
    `- Workspace: ${contract.workspacePath}`,
    `- Isolated worktree: ${contract.worktreePath}`,
    `- Branch: ${contract.branchName}`,
    "",
    `## Constraints`,
    ...contract.constraints.map((item) => `- ${item}`),
    "",
    `## Acceptance Criteria`,
    ...contract.acceptanceCriteria.map((item) => `- ${item}`),
    "",
    `## Deliverables`,
    ...contract.deliverables.map((item) => `- ${item}`),
    "",
    `## Evaluator Checks`,
    ...contract.evaluatorChecks.map((item) => `- ${item}`)
  ];

  return lines.join("\n");
}

function transitionState(currentStage, nextStage) {
  const validTransitions = {
    planning: "implementation",
    implementation: "evaluation",
    evaluation: ["implementation", "handoff"],
    handoff: "completed"
  };

  const allowed = validTransitions[currentStage];
  const ok = Array.isArray(allowed) ? allowed.includes(nextStage) : allowed === nextStage;
  if (!ok) {
    throw new Error(`Invalid stage transition: ${currentStage} -> ${nextStage}`);
  }
}

export class RunEngine {
  constructor({ artifactRoot = path.join(process.cwd(), ".crius", "runs") } = {}) {
    this.artifactStore = new ArtifactStore(artifactRoot);
  }

  async bootstrapRun(input, now = new Date()) {
    const contract = assertRunContract(createRunContract(input, now));
    const runDir = await this.artifactStore.initializeRun(contract, renderSpec(contract));
    return { contract, runDir };
  }

  async recordPlan(runId, planMarkdown, now = new Date()) {
    const contract = await this.artifactStore.readJson(runId, "contract.json");
    const state = await this.artifactStore.readJson(runId, "state.json");
    transitionState(state.currentStage, "implementation");

    contract.currentStage = "implementation";
    contract.status = "planned";
    contract.updatedAt = now.toISOString();

    state.currentStage = "implementation";
    state.status = "planned";
    state.updatedAt = contract.updatedAt;

    await Promise.all([
      this.artifactStore.writeText(runId, "plan.md", planMarkdown),
      this.artifactStore.updateContract(contract),
      this.artifactStore.updateState(runId, state)
    ]);
  }

  async recordImplementation(runId, implementationMarkdown, now = new Date()) {
    const contract = await this.artifactStore.readJson(runId, "contract.json");
    const state = await this.artifactStore.readJson(runId, "state.json");
    transitionState(state.currentStage, "evaluation");

    contract.currentStage = "evaluation";
    contract.status = "implemented";
    contract.updatedAt = now.toISOString();

    state.currentStage = "evaluation";
    state.status = "implemented";
    state.updatedAt = contract.updatedAt;

    await Promise.all([
      this.artifactStore.writeText(runId, "implementation.md", implementationMarkdown),
      this.artifactStore.updateContract(contract),
      this.artifactStore.updateState(runId, state)
    ]);
  }

  async recordEvaluation(runId, evaluation, now = new Date()) {
    if (typeof evaluation !== "object" || evaluation === null || typeof evaluation.passed !== "boolean") {
      throw new Error("evaluation must be an object with a boolean passed field");
    }

    const contract = await this.artifactStore.readJson(runId, "contract.json");
    const state = await this.artifactStore.readJson(runId, "state.json");
    const nextStage = evaluation.passed ? "handoff" : "implementation";

    transitionState(state.currentStage, nextStage);

    contract.currentStage = nextStage;
    contract.status = evaluation.passed ? "approved" : "needs_revision";
    contract.revisionCount = evaluation.passed ? contract.revisionCount : contract.revisionCount + 1;
    contract.updatedAt = now.toISOString();

    state.currentStage = nextStage;
    state.status = contract.status;
    state.revisionCount = contract.revisionCount;
    state.updatedAt = contract.updatedAt;

    await Promise.all([
      this.artifactStore.writeJson(runId, "evaluation.json", evaluation),
      this.artifactStore.updateContract(contract),
      this.artifactStore.updateState(runId, state)
    ]);
  }

  async recordHandoff(runId, handoffMarkdown, now = new Date()) {
    const contract = await this.artifactStore.readJson(runId, "contract.json");
    const state = await this.artifactStore.readJson(runId, "state.json");
    transitionState(state.currentStage, "completed");

    contract.currentStage = "completed";
    contract.status = "completed";
    contract.updatedAt = now.toISOString();

    state.currentStage = "completed";
    state.status = "completed";
    state.updatedAt = contract.updatedAt;

    await Promise.all([
      this.artifactStore.writeText(runId, "handoff.md", handoffMarkdown),
      this.artifactStore.updateContract(contract),
      this.artifactStore.updateState(runId, state)
    ]);
  }
}


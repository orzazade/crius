const RUN_ID_SANITIZER = /[^a-z0-9-]/g;

export const RUN_STAGES = ["planning", "implementation", "evaluation", "handoff", "completed"];

function ensureNonEmptyString(value, field) {
  if (typeof value !== "string" || value.trim() === "") {
    throw new Error(`${field} must be a non-empty string`);
  }
  return value.trim();
}

function ensureStringArray(value, field) {
  if (!Array.isArray(value) || value.length === 0) {
    throw new Error(`${field} must be a non-empty array`);
  }

  const normalized = value.map((entry, index) => {
    if (typeof entry !== "string" || entry.trim() === "") {
      throw new Error(`${field}[${index}] must be a non-empty string`);
    }
    return entry.trim();
  });

  return normalized;
}

export function slugifyGoal(goal) {
  return ensureNonEmptyString(goal, "goal")
    .toLowerCase()
    .replace(/\s+/g, "-")
    .replace(RUN_ID_SANITIZER, "")
    .replace(/-+/g, "-")
    .replace(/^-|-$/g, "")
    .slice(0, 48);
}

export function createRunId(goal, now = new Date()) {
  const timestamp = now.toISOString().replace(/[-:.TZ]/g, "").slice(0, 14);
  return `${timestamp}-${slugifyGoal(goal)}`;
}

export function createRunContract(input, now = new Date()) {
  const goal = ensureNonEmptyString(input.goal, "goal");
  const workspacePath = ensureNonEmptyString(input.workspacePath, "workspacePath");
  const worktreePath = ensureNonEmptyString(input.worktreePath, "worktreePath");
  const constraints = ensureStringArray(input.constraints ?? ["Do not operate on the live working tree."], "constraints");
  const acceptanceCriteria = ensureStringArray(input.acceptanceCriteria, "acceptanceCriteria");
  const deliverables = ensureStringArray(input.deliverables ?? ["Implementation summary"], "deliverables");
  const evaluatorChecks = ensureStringArray(input.evaluatorChecks, "evaluatorChecks");

  const createdAt = now.toISOString();
  const runId = input.runId ? ensureNonEmptyString(input.runId, "runId") : createRunId(goal, now);

  return {
    version: 1,
    runId,
    goal,
    workspacePath,
    worktreePath,
    branchName: input.branchName ? ensureNonEmptyString(input.branchName, "branchName") : `codex/${slugifyGoal(goal)}`,
    constraints,
    acceptanceCriteria,
    deliverables,
    evaluatorChecks,
    currentStage: "planning",
    status: "draft",
    revisionCount: 0,
    createdAt,
    updatedAt: createdAt
  };
}

export function assertRunContract(contract) {
  ensureNonEmptyString(contract.runId, "runId");
  ensureNonEmptyString(contract.goal, "goal");
  ensureNonEmptyString(contract.workspacePath, "workspacePath");
  ensureNonEmptyString(contract.worktreePath, "worktreePath");
  ensureNonEmptyString(contract.branchName, "branchName");
  ensureStringArray(contract.constraints, "constraints");
  ensureStringArray(contract.acceptanceCriteria, "acceptanceCriteria");
  ensureStringArray(contract.deliverables, "deliverables");
  ensureStringArray(contract.evaluatorChecks, "evaluatorChecks");

  if (!RUN_STAGES.includes(contract.currentStage)) {
    throw new Error(`currentStage must be one of: ${RUN_STAGES.join(", ")}`);
  }

  return contract;
}


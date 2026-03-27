function section(title, body) {
  return [`## ${title}`, body, ""];
}

export function buildPlanningPrompt({ contract, repository, plannerBrief }) {
  return [
    "# Role",
    "You are the planning agent for a long-running project-improver harness.",
    "You must work in read-only mode. Do not modify files.",
    "",
    ...section("Goal", contract.goal),
    ...section("Contract Constraints", contract.constraints.map((item) => `- ${item}`).join("\n")),
    ...section("Acceptance Criteria", contract.acceptanceCriteria.map((item) => `- ${item}`).join("\n")),
    ...section("Repository Snapshot", [
      `- Root path: ${repository.rootPath}`,
      `- Base branch: ${repository.branch}`,
      `- HEAD: ${repository.headSha}`,
      `- Dirty source checkout: ${repository.dirty ? "yes" : "no"}`
    ].join("\n")),
    ...section("Planner Brief", plannerBrief.trim()),
    "## Instructions",
    "- Inspect the repository and identify exactly one scoped improvement.",
    "- The improvement should be realistic for one implementation pass.",
    "- Prefer bugs, reliability, or clear UX improvements over broad rewrites.",
    "- Verification must be concrete: tests, commands, or deterministic checks.",
    "",
    "## Output Format",
    "Return Markdown with exactly these sections:",
    "",
    "# Improvement",
    "# Rationale",
    "# Files To Touch",
    "# Verification",
    "# Risks"
  ].join("\n");
}

export function buildImplementationPrompt({ contract, repository, plan, worktree, providers }) {
  return [
    "# Role",
    "You are the implementation agent for a long-running project-improver harness.",
    "You may edit files and run commands, but only inside the isolated worktree for this run.",
    "",
    ...section("Goal", contract.goal),
    ...section("Worktree", [
      `- Source repository: ${repository.rootPath}`,
      `- Isolated worktree: ${worktree.worktreePath}`,
      `- Branch: ${worktree.branchName}`,
      `- Evaluator: ${providers.evaluator.name}`
    ].join("\n")),
    ...section("Constraints", contract.constraints.map((item) => `- ${item}`).join("\n")),
    ...section("Accepted Plan", plan.trim()),
    "## Instructions",
    "- Implement the scoped improvement from the plan in the isolated worktree only.",
    "- Run the minimum verification needed to prove the change works.",
    "- Do not broaden scope beyond the approved plan.",
    "- If the plan is invalid or blocked, explain the blocker instead of improvising a rewrite.",
    "",
    "## Output Format",
    "Return Markdown with exactly these sections:",
    "",
    "# Summary",
    "# Files Changed",
    "# Verification",
    "# Risks"
  ].join("\n");
}

export function buildEvaluationPrompt({ contract, repository, plan, implementationSummary, worktree }) {
  return [
    "# Role",
    "You are the evaluation agent for a long-running project-improver harness.",
    "You must not modify files. Your job is to judge whether the implementation satisfies the contract and plan.",
    "",
    ...section("Goal", contract.goal),
    ...section("Acceptance Criteria", contract.acceptanceCriteria.map((item) => `- ${item}`).join("\n")),
    ...section("Evaluator Checks", contract.evaluatorChecks.map((item) => `- ${item}`).join("\n")),
    ...section("Repository", [
      `- Source repository: ${repository.rootPath}`,
      `- Isolated worktree: ${worktree.worktreePath}`,
      `- Branch: ${worktree.branchName}`
    ].join("\n")),
    ...section("Plan", plan.trim()),
    ...section("Implementation Summary", implementationSummary.trim()),
    "## Instructions",
    "- Inspect the worktree, diff, and any verification results.",
    "- Mark passed=false if the change is incomplete, unverified, or off-plan.",
    "- Be strict. Do not reward effort. Reward evidence.",
    "",
    "## Output Contract",
    "Return only a JSON object with these fields:",
    "- passed: boolean",
    "- summary: string",
    "- issues: string[]",
    "- evidence: string[]",
    "- suggestedNextStep: string"
  ].join("\n");
}

export function buildHandoffMarkdown({ contract, plan, implementationSummary, evaluation }) {
  return [
    `# Handoff`,
    "",
    `## Goal`,
    contract.goal,
    "",
    `## Final Status`,
    evaluation.passed ? "approved" : "needs_revision",
    "",
    `## Plan`,
    plan.trim(),
    "",
    `## Implementation Summary`,
    implementationSummary.trim(),
    "",
    `## Evaluation`,
    `- Passed: ${evaluation.passed ? "yes" : "no"}`,
    `- Summary: ${evaluation.summary}`,
    ...(evaluation.evidence ?? []).map((item) => `- Evidence: ${item}`),
    ...(evaluation.issues ?? []).map((item) => `- Issue: ${item}`),
    "",
    `## Suggested Next Step`,
    evaluation.suggestedNextStep
  ].join("\n");
}

export const EVALUATION_SCHEMA = {
  type: "object",
  properties: {
    passed: { type: "boolean" },
    summary: { type: "string" },
    issues: {
      type: "array",
      items: { type: "string" }
    },
    evidence: {
      type: "array",
      items: { type: "string" }
    },
    suggestedNextStep: { type: "string" }
  },
  required: ["passed", "summary", "issues", "evidence", "suggestedNextStep"],
  additionalProperties: false
};


import path from "node:path";
import { RunEngine } from "../runner/run-engine.js";
import { createRunId, slugifyGoal } from "../contracts/run-contract.js";
import { WorktreeManager } from "./worktree-manager.js";
import { JournalStore } from "../store/journal-store.js";
import { getLocalCliProvider } from "../providers/local-cli-registry.js";
import { CliProviderRunner } from "../providers/cli-provider-runner.js";
import { buildEvaluationPrompt, buildHandoffMarkdown, buildImplementationPrompt, buildPlanningPrompt, EVALUATION_SCHEMA } from "./stage-prompts.js";
import { execFileAsync } from "../utils/exec.js";

function renderPlannerBrief({ repository, goal, implementationProvider, evaluatorProvider, baseRef }) {
  const lines = [
    `# Planner Brief`,
    "",
    `## Objective`,
    goal,
    "",
    `## Repository Snapshot`,
    `- Repository: ${repository.repositoryName}`,
    `- Root path: ${repository.rootPath}`,
    `- Base ref: ${baseRef}`,
    `- Current branch: ${repository.branch}`,
    `- HEAD: ${repository.headSha}`,
    `- Dirty source checkout: ${repository.dirty ? "yes" : "no"}`,
    `- Origin: ${repository.originUrl ?? "none"}`,
    "",
    `## Providers`,
    `- Implementer: ${implementationProvider.name} (${implementationProvider.command})`,
    `- Evaluator: ${evaluatorProvider.name} (${evaluatorProvider.command})`,
    "",
    `## Constraints For Planning`,
    `- The source checkout is read-only context; all edits must happen in the isolated worktree.`,
    `- The first pass should identify one scoped improvement, not a vague rewrite.`,
    `- The evaluator must be able to prove success with repo artifacts, commands, or tests.`,
    ""
  ];

  if (repository.statusEntries.length > 0) {
    lines.push("## Existing Source Checkout Changes");
    lines.push(...repository.statusEntries.map((entry) => `- ${entry}`));
    lines.push("");
  }

  lines.push("## Required Output");
  lines.push("- A concise plan with exactly one proposed improvement.");
  lines.push("- The commands or tests needed to verify it.");
  lines.push("- Any blocking risks that would invalidate the run.");

  return lines.join("\n");
}

export class ProjectImprover {
  constructor({
    artifactRoot,
    worktreeRoot,
    providerRunner
  } = {}) {
    this.engine = new RunEngine({ artifactRoot });
    this.artifactStore = this.engine.artifactStore;
    this.worktreeManager = new WorktreeManager({ worktreeRoot });
    this.journalStore = new JournalStore(this.artifactStore);
    this.providerRunner = providerRunner ?? new CliProviderRunner(this.artifactStore);
  }

  async initializeRun({
    repositoryPath,
    goal,
    implementationProvider = "codex",
    evaluatorProvider = "claude",
    baseRef,
    acceptanceCriteria,
    deliverables,
    evaluatorChecks
  }, now = new Date()) {
    const repository = await this.worktreeManager.inspectRepository(repositoryPath);
    const implementation = getLocalCliProvider(implementationProvider);
    const evaluator = getLocalCliProvider(evaluatorProvider);
    const effectiveGoal = goal?.trim() || `Ship one scoped improvement in ${repository.repositoryName}`;
    const runId = createRunId(`${repository.repositoryName}-${effectiveGoal}`, now);
    const branchName = `codex/improve-${slugifyGoal(repository.repositoryName)}-${runId.slice(0, 14)}`;
    const effectiveBaseRef = baseRef ?? (repository.branch === "HEAD" ? repository.headSha : repository.branch);
    const worktreePath = this.worktreeManager.getRunWorktreePath(runId);

    const { contract, runDir } = await this.engine.bootstrapRun({
      runId,
      goal: effectiveGoal,
      workspacePath: repository.rootPath,
      worktreePath,
      branchName,
      constraints: [
        "Do not modify the source checkout.",
        "Work only inside the isolated git worktree created for this run.",
        "Do not use destructive git commands against the source repository.",
        "All success claims must be backed by tests, commands, or evaluator evidence."
      ],
      acceptanceCriteria: acceptanceCriteria ?? [
        "A dedicated worktree exists for the run and uses its own branch.",
        "Repository state is persisted in run artifacts before implementation starts.",
        "The run identifies one explicit improvement and a verifiable way to check it."
      ],
      deliverables: deliverables ?? [
        "An isolated improvement branch",
        "A repository snapshot artifact",
        "A planner brief for the implementation agent",
        "A durable journal of run events"
      ],
      evaluatorChecks: evaluatorChecks ?? [
        "The worktree path is different from the source checkout path.",
        "The run artifacts include repository.json, providers.json, worktree.json, and journal.jsonl.",
        "The contract names both implementation and evaluator providers."
      ]
    }, now);

    const worktree = await this.worktreeManager.createWorktree({
      repositoryPath: repository.rootPath,
      runId,
      branchName,
      baseRef: effectiveBaseRef
    });

    await Promise.all([
      this.artifactStore.writeJson(runId, "repository.json", {
        ...repository,
        baseRef: effectiveBaseRef
      }),
      this.artifactStore.writeJson(runId, "providers.json", {
        implementation,
        evaluator
      }),
      this.artifactStore.writeJson(runId, "worktree.json", worktree),
      this.artifactStore.writeText(
        runId,
        "planner-brief.md",
        renderPlannerBrief({
          repository,
          goal: effectiveGoal,
          implementationProvider: implementation,
          evaluatorProvider: evaluator,
          baseRef: effectiveBaseRef
        })
      ),
      this.journalStore.append(runId, {
        type: "run.initialized",
        detail: `Initialized improve-project run for ${repository.repositoryName}`,
        repositoryPath: repository.rootPath,
        worktreePath
      }, now),
      this.journalStore.append(runId, {
        type: "worktree.created",
        detail: `Created isolated worktree on branch ${branchName}`,
        branchName,
        baseRef: effectiveBaseRef,
        worktreePath
      }, now)
    ]);

    return {
      contract,
      runDir,
      repository,
      worktree,
      providers: {
        implementation,
        evaluator
      }
    };
  }

  async loadRunContext(runId) {
    const [contract, state, repository, providers, worktree] = await Promise.all([
      this.artifactStore.readJson(runId, "contract.json"),
      this.artifactStore.readJson(runId, "state.json"),
      this.artifactStore.readJson(runId, "repository.json"),
      this.artifactStore.readJson(runId, "providers.json"),
      this.artifactStore.readJson(runId, "worktree.json")
    ]);

    return {
      contract,
      state,
      repository,
      providers,
      worktree
    };
  }

  async updateState(runId, mutate, now = new Date()) {
    const state = await this.artifactStore.readJson(runId, "state.json");
    const nextState = {
      ...state,
      ...mutate(state),
      updatedAt: now.toISOString()
    };
    await this.artifactStore.updateState(runId, nextState);
    return nextState;
  }

  nextAttempt(state, stage) {
    const attempts = state.attempts ?? {
      planning: 0,
      implementation: 0,
      evaluation: 0,
      handoff: 0
    };
    return (attempts[stage] ?? 0) + 1;
  }

  async snapshotWorktree(runId, attempt, worktreePath) {
    const [status, diffStat, diffPatch] = await Promise.all([
      execFileAsync("git", ["-C", worktreePath, "status", "--short"]),
      execFileAsync("git", ["-C", worktreePath, "diff", "--stat"]),
      execFileAsync("git", ["-C", worktreePath, "diff", "--binary"])
    ]);

    await Promise.all([
      this.artifactStore.writeText(runId, `implementation-attempt-${attempt}.status.txt`, status.stdout),
      this.artifactStore.writeText(runId, `implementation-attempt-${attempt}.diffstat.txt`, diffStat.stdout),
      this.artifactStore.writeText(runId, `implementation-attempt-${attempt}.diff.patch`, diffPatch.stdout)
    ]);
  }

  async executeStage(runId, stage, now = new Date()) {
    const context = await this.loadRunContext(runId);
    const attempt = this.nextAttempt(context.state, stage);

    await this.updateState(runId, (state) => ({
      attempts: {
        ...(state.attempts ?? {}),
        [stage]: attempt
      },
      lastStageResult: {
        stage,
        status: "running",
        attempt
      }
    }), now);

    await this.journalStore.append(runId, {
      type: "stage.started",
      stage,
      attempt,
      detail: `Starting ${stage} with provider ${this.providerNameForStage(context, stage)}`
    }, now);

    const execution = await this.runProviderStage(runId, stage, attempt, context);

    await this.updateState(runId, () => ({
      lastStageResult: {
        stage,
        status: "completed",
        attempt,
        provider: execution.provider,
        responsePath: execution.responsePath
      }
    }), new Date(execution.finishedAt));

    await this.journalStore.append(runId, {
      type: "stage.completed",
      stage,
      attempt,
      provider: execution.provider,
      responsePath: execution.responsePath,
      durationMs: execution.durationMs,
      detail: `${stage} completed`
    }, new Date(execution.finishedAt));

    return execution;
  }

  providerNameForStage(context, stage) {
    if (stage === "evaluation") return context.providers.evaluator.name;
    return context.providers.implementation.name;
  }

  async runProviderStage(runId, stage, attempt, context) {
    if (stage === "planning") {
      const plannerBrief = await this.artifactStore.readText(runId, "planner-brief.md");
      const prompt = buildPlanningPrompt({
        contract: context.contract,
        repository: context.repository,
        plannerBrief
      });

      const execution = await this.providerRunner.run({
        runId,
        stage,
        attempt,
        provider: context.providers.implementation.name,
        cwd: context.repository.rootPath,
        prompt
      });

      await this.engine.recordPlan(runId, String(execution.output));
      return execution;
    }

    if (stage === "implementation") {
      const plan = await this.artifactStore.readText(runId, "plan.md");
      const prompt = buildImplementationPrompt({
        contract: context.contract,
        repository: context.repository,
        plan,
        worktree: context.worktree,
        providers: context.providers
      });

      const execution = await this.providerRunner.run({
        runId,
        stage,
        attempt,
        provider: context.providers.implementation.name,
        cwd: context.worktree.worktreePath,
        prompt
      });

      await this.snapshotWorktree(runId, attempt, context.worktree.worktreePath);
      await this.engine.recordImplementation(runId, String(execution.output));
      return execution;
    }

    if (stage === "evaluation") {
      const [plan, implementationSummary] = await Promise.all([
        this.artifactStore.readText(runId, "plan.md"),
        this.artifactStore.readText(runId, "implementation.md")
      ]);
      const prompt = buildEvaluationPrompt({
        contract: context.contract,
        repository: context.repository,
        plan,
        implementationSummary,
        worktree: context.worktree
      });

      const execution = await this.providerRunner.run({
        runId,
        stage,
        attempt,
        provider: context.providers.evaluator.name,
        cwd: context.worktree.worktreePath,
        prompt,
        schema: EVALUATION_SCHEMA
      });

      await this.engine.recordEvaluation(runId, execution.output);
      if (execution.output.passed) {
        const handoff = buildHandoffMarkdown({
          contract: context.contract,
          plan,
          implementationSummary,
          evaluation: execution.output
        });
        await this.engine.recordHandoff(runId, handoff);
      }
      return execution;
    }

    throw new Error(`Unsupported stage: ${stage}`);
  }

  async executeRun(runId, { maxRevisions = 2 } = {}) {
    while (true) {
      const context = await this.loadRunContext(runId);

      if (context.state.currentStage === "completed") {
        return {
          status: "completed",
          runId
        };
      }

      if (
        context.state.currentStage === "implementation" &&
        context.state.status === "needs_revision" &&
        context.state.revisionCount > maxRevisions
      ) {
        await this.journalStore.append(runId, {
          type: "run.stopped",
          detail: `Stopped after exceeding max revisions (${maxRevisions})`
        });
        return {
          status: "needs_revision",
          runId,
          revisionCount: context.state.revisionCount
        };
      }

      await this.executeStage(runId, context.state.currentStage);
    }
  }
}

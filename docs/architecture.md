# Architecture

## Why this exists

Crius is being rebuilt around harness design instead of scheduled prompt injection.

The old failure mode was straightforward:

- autonomous work shared the operator's live model session
- safety rules lived mostly in prompts
- long-running work had weak handoff artifacts
- evaluation and implementation were collapsed into one actor

This version starts with the minimum structure needed to avoid that class of failure.

## Core model

Each run has four durable concepts:

1. **Contract**
   Defines the goal, constraints, acceptance criteria, evaluator checks, workspace path, and isolated worktree path.
2. **State**
   Tracks current stage, status, revision count, and timestamps.
3. **Artifacts**
   Markdown and JSON outputs for each stage.
4. **Lifecycle**
   Planner -> implementation -> evaluator -> handoff.

## Stage model

### Planning

The planner expands a goal into a concrete execution plan with explicit acceptance criteria and deliverables.

Artifacts:

- `spec.md`
- `plan.md`

### Implementation

The implementation agent works only against the contract and plan.

Artifacts:

- `implementation.md`

### Evaluation

The evaluator checks the result against external criteria.

Artifacts:

- `evaluation.json`

If evaluation fails, the run returns to implementation with `status=needs_revision`.

### Handoff

The final stage records summary, risks, and next steps.

Artifacts:

- `handoff.md`

## Storage

Runs are stored under:

```text
.crius/runs/<run-id>/
```

This keeps the harness reset-safe. If the model session dies, the next session can recover from durable run state instead of re-inferencing everything from old chat context.

## Next implementation steps

- add a real isolated worktree manager
- add command execution journals
- add planner/evaluator adapters for concrete models
- add guardrail enforcement before filesystem mutations
- add retry policies and leases for daemonized execution


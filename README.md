# Crius

Crius is a long-running agent harness for software work.

This repo starts from a different assumption than the old project: autonomous work should not be injected into a live user session. Every run gets an explicit contract, isolated workspace metadata, persisted artifacts, and a planner -> implementation -> evaluator -> handoff lifecycle.

## Principles

- Isolated runs, not shared chat state
- Explicit contracts before implementation
- External evaluation criteria, not self-grading only
- Durable artifacts for reset-safe continuation
- Runtime guardrails over prompt-only rules

## Current Scope

The current codebase is the v2 foundation:

- `src/contracts/` defines run contracts and validation
- `src/store/` persists contracts, state, and artifacts under `.crius/runs/`
- `src/runner/` drives the staged lifecycle
- `src/improver/` initializes isolated improve-project runs with git worktrees
- `src/providers/` executes planning, implementation, and evaluation through local CLI agents
- `src/cli.js` bootstraps generic runs and improve-project runs

The previous scheduler-first codebase has been split out into the separate repository `agent-scheduler`.

## Quick Start

```bash
node src/cli.js fixtures/demo-run.json
```

This creates a new run under `.crius/runs/<run-id>/` with:

- `contract.json`
- `state.json`
- `spec.md`

To initialize a real `project-improver` run against a git repository:

```bash
node src/cli.js improve-project /path/to/repo --goal "Ship one scoped improvement" --provider codex --evaluator claude
```

This also creates:

- `repository.json`
- `providers.json`
- `worktree.json`
- `planner-brief.md`
- `journal.jsonl`

To execute the run through planning, implementation, and evaluation:

```bash
node src/cli.js execute-run <run-id> --max-revisions 2
```

Default execution model:

- planning: implementation provider in read-only mode
- implementation: implementation provider in isolated worktree
- evaluation: evaluator provider with structured JSON output

This is now a real staged runtime. It is still not the full finished system because lease recovery, parallel branches, and deep guardrails are still missing.

## Test

```bash
npm test
```

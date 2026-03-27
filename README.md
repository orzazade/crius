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
- `src/cli.js` bootstraps a run from JSON

The previous scheduler-first codebase has been split out into the separate repository `agent-scheduler`.

## Quick Start

```bash
node src/cli.js fixtures/demo-run.json
```

This creates a new run under `.crius/runs/<run-id>/` with:

- `contract.json`
- `state.json`
- `spec.md`

## Test

```bash
npm test
```


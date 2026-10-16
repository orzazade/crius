# Claude Code telemetry, hooks, statusline: sourced facts

Checked 2026-10-16 against the `.md` versions of the official pages:
- MON = https://code.claude.com/docs/en/monitoring-usage
- HOOKS = https://code.claude.com/docs/en/hooks
- SL = https://code.claude.com/docs/en/statusline
- COSTS = https://code.claude.com/docs/en/costs
- MEM = https://code.claude.com/docs/en/memory
- SET = https://code.claude.com/docs/en/settings
- CLI = https://code.claude.com/docs/en/cli-reference

## 1. OpenTelemetry (MON)

Turn it on with `CLAUDE_CODE_ENABLE_TELEMETRY=1`. CONFIRMED (MON).

### Metrics (all CONFIRMED, MON)

| Metric | Unit | Key attributes |
|---|---|---|
| `claude_code.session.count` | - | standard; how it started (`resume`, `continue`, `agents_view`, ...) |
| `claude_code.cost.usage` | USD | `model`, `query_source` (main/subagent/auxiliary), `effort`, `speed`, `agent.name`, `skill.name`, `plugin.name`, `mcp_server.name`. Docs call it an approximation |
| `claude_code.token.usage` | tokens | as cost + `type` = `input` / `output` / `cacheRead` / `cacheCreation` (`input` excludes cache) |
| `claude_code.lines_of_code.count` | - | added/removed, by model |
| `claude_code.commit.count` | - | standard |
| `claude_code.pull_request.count` | - | standard |
| `claude_code.code_edit_tool.decision` | - | `tool_name` (Edit/Write/NotebookEdit), `decision` accept/reject, `source` (config, hook, user_permanent, user_temporary, user_abort, user_reject), `language` |
| `claude_code.active_time.total` | s | `type` = `user` (keyboard) or `cli` (tools + model). Excludes idle time |

Standard attributes on every metric and event: `session.id` (default on), `organization.id`, `user.account_uuid`, `user.account_id`, `user.id`, `user.email`, `terminal.type`, optional `app.version`, `app.entrypoint`. CONFIRMED (MON).

### Events (logs; need `OTEL_LOGS_EXPORTER`). CONFIRMED (MON)

| Event | Useful fields |
|---|---|
| `user_prompt` | `prompt_length`, `prompt`/`prompt_text` (redacted unless `OTEL_LOG_USER_PROMPTS=1`), `command_name`, `message.uuid` |
| `assistant_response` | `response_length`, text only |
| `api_request` | `model`, `cost_usd`, `cost_usd_micros`, `duration_ms`, `input_tokens`, `output_tokens`, `cache_read_tokens`, `cache_creation_tokens`, `request_id`, `query_source`, `effort`, `speed` |
| `tool_decision` | `tool_name`, `tool_use_id` (same id hooks get), `decision`, `source`, `tool_source` |
| `tool_result` | tool call result; details gated by `OTEL_LOG_TOOL_DETAILS=1` |
| others | `api_error`, `api_refusal`, `api_retries_exhausted`, `api_request_body`/`api_response_body` (`OTEL_LOG_RAW_API_BODIES`), `permission_mode_changed`, `auth` (`auth_method` e.g. `oauth`), `mcp_server_connection`, `internal_error`, `plugin_installed`, `plugin_loaded`, `skill_activated`, `at_mention`, `hook_registered`, `hook_execution_start`, `hook_execution_complete`, `hook_plugin_metrics`, `compaction`, `subagent_completed`, `feedback_survey`, `retention_sweep`, `managed_settings_resolved` |

Every event in one turn shares `prompt.id`; `event.sequence` orders them. CONFIRMED (MON).

### Traces (beta). CONFIRMED (MON)

| Item | Fact |
|---|---|
| Enable | `CLAUDE_CODE_ENABLE_TELEMETRY=1` + `CLAUDE_CODE_ENHANCED_TELEMETRY_BETA=1` + `OTEL_TRACES_EXPORTER` (`console`, `otlp`, `none`) |
| `claude_code.interaction` | root span per prompt; `interaction.duration_ms`, `user_prompt_length`, `user_prompt` (redacted unless gated) |
| `claude_code.tool.blocked_on_user` | child of each tool span: `duration_ms` = **time waiting for the permission decision**, `decision`, `source` |

### Exporters and privacy

| Question | Answer | Status |
|---|---|---|
| Metrics exporters | `otlp`, `prometheus`, `console`, `none` | CONFIRMED (MON) |
| Logs exporters | `otlp`, `console`, `none` | CONFIRMED (MON) |
| Local collector | Yes: example `OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4317` (grpc); Prometheus scrape at `http://localhost:9464/metrics` | CONFIRMED (MON) |
| File exporter | No file exporter listed. Use `console`, or a local OTel Collector with a file exporter | NOT FOUND (searched MON) |
| Intervals | metrics default 60000 ms, logs 5000 ms | CONFIRMED (MON) |
| Prompt text | Opt-in: `OTEL_LOG_USER_PROMPTS=1`. Tool args: `OTEL_LOG_TOOL_DETAILS=1`. Tool output: `OTEL_LOG_TOOL_CONTENT=1` | CONFIRMED (MON) |
| Works with subscription (OAuth) login | No explicit sentence. Indirect: docs say "When authenticated via OAuth, `user.email` is included in telemetry attributes", and the `auth` event has `auth_method` `oauth` | NOT FOUND explicitly (MON, COSTS) |
| Where env may be set | Repo `.claude/settings.json` / `.local.json` can't set exporter vars; use `~/.claude/settings.json`, shell env, managed settings, or `--settings` | CONFIRMED (MON) |
| Subprocesses | Claude Code strips `OTEL_*` exporter vars from hooks and other subprocesses | CONFIRMED (HOOKS) |

## 2. Hooks (HOOKS)

All event names (CONFIRMED): SessionStart, Setup, InstructionsLoaded, UserPromptSubmit, UserPromptExpansion, MessageDisplay, PreToolUse, PermissionRequest, PostToolUse, PostToolUseFailure, PostToolBatch, PermissionDenied, Notification, SubagentStart, SubagentStop, TaskCreated, TaskCompleted, Stop, StopFailure, TeammateIdle, ConfigChange, CwdChanged, DirectoryAdded, FileChanged, WorktreeCreate, WorktreeRemove, PreCompact, PostCompact, PreModelSwitch, PostModelSwitch, SessionEnd, Elicitation, ElicitationResult.

Common input on every event: `session_id`, `prompt_id`, `transcript_path`, `cwd`, `scratchpad_dir`, `permission_mode`, `effort.level`, `hook_event_name`, plus `agent_id`/`agent_type` inside subagents. CONFIRMED.

| Event | Extra input fields (CONFIRMED) |
|---|---|
| Notification | `message`, `title` (optional), `notification_type` |
| Stop | `stop_hook_active`, `last_assistant_message`, `background_tasks`, `session_crons` |
| SubagentStop | same as Stop + `agent_id`, `agent_type`, `agent_transcript_path` |
| StopFailure | `error`, `error_details`, `last_assistant_message` (the API error text, e.g. "API Error: Rate limit reached") |
| SessionStart | `source` (startup/resume/clear/compact/fork), `model`, `agent_type`, `session_title`; on resume/fork also `seconds_since_last_response`, `context_tokens`, `prompt_cache_likely_expired`, `estimated_cache_write_usd` |
| SessionEnd | `reason`: clear, resume, logout, prompt_input_exit, other |
| PreCompact | `trigger` (manual/auto), `custom_instructions` |
| PostCompact | `trigger`, `compact_summary` |
| UserPromptSubmit | `prompt`, `session_title` (when set) |

Notification types that mean "the session waits for the human" (CONFIRMED):

| `notification_type` | Fires when |
|---|---|
| `permission_prompt` | a permission prompt has waited about 6 s |
| `idle_prompt` | Claude finished about 60 s ago and you have not typed |
| `elicitation_dialog`, `elicitation_url_dialog` | MCP form/URL waiting about 6 s |
| `agent_needs_input` | background session waits on input (only while agent view is open) |
| `quota_auto_resume_fired` / `_stale` / `_disabled` | session paused by a claude.ai usage limit, then resumed / waiting for Enter / gave up |
| also | `auth_success`, `elicitation_complete`, `elicitation_response`, `agent_completed` |

`PermissionRequest` fires at the moment a permission dialog would show (matches on tool name). Same handler in several settings files runs once. CONFIRMED.

## 3. Statusline JSON (SL). All CONFIRMED

| Group | Fields |
|---|---|
| Identity | `session_id`, `session_name`, `prompt_id`, `transcript_path`, `version`, `agent.name` |
| Model | `model.id`, `model.display_name`, `effort.level`, `thinking.enabled`, `fast_mode` |
| Cost/time | `cost.total_cost_usd` (client-side list-price estimate), `cost.total_duration_ms`, `cost.total_api_duration_ms`, `cost.total_lines_added`, `cost.total_lines_removed` |
| Context | `context_window.total_input_tokens`, `total_output_tokens`, `context_window_size`, `used_percentage`, `remaining_percentage`, `current_usage`; `prompt_cache` stats |
| Rate limits | `rate_limits.five_hour.used_percentage` / `.resets_at`, `rate_limits.seven_day.used_percentage` / `.resets_at` (epoch s); `rate_limits.spend_limit.*` behind a gateway |
| Place | `cwd`, `workspace.*` (project_dir, git_worktree, repo host/owner/name), `worktree.*`, `pr.number`, `pr.url`, `pr.review_state` |

`rate_limits` appears only for claude.ai Pro and Max subscribers, and only after the first API response in the session. CONFIRMED (SL).

## 4. Subscription usage limit, programmatic

| Source | Has 5-hour / weekly limit? | Status |
|---|---|---|
| Statusline JSON | Yes: `rate_limits.five_hour` and `rate_limits.seven_day` (`used_percentage`, `resets_at`) | CONFIRMED (SL) |
| `/usage` | Yes, as bars (interactive screen); plus attribution by skill/subagent/plugin/MCP, 24 h / 7 d, from local history only | CONFIRMED (COSTS) |
| OTel | No rate-limit metric or event | NOT FOUND (searched MON for five_hour, seven_day, rate limit, usage limit: 0 hits) |
| A file on disk | None documented. `/insights` writes `~/.claude/usage-data/report.html` (work patterns, not limits) | NOT FOUND (COSTS, SL) |
| Hooks | Only indirect: `quota_auto_resume_*` notification types, StopFailure rate-limit error | CONFIRMED (HOOKS) |

## 5. Memory (MEM). CONFIRMED

| Item | Limit |
|---|---|
| `MEMORY.md` (auto memory) at session start | first 200 lines or first 25 KB, whichever comes first; rest not loaded |
| Topic files | not loaded at start; read on demand |
| CLAUDE.md | loaded in full; target under 200 lines; files over 4 MiB skipped |

## 6. `--settings` per launch (CLI, SET)

| Question | Answer | Status |
|---|---|---|
| Accepts | path to a JSON file or an inline JSON string, max 2 MiB | CONFIRMED (CLI) |
| Precedence | above user/project/local, below managed; can set any key user settings can | CONFIRMED (SET) |
| `hooks` via `--settings` | `hooks` is a user-settings key, so yes by the rule above; no worked example found | CONFIRMED by rule, not by example (SET, HOOKS) |
| Telemetry `env` via `--settings` | Yes: MON names "a `--settings` file" as a place that sets OTel vars a repo can't override | CONFIRMED (MON) |
| Hooks and trust | interactive sessions hold back hooks until the folder's trust dialog is accepted | CONFIRMED (HOOKS) |

## What this means for measuring output/cost/waits without our own parser

1. Output and cost: OTel metrics give per-`session.id` cost, tokens (with cache read/write split), lines, commits, PRs and active time; send to a local OTel Collector (OTLP :4317) or scrape Prometheus :9464, no transcript parsing.
2. Cost on Max is a list-price estimate (same in statusline and OTel); for plan pressure use the statusline `rate_limits.five_hour/seven_day` percentages, the only programmatic source of the limit.
3. Waits: `claude_code.tool.blocked_on_user.duration_ms` (beta traces) gives exact permission-wait time; Notification hooks (`permission_prompt`, `idle_prompt`) plus the next `UserPromptSubmit` timestamp give time waiting for input.
4. Quality signals without a parser: `tool_decision` rejects (`user_reject`, `user_abort`), `api_error`/`StopFailure`, `compaction` events, and Stop's `last_assistant_message` for a judge.
5. One gap: OAuth support for OTel is implied, not stated; run one session with `OTEL_METRICS_EXPORTER=console` via `--settings` to confirm before building on it.

## Point 4 settled (2026-10-16, Crius)
CONFIRMED by test: OTel works on the Max subscription (OAuth) login. `claude -p --settings '{"env":{"CLAUDE_CODE_ENABLE_TELEMETRY":"1","OTEL_METRICS_EXPORTER":"console","OTEL_LOGS_EXPORTER":"console",...}}'`
emitted claude_code.cost.usage, token.usage, active_time.total, session.count, and events api_request,
user_prompt, assistant_response, hook_execution_*, plugin_loaded, mcp_server_connection. So `--settings` env works
per launch too.

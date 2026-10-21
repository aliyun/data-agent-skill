# Regression Matrix

Pre-release (`pre`) end-to-end regression for the CLI. Run before every version bump that touches session creation, data sources, or the send/attach path.

## Principles

- **Credential axis is not a full product.** AK/SK is the trunk and covers every path. API_KEY is a partial branch: the backend supports it for file analysis and (via pre-creation) custom agents; every other cell is either out of scope by design or an unknown that needs a baseline first.
- **Two depth tiers.** *Smoke* cells only prove a message can be sent and a response received. *E2E* cells must drive a pro-mode session through to completion and produce artifacts.
- **`status` is an embedded assertion, not a row.** Its risk surface (wire enum normalization: `init`, `RECOVERABLE`, `INITIALIZING`...) is state-dependent, so it is asserted inside other cells at different session phases.
- **Known issues are expected behavior, not failures.** See [Known issues](#known-issues).

## Fixtures

| Fixture | Value |
|---------|-------|
| Environment | `pre` via `DATA_AGENT_CREDENTIALS_ENV=pre` — profiles `dmstest` (recommended, known-good) and `idbcloud` (has custom agents; db permission failures observed) |
| Database | `internal_data_employees` [mysql] dbId=`6fyoqbd8tz4kpu21vtg5f5oj6` (DMS instance 1987016, DMS db 118916382) — **table permission failures under pre**; prefer `示例_员工数据及部门数据.duckdb` dbId=`f-ckads6dfq5fo6reoml84qxxs0` via `send --data-source` |
| File | `assets/example_game_data.csv` |
| Custom agent | `测试` ca-efawjvflo94bofxobojhymcpo, workspace `bwhrx2e80zbzcjdnznj2azces` (pre/idbcloud only) |
| Query (smoke) | `"ping"` / `"How many rows are there?"` |
| Query (E2E) | `"Analyze the game data and generate a summary report."` |

> `dms` subcommands (list-instances / search-database / list-tables) use the SDK default credential chain only and do not read the credentials file — on machines without `~/.alibabacloud/credentials.ini` they fail with an ECS metadata lookup. Discover fixtures via `ls` instead.

## Matrix

| ID | Cred | Path | Command | Depth | Assertions | State |
|----|------|------|---------|-------|------------|-------|
| A1 | AK | send, no data source | `send -m "ping"` | Smoke | exit 0; `Session ID` printed; non-empty response; `status` → RUNNING / IDLE | ✅ PASS 2026-10-21 (pre/idbcloud) — `IDLE (server: init)` proves alias normalization on the wire |
| A2 | AK | send, db | `send --dms-instance-id <I> --dms-db-id <D> --db-name <DB> --tables <T> -m "..."` | Smoke | A1 + DescribeDataAgentSession shows db data source bound | ✅ PASS 2026-10-21 — detail shows `data_source_type: database` with tables + dms ids (binding OK; note the session itself fails data-prep on permission, see A5) |
| A3 | AK | send, file | `upload <file>` → `send --data-source <FILE_ID> -m "..."` | Smoke | A1 + file data source bound | ✅ PASS 2026-10-21 — detail shows `data_source_type: remote_data_center`, file_id matches, session RUNNING |
| A4 | AK | send, custom agent | `send --workspace-id <WS> --custom-agent-id <CA> -m "..."` | Smoke | A1 + no CreateDataAgentSession on the wire (`DATA_AGENT_DEBUG_API=1`) | ✅ PASS 2026-10-21 — exactly 1 SendChatMessage, 0 CreateDataAgentSession; `SessionConfig` carries `CustomAgentId` + `CustomAgentStage: prod` |
| A5 | AK | db pro → attach confirm → artifacts | `db --session-mode pro ... -q "..."` → `attach -s <ID>` → `attach -s <ID> -q "confirm"` → `reports -s <ID>` | E2E | plan emitted → `status` = WAIT_INPUT → confirm accepted → steps run → `result.json={"status":"completed"}` → `reports` lists ≥1 artifact → `status` = IDLE (server: RECOVERABLE) or STOPPED | ✅ PASS 2026-10-21 (file path, pre/dmstest) — full chain: plan (5 steps) → confirm → steps 1–5 → Report Render second confirm → confirm → 25 artifacts via `reports`. db path blocked by fixture permissions, not CLI: `internal_data_employees` mysql returns `DMS.DataAgent.CheckDatabasePermissionFailed` in data-prep (SSE `task_finish` payload); duckdb file-type database via `send --data-source` does produce a plan |
| A6 | AK | db pro, unattended via SessionConfig Skip* | `send --session-mode pro --dms-* ... -m "..." --poll` with `SkipPlan` / `SkipSqlConfirm` / `SkipWebReportConfirm` / `SkipAskHuman` set | E2E | **no** WAIT_INPUT at any point; completes; `reports` lists ≥1 artifact | BLOCKED — `send` does not expose Skip*, see [Capability gap](#send-vs-sendchatmessage-capability-gap) |
| B1 | API_KEY | file E2E (implicit session) | `file <path> -q "..."` → `status` → `attach` → `reports` | E2E | implicit creation works under API_KEY (no CreateDataAgentSession) → completes → artifacts | SKIPPED — no API_KEY credential on this machine (console-issued); matrix needs one to run |
| B2 | API_KEY | send, custom agent (pre-create path) | `send --workspace-id <WS> --custom-agent-id <CA> -m "..."` | Smoke | CLI prints `API_KEY + custom agent pre-create`; CreateDataAgentSession then SendChatMessage on the wire; response non-empty | SKIPPED — no API_KEY credential; pre-create branch confirmed by code-shape probe with fake key (prints pre-create banner, calls CreateDataAgentSession first) |
| B3 | API_KEY | send db / send no data source | `send -m "ping"`; `send --dms-* -m "..."` | N/A-by-design | out of API_KEY support (file-only); record the failure shape and assert it is stable across versions, do not treat as regression | ✅ SHAPE RECORDED 2026-10-21 (fake key) — gateway: HTTP 403 `{'code': 'Forbidden', 'message': 'Invalid ApiKey.'}`; CLI surface prints `Business failure: None None` with **inconsistent exit codes (0 and 1 observed across runs)** — error-parsing gap, see [Findings](#findings-2026-10-21) |

Optional smoke (contract drift probe): `send -s <ID> --message-type diag -m "..."` — `diag` is not in the SendChatMessage `MessageType` enum (`primary` / `additional` / `cancel` / `operate` / `report`); record whether the gateway accepts it.

## E2E checkpoints

Markers in `sessions/<SESSION_ID>/progress.log`, in order:

1. `> User Query: ...`
2. `### Execution Plan (ID: ...)` — A5 only; A6 must **not** stop here
3. `> ⚠️  Plan confirmed, continuing analysis...` — A5 after `attach -q "confirm"`
4. `## Step N/M: ...` — repeated
5. `### Report Render` + `⚠️  Please review the report rendering request.` — A5 second WAIT_INPUT (confirm again or stop); A6 must skip via `SkipWebReportConfirm`
6. `result.json` → `{"status":"completed"}`

## Known issues

| Symptom | Expected handling |
|---------|-------------------|
| SSE stream dies with ECS metadata (`100.100.100.200`) credential refresh timeout under AK | Not a regression. Re-run `attach -s <ID>`; the server keeps running. Record occurrence count per run. |
| `attach` / GetChatContent on custom agent + workspace session returns `SessionUnavailable` | Pre-existing. Custom agent stays in the smoke tier (A4, B2); E2E tiers use db / file. Observed 2026-10-21 also on plain db sessions under pre/idbcloud (describe OK, content stream 404) — account-scoped, dmstest unaffected. |
| API_KEY + custom agent goes through CreateDataAgentSession | By design (backend limitation). B2 asserts the pre-create path, not its absence. |
| `internal_data_employees` mysql tables fail `CheckDatabasePermissionFailed` in pre data-prep | Fixture-level, not CLI. Use duckdb file-type databases or uploaded files for E2E. Confirmed 2026-10-21. |

## Findings 2026-10-21 (first run)

Real bugs / gaps surfaced by the first matrix run, in priority order:

1. **`task_finish` failure payloads are swallowed** — data-prep failures (e.g. `CheckDatabasePermissionFailed`) arrive in the SSE `task_finish` event but the worker prints `✅ 已完成分析，正在获取生成的报告和中间文件...` and `result.json` says `completed`. The session "succeeds" locally while the server never ran the analysis. Repro: `db --dms-instance-id 1987016 --dms-db-id 118916382 ... --session-mode pro` under pre; payload visible only via `attach --from-start --output raw`.
2. **API_KEY business-failure rendering** — gateway 403 JSON (`Invalid ApiKey.`) prints as `Business failure: None None`, and the exit code is inconsistent across runs (0 and 1 observed). Error parsing should surface code/message and exit non-zero deterministically.
3. **Post-confirm plan regeneration breaks the worker loop** — after `attach -q confirm`, the backend can emit a *new* plan (history recap + report generation) instead of continuing the old one; the worker prints `Confirmation processed. Subsequent steps will continue in the background.` and then goes silent — follow-up steps are only observable by re-attaching. The final confirm on the second plan did complete the chain and artifacts were retrievable via `reports`.
4. **`dms` subcommands ignore the credentials file** — `list-instances` / `search-database` / `list-tables` build a `CredentialClient()` from the SDK default chain only, so credentials-file profiles (pre/dmstest etc.) cannot use them; on this machine they fall through to the ECS metadata endpoint and time out. Fixture discovery must use `ls`.
5. **`--dms-db-id` is int-typed** — file-type Data Center databases have `f-` string IDs and cannot be passed as db data sources; only `send --data-source` works for them. Fine today, worth remembering when fixtures are chosen.

## `send` vs SendChatMessage capability gap

Principle: `send` should expose the full SendChatMessage request surface. Contract source: `dms-jarvis/pop/SendChatMessage.yaml` (matches the public API doc).

| Contract field | `send` today | Note |
|----------------|--------------|------|
| `SessionConfig.SkipPlan` / `SkipSqlConfirm` / `SkipWebReportConfirm` / `SkipAskHuman` | missing | blocks A6; CLI only sets them as `agent create/update` ExecutionConfig defaults |
| `SessionConfig.PlanMode` | missing on `send` (present on `db` / `file`) | |
| `SessionConfig.EnableSearch` | missing on `send` (present on `db` / `file`) | |
| `SessionConfig.Language` | hard-coded `CHINESE` | |
| `SessionConfig.ReportWaterMark` | missing | |
| `SessionConfig.KbUuidList` / `McpServerIds` / `UserSpecifiedSkillList` | missing | |
| `SessionConfig.Mode` | `--session-mode auto/lite/pro/ultra` | public doc lists `ASK_DATA` / `ANALYSIS` / `INSIGHT`; backend accepts both — confirm during A5 |
| `SessionConfig.CustomAgentId` / `CustomAgentStage` / `PermissionConfig` | exposed | |
| `MessageType=additional` / `cancel` | missing (`cancel` has its own subcommand) | `additional` is the Human-in-Loop reply type |
| `MessageType=diag` | exposed | not in contract enum — see drift probe above |
| `ReplyTo` / `QuotedMessage` | missing | |
| `ParentSessionId` | missing | |
| `DataSources` / `TaskConfig.ReportConfig` / `WorkspaceId` | exposed | |

## Run log

| Date | CLI version / commit | Cells run | Result | Notes |
|------|----------------------|-----------|--------|-------|
| 2026-10-21 | v2.7.0 (main f052231) | A1–A5, B3 | A1–A5 PASS, B3 shape recorded; A6 BLOCKED (capability gap); B1/B2 SKIPPED (no API_KEY credential) | First run surfaced 5 findings — most severe: `task_finish` failure payloads swallowed (fake success). E2E completed via file path; db path blocked by fixture permissions (`CheckDatabasePermissionFailed`), duckdb plan confirmed as control. API_KEY cells need a console-issued key to run. |

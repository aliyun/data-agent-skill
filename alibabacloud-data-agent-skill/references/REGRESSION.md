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
| API_KEY (real) | console-issued, pre Hangzhou. The key identity sees custom agents in workspace `71hx6c1krdky4xhqsamc2htr2` (e.g. `jufeng-test` ca-cb993u5sprmjni56prlri9j72); discover via `agent` |
| API_KEY gateways (pre) | control `pre-dataagent.aliyuncs.com/apikey`, data plane `pre-dataagent-stream.aliyuncs.com/apikey` (no region segment; production = same hosts with region: `dataagent-{region}` / `dataagent-stream-{region}`). Set via `DATA_AGENT_ENDPOINT` / `DATA_AGENT_STREAM_ENDPOINT` (or `--endpoint` / `--stream-endpoint`) |
| Query (smoke) | `"ping"` / `"How many rows are there?"` |
| Query (E2E) | `"Analyze the game data and generate a summary report."` |

> `dms` subcommands read the credentials file since v2.7.1 (Fix 4); `ls` / `agent` remain the fixture-discovery path for API_KEY mode (the apikey gateway serves session/workspace/custom-agent listing, `dms` uses the AK/SK POP API).

## Matrix

| ID | Cred | Path | Command | Depth | Assertions | State |
|----|------|------|---------|-------|------------|-------|
| A1 | AK | send, no data source | `send -m "ping"` | Smoke | exit 0; `Session ID` printed; non-empty response; `status` → RUNNING / IDLE | ✅ PASS 2026-10-21 (pre/idbcloud) — `IDLE (server: init)` proves alias normalization on the wire |
| A2 | AK | send, db | `send --dms-instance-id <I> --dms-db-id <D> --db-name <DB> --tables <T> -m "..."` | Smoke | A1 + DescribeDataAgentSession shows db data source bound | ✅ PASS 2026-10-21 — detail shows `data_source_type: database` with tables + dms ids (binding OK; note the session itself fails data-prep on permission, see A5) |
| A3 | AK | send, file | `upload <file>` → `send --data-source <FILE_ID> -m "..."` | Smoke | A1 + file data source bound | ✅ PASS 2026-10-21 — detail shows `data_source_type: remote_data_center`, file_id matches, session RUNNING |
| A4 | AK | send, custom agent | `send --workspace-id <WS> --custom-agent-id <CA> -m "..."` | Smoke | A1 + no CreateDataAgentSession on the wire (`DATA_AGENT_DEBUG_API=1`) | ✅ PASS 2026-10-21 — exactly 1 SendChatMessage, 0 CreateDataAgentSession; `SessionConfig` carries `CustomAgentId` + `CustomAgentStage: prod` |
| A5 | AK | db pro → attach confirm → artifacts | `db --session-mode pro ... -q "..."` → `attach -s <ID>` → `attach -s <ID> -q "confirm"` → `reports -s <ID>` | E2E | plan emitted → `status` = WAIT_INPUT → confirm accepted → steps run → `result.json={"status":"completed"}` → `reports` lists ≥1 artifact → `status` = IDLE (server: RECOVERABLE) or STOPPED | ✅ PASS 2026-10-21 (file path, pre/dmstest) — full chain: plan (5 steps) → confirm → steps 1–5 → Report Render second confirm → confirm → 25 artifacts via `reports`. db path blocked by fixture permissions, not CLI: `internal_data_employees` mysql returns `DMS.DataAgent.CheckDatabasePermissionFailed` in data-prep (SSE `task_finish` payload); duckdb file-type database via `send --data-source` does produce a plan |
| A6 | AK | db pro, unattended via SessionConfig Skip* | `send --session-mode pro --dms-* ... -m "..." --poll` with `SkipPlan` / `SkipSqlConfirm` / `SkipWebReportConfirm` / `SkipAskHuman` set | E2E | **no** WAIT_INPUT at any point; completes; `reports` lists ≥1 artifact | BLOCKED — `send` does not expose Skip*, see [Capability gap](#send-vs-sendchatmessage-capability-gap) |
| B1 | API_KEY | file E2E (implicit session) | `file <path> -q "..."` → `status` → `attach` → `reports` | E2E | implicit creation works under API_KEY (no CreateDataAgentSession) → completes → artifacts | ✅ PASS 2026-10-21 (real key, pre gateways) — `Direct send (backend will create the session)`, no CreateDataAgentSession on the wire; plan (7 steps) → WAIT_INPUT → `attach -q confirm` → steps run → `result.json={"status":"completed"}`; `reports` downloaded 15 artifacts (2 TextReport + 13 DefaultArtifact). First 2 attempts died UNAVAILABLE early (pre instability, see Known issues). |
| B2 | API_KEY | send, custom agent (pre-create path) | `send --workspace-id <WS> --custom-agent-id <CA> -m "..."` | Smoke | CLI prints `API_KEY + custom agent pre-create`; CreateDataAgentSession then SendChatMessage on the wire; response non-empty | ✅ PASS 2026-10-21 (real key, pre gateways, workspace `71hx…`, `jufeng-test`) — pre-create banner + Session/Agent IDs printed, message accepted on the pre-created session (RequestId); `DescribeDataAgentSession` shows `sessionConfig.customAgentId` bound, session RUNNING |
| B3 | API_KEY | send db / send no data source | `send -m "ping"`; `send --dms-* -m "..."` | N/A-by-design | out of API_KEY support (file-only); record the failure shape and assert it is stable across versions, do not treat as regression | ✅ SHAPE RECORDED 2026-10-21 (fake key), re-verified on fix branch — gateway: transport HTTP 200 with body `{'code': 'Forbidden', 'httpStatusCode': 403, 'message': 'Invalid ApiKey.'}`; exit code deterministically 1 (the earlier "0 and 1 inconsistent" was a zsh pipe misread). v2.7.0 printed `Business failure: None None` (error-parsing gap, fixed in v2.7.1) |

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
| SSE stream dies with ECS metadata (`100.100.100.200`) credential refresh timeout under AK | Root-caused in v2.7.1: the SSE client ignored the credentials file and used the default chain (fixed — `build_credential_client()`). If it recurs it is a genuine credential-resolution gap; re-run `attach -s <ID>`, the server keeps running. Record occurrence count per run. |
| `attach` / GetChatContent on custom agent + workspace session returns `SessionUnavailable` | Pre-existing. Custom agent stays in the smoke tier (A4, B2); E2E tiers use db / file. Observed 2026-10-21 also on plain db sessions under pre/idbcloud (describe OK, content stream 404) — account-scoped, dmstest unaffected. |
| API_KEY + custom agent goes through CreateDataAgentSession | By design (backend limitation). B2 asserts the pre-create path, not its absence. |
| `internal_data_employees` mysql tables fail `CheckDatabasePermissionFailed` in pre data-prep | Fixture-level, not CLI. Use duckdb file-type databases or uploaded files for E2E. Confirmed 2026-10-21. |
| A fresh pre session flips `UNAVAILABLE` (`SessionUnavailable 会话已失效`) minutes after creation, rejecting further messages | Pre-environment instability, not CLI: sibling sessions behaved differently (RECOVERABLE vs UNAVAILABLE) with no client-side cancel. Retry the cell; when `status` reports UNAVAILABLE do not debug the CLI. Observed 2026-10-21 on 2 of 3 B1 attempts. |

## Findings 2026-10-21 (first run + API_KEY live run)

Real bugs / gaps surfaced by the matrix runs, in priority order. Items 1–4, 6–7 are fixed in v2.7.1 (`feat/cli-fixes-v2.7.1`) and re-verified live — the data-plane host override of item 6 ships as v2.7.2's `stream_endpoint`; item 5 is fixture-level and stays as guidance. Items 6–7 surfaced when the real API_KEY unlocked B1/B2.

1. **`task_finish` failure payloads are swallowed** — data-prep failures (e.g. `CheckDatabasePermissionFailed`) arrive in the SSE `task_finish` event but the worker prints `✅ 已完成分析，正在获取生成的报告和中间文件...` and `result.json` says `completed`. The session "succeeds" locally while the server never ran the analysis. Repro: `db --dms-instance-id 1987016 --dms-db-id 118916382 ... --session-mode pro` under pre; payload visible only via `attach --from-start --output raw`. **Fixed v2.7.1** — failure notice printed (`❌`), `result.json`/`status.txt` = `failed` with message, non-zero exit; detection by template prefix (`数据准备失败：` / `Data preparation failed:`). Re-verified on the failing fixture: failed + exit 1; success path (file lite E2E) unaffected.
2. **API_KEY business-failure rendering** — the API_KEY gateway answers with transport HTTP 200 + error body (`{'code': 'Forbidden', 'httpStatusCode': 403, 'message': 'Invalid ApiKey.'}`); `_print_send_result` / cancel only parsed the AK/SK RPC shape (`errorCode`/`errorMessage`) and printed `Business failure: None None`. Exit code was in fact consistently 1 — the "0 and 1" observation was a zsh pipe misread (`| tail; echo $?` returns tail's code). **Fixed v2.7.1**: both parsers fall back to `code`/`message`. Re-verified 2× with fake key: `Business failure: Forbidden Invalid ApiKey.`, exit 1 both runs.
3. **Post-confirm plan regeneration breaks the worker loop** — after `attach -q confirm`, the backend can emit a *new* plan (history recap + report generation) instead of continuing the old one; the worker prints `Confirmation processed. Subsequent steps will continue in the background.` and then goes silent — follow-up steps are only observable by re-attaching, and a WAIT_INPUT settle point could be recorded as `completed`. **Fixed v2.7.1** — the attach worker now re-checks the live session status after the reply stream (RUNNING → keep monitoring to the next settle point; WAIT_INPUT → `waiting_input`; FAILED/ERROR → `failed`); the misleading message is gone. Re-verified: `attach -q confirm` streamed plan recap → task report → report-render ask → completion inline, `result.json` written only after the server-state check.
4. **`dms` subcommands ignore the credentials file** — `list-instances` / `search-database` / `list-tables` build a `CredentialClient()` from the SDK default chain only, so credentials-file profiles (pre/dmstest etc.) cannot use them; on this machine they fall through to the ECS metadata endpoint and time out. **Scope was wider than `dms`**: the SSE client's AK path had the same gap — the A1–A5 runs only worked because `ALIBABA_CLOUD_*` env vars happened to be exported inline. **Fixed v2.7.1** — credential construction unified in `DataAgentConfig.build_credential_client()` and used by the main client, the SSE client (sync + async) and both DMS clients. Re-verified: `dms list-instances` under pre/dmstest returns 3252 instances (was metadata timeout); SSE reaches the gateway (`SessionUnavailable` on a fake session id, no metadata timeout); `send` smoke OK.
5. **`--dms-db-id` is int-typed** — file-type Data Center databases have `f-` string IDs and cannot be passed as db data sources; only `send --data-source` works for them. Fine today, worth remembering when fixtures are chosen.
6. **API_KEY endpoint overrides ignored, and control APIs misrouted to the stream gateway** — `_call_api_with_api_key` derived both endpoints from region, so `DATA_AGENT_ENDPOINT` had no effect in API_KEY mode (AK mode honored it): traffic targeting a non-public gateway (pre) silently went to the public region host. Worse, the legacy data-plane list also routed `ListFileUpload` / `DescribeDataAgentUsage` / `UpdateDataAgentSession` / `CreateDataAgentFeedback` to the stream gateway, where they answer `403 Invalid Action` — `reports` under API_KEY printed "No report files found" even with artifacts sitting on the server (the error shape has no `data` key, so it failed silent). **Fixed v2.7.1** — the control plane honors `endpoint` (`--endpoint`) and the data-plane list is narrowed to the two chat APIs the stream gateway actually serves (`SendChatMessage`, `GetChatContent`), verified per-action against the pre gateways; the data-plane host override itself ships in v2.7.2 (`stream_endpoint`: `DATA_AGENT_STREAM_ENDPOINT` / `--stream-endpoint`, default `dataagent-stream-{region}.aliyuncs.com/apikey`). Re-verified: control calls reach the pre gateway, `reports` lists and downloads all artifacts.
7. **API_KEY SSE wire defects made streaming dead on arrival** — `GetChatContent` was sent with `Accept: application/json` (the apikey stream gateway then answers transport-200 `INTERNAL_DATA_AGENT_ERROR` and no SSE events arrive at all) and carried `AgentId` in the body (which switches the gateway into a non-terminating tail mode: replay without `chat_finish`/`SSE_FINISH`, stream hangs until timeout). Symptom in B1 first run: worker consumes zero events, `result.json` still says `completed`. Isolated by a three-variable curl A/B against the pre stream gateway. **Fixed v2.7.1** — `Accept: text/event-stream`, `AgentId` omitted from the body (the server resolves it from SessionId alone). Re-verified: full turn semantics (chat_finish → SSE_FINISH → clean close, same as the AK/SK gateway); B1 E2E streams plan and steps.

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
| 2026-10-21 | v2.7.1 (feat/cli-fixes-v2.7.1) | fix re-verification, not a full matrix pass | All 4 fixes verified live under pre/dmstest | Fix 1 on failing db fixture → `❌ 数据准备失败…`, `result.json` failed, exit 1 (was fake success); file lite success path unaffected (plan → confirm → completed). Fix 2 fake-key 2×: `Forbidden Invalid ApiKey.` exit 1 both. Fix 3 `attach -q confirm` streams continuation inline, result written after server-state check. Fix 4 `dms list-instances` 3252 instances via credentials file; SSE gateway reached without env AK. Full matrix re-run pending (B1/B2 still need an API_KEY). |
| 2026-10-21 | v2.7.1 + v2.7.2 (feat/cli-fixes-v2.7.1) | B1, B2 (real API_KEY, pre gateways) | B1 PASS, B2 PASS | Real pre-issued key unlocked B1/B2 and surfaced Fix 5 (endpoint overrides ignored + control APIs misrouted to stream gateway) and Fix 6 (SSE `Accept` header + `AgentId` tail-mode) — before the fixes B1 got zero SSE events with a local "completed", and `reports` was silently empty. After: B1 implicit creation → 7-step plan → confirm → completed → 15 artifacts; B2 pre-create path with `customAgentId` bound. 2 of 3 B1 sessions died UNAVAILABLE early (pre instability, see Known issues). B3 re-confirmed on real key: `Forbidden Invalid ApiKey.` shape only on wrong-gateway/production hosts. |

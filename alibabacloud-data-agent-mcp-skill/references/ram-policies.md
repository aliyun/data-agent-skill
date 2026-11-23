# RAM Policy Requirements

This Go MCP skill signs Data Agent, DMS Enterprise, and optional Agentic metadata OpenAPI requests with AK/STS from the credentials-go default chain. The Server's API Key runtime mode has been removed; see [migration and credentials](INSTALLATION.md#credentials). This does not remove inbound platform tokens/JWT, STS SecurityToken, or OSS upload signatures.

Use only permissions established as necessary for the deployment. Do not recommend `AliyunDMSFullAccess` or `AliyunDMSDataAgentFullAccess` as a default or a workaround for unverified gateway permissions. Existing broad policies are **not evidence** of least privilege or Agentic data authorization; this document does not change deployed permissions.

**Security remediation is implemented locally; deployment and real-cloud release gates remain unverified.** Historical cloud evidence below predates the remediation and does not prove production acceptance. See [build, transport, secret-file and release requirements](INSTALLATION.md#post-remediation-release-gate--not-yet-verified). Permission, role/trust-policy, secret, migration and deployment changes need separate explicit administrator authorization; do not change policies to manufacture differential test results.

Existing Data Agent / DMS custom-policy action reference (not a verified minimum for the new gateway):

```json
{
  "Version": "1",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "dms:CreateDataAgentSession",
        "dms:DescribeDataAgentSession",
        "dms:SendChatMessage",
        "dms:GetChatContent",
        "dms:InitDataAgentPersonalWorkspace",
        "dms:ListDataAgentWorkspace",
        "dms:ListCustomAgent",
        "dms:ListFileUpload",
        "dms:DescribeFileUploadSignature",
        "dms:FileUploadCallback",
        "dms:GetActiveRouteUnit",
        "dms:GetUserActiveTenant",
        "dms:ListTagMetaAsset",
        "dms:TagMetaAsset",
        "dms:ListTables",
        "dms:ListInstances",
        "dms:SearchDatabase"
      ],
      "Resource": "*"
    }
  ]
}
```

The RAM principal must also have DMS-level access to the target instances, databases, and tables. If database discovery works but analysis fails with `CheckDatabasePermissionFailed`, have the administrator review the required DMS data permissions or use a workspace/database that the user can access. Do not automatically broaden permissions.

## Identity roles and source credentials

The base SDK credential provider is retained as the source for RAM AssumeRole; the source principal needs `sts:AssumeRole` on each mapped role, and the role trust policy must permit that principal. Dynamic refresh depends on the source and role providers; static temporary credentials supplied as environment values do not renew themselves.

**Every HTTP/SSE deployment**, gateway on or off, requires `identity.enabled=true`, `require_identity=true`, nonempty `auth_token` and a stable administrator-selected `namespace`. All tools and protocol methods (initialize, list/call, notifications, GET/POST/DELETE) authenticate and validate server-issued principal-owned IDs where present. No anonymous/global-AK fallback or weak protocol ownership checks are allowed. stdio uses one local process identity, not chat-user authorization.

When JWT is enabled, a completely absent JWT permits trusted Header identity under shared mappings; a present empty, invalid or expired JWT never falls back. A valid HS256 JWT with unexpired `exp` and user ID or email takes precedence over plain identity headers. Shared groups/defaults omit both tenant/agent mapping fields and accept Header identity or valid JWTs without scope claims. Explicitly scoped groups/defaults require both signed JWT scope claims to match exactly; Header identity cannot match them. Mixed shared/scoped and partial mapping scopes remain invalid. Exactly one named match takes precedence; default applies only to authenticated unmatched users within its configured scope. Multiple group matches reject even if they share a RoleArn. Both Header paths require a trusted upstream that overwrites self-reported headers and ensures namespace-wide identifier uniqueness. Never distribute its platform token to ordinary callers; do not remove a rejected JWT to retry as Header identity. With JWT disabled, any JWT header is rejected.

All configured JWT keys have equivalent signing authority in one trusted identity domain, for rotation or mutually trusted applications; independent issuers need separate instances/configurations/storage roots. Namespace and key rotation do not replace claim validation or scoped role authorization.

Different RoleSessionName values distinguish audit sessions but **do not isolate cloud row/column permissions**. Users sharing a role normally share effective cloud scope; distinct permissions need differently authorized roles or a separately verified service-side mechanism. `identity-v2/<principal_id>/` and `owner.json` establish only local history ownership, not cloud session ACLs. No old-history fallback or automatic migration exists: imports require separately authorized administrator proof of unique ownership, not just a matching role or successful DescribeSession. See [identity/migration](INSTALLATION.md#multi-tenant-identity-mode-required-remotely), [capacity/lease limits](INSTALLATION.md#remote-http-limits-and-protocol-lifecycle) and [actual secret-file requirements](INSTALLATION.md#secret-files).

## Agentic metadata gateway: P0 authorization validation pending

The nine base `data_gateway_*` tools use `agent_gateway.enabled` and call:

- `ListAgenticCatalog`, `SearchAgenticCatalog`, `GetAgenticCatalog`
- `ListAgenticDatabase`, `GetAgenticDatabase`, `SearchAgenticDatabase`
- `ListAgenticTableBaseInfo`, `GetAgenticTableColumn`, `GetAgenticTableIndex`

P2b adds only `ListAgenticDataSources` / `GetAgenticDataSource` summaries behind `agent_gateway.data_sources_enabled` (default `false`, requires `enabled=true`). Tool counts before host filtering: 19 off / 28 base enabled (9 gateway) / 30 with P2b (11 gateway). P2a uses the base switch and leaves the original six contracts unchanged.

Confirmed P2a authorization definitions (not a policy change or production acceptance):

| OpenAPI Action | Official RAM action | Authorization level | Resource / Condition |
|---|---|---|---|
| `GetAgenticCatalog` | `dms:GetAgenticCatalog` | Operation-level (`authLevel=operate`) | Resource `*`; no listed Condition |
| `SearchAgenticDatabase` | `dms:SearchAgenticDatabase` | Operation-level (`authLevel=operate`) | Resource `*`; no listed Condition |
| `GetAgenticDatabase` | `ramActions` is still empty; awaiting official definition | Unconfirmed | Minimum RAM authorization pending; do not guess an ARN |

The official operation-level Resource `*` is not proof of database/table data permission. An empty `ramActions` list is not permission-free access. Confirm Action, Resource and Condition against official definitions and authorized tests; do not invent fine-grained resource ARNs or use FullAccess to hide missing definitions. Existing policies and permissions are unchanged.

### P2b read-only control-plane summaries

**Official sources differ:** the machine-readable RAM JSON lists the following Actions, but the help pages state that no authorization information is available. Preserve this discrepancy as an acceptance item; do not interpret either source as proof of permission-free access or verified minimum authorization.

| OpenAPI Action | RAM action in machine-readable JSON | `authLevel` | Resource | `conditions` |
|---|---|---|---|---|
| `ListAgenticDataSources` | `dms:ListAgenticDataSources` | `operate` | `*` | `[]` |
| `GetAgenticDataSource` | `dms:GetAgenticDataSource` | `operate` | `*` | `[]` |

These are source metadata, not a recommended policy grant. Do not guess a finer-grained Resource or recommend `AliyunDMSFullAccess`. Resolve the documentation discrepancy and validate the actual required permissions before release.

P2b scope is the **current upstream RAM/STS principal's control-plane visibility, not the chat user's database ACL**. Same-role STS sessions share permissions; RoleSessionName is audit attribution only. Before publishing this capability, the deployer must explicitly accept the summary visibility scope for mapped users; otherwise leave `data_sources_enabled=false`. The current positive test does not prove account-wide visibility, cross-role isolation or least privilege.

The local List tool exposes only optional uppercase-format `db_type`, `page_size` (default 50, range 1..100) and `next_token`, not `search_key`, `Host`, `State` or other filters. Get accepts only required, unchanged `data_source_uuid` and has no pagination. Both summaries allow only eight strings (`DataSourceUuid`, `Alias`, `DbType`, `EnvType`, `InstanceSource`, `RegionId`, `CatalogUuid`, `CatalogName`) and signed int32 `State`. Preserve optional-field nulls; nonempty entities require `DataSourceUuid`, and detail UUID must match the request. Authentication/network/permission/unknown fields and `TotalCount` are removed. This is not server-wide Host confidentiality: root Catalog search still supports address prefixes and existing Data Agent discovery is unchanged.

**Historical direct upstream evidence (2026-10-29, pre-remediation):** a configured test role's STS in `cn-hangzhou` successfully called List/Get. Raw detail had 23 fields including `Host` and `AuthPassword`; their values were not recorded. A format-preserving nonexistent ID returned HTTP 404, `Code=DATA_SOURCE_NOT_FOUND`, missing `Data`, with a real upstream RequestId recorded in historical evidence. No role/resource/request identifier is an example deployment input.

That 404 stays `ERROR` under the existing `GATEWAY_UPSTREAM_INCOMPATIBLE` mapping; no new error code or successful-null conversion is added. Current Get accepts only a valid `Data` object: missing `Data`, `{}` (including an empty `Data` object), `Data:null` and wrong shapes are incompatible. Planned successful `Data:null` support is deferred this round for lack of a real sample; optional-field null preservation is not support for a null detail.

### Validation still required

**Historical checks, before remediation:** P2a automation and all nine base Actions' real-cloud discovery passed in stdio and configured-role HTTP/SSE in Hangzhou. P2b automated tests, race, vet and independent-binary stdio/HTTP/SSE checks for all 19/28/30 configurations also passed, including real pagination, type filtering, nine-field sanitized details and missing-source errors. These used process SDK identity in stdio and configured-role STS remotely, not the remediated binary. They do not establish minimum permissions, cross-role isolation or production data ACL enforcement.

**First cross-role attempt, 2026-10-29: STS rejection, since resolved on retry.** Test role A obtained temporary credentials; role B initially failed AssumeRole with `NoPermission` and an STS RequestId. This was not an Agentic API denial or an empty visible-resource set. A separate same-server Streamable HTTP test mapped two users to these roles with no default mapping: A listed five data sources and five Catalogs (both had more pages) and read their first details; B's two lists and two details using A's known IDs all returned `GATEWAY_CREDENTIAL_ERROR`, without data, pagination or Agentic upstream RequestId; A's subsequent details still succeeded. All ten calls met these assertions. This verifies rejection without fallback for that historical credential failure, not resource isolation between two usable roles or post-remediation acceptance.

**Same-day retry: both roles usable, observed visibility equal.** Both obtained STS credentials. With the same two explicit role mappings and no default, each exhausted two data-source pages and one Catalog page: 28 data sources and 28 Catalogs per role, identical identifier sets with no exclusive items. Three sampled details of each kind succeeded for both. Across three sampled common Catalogs, complete database lists contained 10/58/7 items with identical sets; two databases per Catalog yielded identical table sets of 68/118/68/0/8/0 items. Three sampled tables had matching column-name sets of 6/4/6 and index-name sets of 1/1/2; a fourth table's columns and indexes returned `GATEWAY_ACCESS_DENIED` for both roles, with no data or usable upstream RequestId. Those errors are not empty sets or proven upstream ACL isolation. The final run observed 74 calls: 70 successes and four shared denials, covering nine tools but not the two search tools. B's successful data-source list/detail responses included upstream RequestIds; shared denials without one remain un-attributed.

The temporary harness initially failed an overstrict assertion requiring an upstream ID on every denial; the existing envelope permits its absence. Only that harness assertion was corrected to retain an evidence gap, and the same denials recurred. No product behavior was changed. Intended permission differences and an A-allowed/B-denied resource are still needed to validate isolation; identical visible sets do not establish identical policies, account-wide visibility or SQL permission. No deployed configuration, cloud policy, trust relationship or resource was changed, and resource identifiers or sensitive field values were not retained in the evidence.

**Post-remediation external gates remain open.** Before multi-tenant release, authorized testing on the rebuilt binary must confirm:

1. Every enabled Action accepts the deployed STS mode in the selected region through stdio/HTTP/SSE, including initialization requirements. Resolve the missing `GetAgenticDatabase` RAM definition and the P2b machine-readable/help-page discrepancy; establish minimum permissions without broadening them by default.
2. Distinguish RAM API permission, control-plane summary visibility and database/table data permission. Do not equate P2b summaries with a chat user's database ACL; metadata visibility or platform ADMIN does not imply SQL permission. Record deployer acceptance of the summary scope or keep P2b off.
3. Two differently authorized roles have an explicit **A-allowed/B-denied** resource and expected-results table: B cannot enumerate it and cannot fetch it using A's known ID; failures never switch roles. Equal visible sets or common denials without upstream RequestId do not pass this gate. Same-role users must separately pass local-history/protocol-channel isolation; that is not evidence of cloud row isolation.
4. Gateway credential rotation, expiry and refresh failure behave correctly, without stale/global-identity fallback. Retain the SDK default chain; static STS environment credentials do not self-renew. Preserve sanitized success, empty, pagination and denial evidence without raw sensitive values. Do not extend the gateway strict-refresh claim to old analysis snapshots.
5. The **real JWT platform**, not just locally signed fixtures, supplies authentication and required scoped claims for initialize, sustained SSE, notifications and DELETE, with renewal/reconnection. If any method cannot authenticate, block integration rather than exempt it. dacli's `--jwt` / `AILY_JWT` and authenticated notification/DELETE support validate Streamable HTTP only, not the separate SSE transport.
6. Go 1.26.10-or-newer supported/rescanned artifacts and applicable automated/fuzz/source/binary scans pass the [release checklist](INSTALLATION.md#post-remediation-release-gate--not-yet-verified); actual secret files meet ownership/0600-or-0400 requirements, and any offline migration is explicitly authorized and ownership-verified. Old read-only analysis/multi-turn regression must not resend unknown messages or be counted as gateway SQL/row-protection evidence.

Without that evidence, do not claim production validation or multi-tenant gateway isolation. P2c/P3 and Wiki/ACL remain unimplemented; no gateway SQL history/execution, data-source writes, connection tests, Token, DataWiki or ACL/self-permission tools are exposed. No placeholder `sql_history_enabled` switch is added. The existing Data Agent SQL path is unchanged and **not protected by these metadata tools**. Gateway UUID/QualifiedName values are not Data Agent session `database_id` values.

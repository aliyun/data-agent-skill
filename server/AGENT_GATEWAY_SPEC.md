# AI 数据网关集成 MCP Server — Spec

- 状态：首期 6 个元数据工具、P2a 3 个扩展工具及默认关闭的 P2b 2 个数据源摘要工具已实现，当前注册数为 19/28/30；本地和真实云端验证见第 2.2 节。P2b 成功 null 分支待补证；P2c/P3 和 DataWiki/ACL 尚未实施，不代表完整多租户或生产验收通过。
- 日期：2026-10-29（后续接口盘点快照为 2026-10-28）。
- 目标项目：`server/`，现有 Go Data Agent MCP Server。
- 已定架构：直接调用 OpenAPI，复用 SDK/RAM Role/STS 身份链；不维护逐用户网关 Access Token，整个 Go Server 不再支持 Data Agent API Key 模式。
- 本次交付：仅更新 Spec，盘点其他接口并冻结下一阶段范围、工具契约与验收门槛；不实现新工具，不修改真实配置、权限或部署，不改独立 Python CLI。

**2026-11-02 安全修复实施：**见 [Go MCP Server 安全修复 Spec](SECURITY_REMEDIATION_SPEC.md)。本地修复已实现，覆盖整个 Go Server，并替代本文历史版本中“旧远程身份回退不变、单字符串租户键、旧目录自动恢复、初始化持全局锁”等兼容边界；网关业务工具契约不变。最终原生产物已独立启动，三传输九配置真实元数据、协议边界及同角色不同用户本地历史隔离复验通过；旧分析追问的严格固定标记校验未通过。本文旧版云端实测不替代修复版本验收，最新证据及真实身份平台、差异角色、部署等未关闭的发布门禁以安全 Spec 第 9 节为准，不代表生产验收通过。

## 1. Context 与方案决策

现有 MCP Server 将 Data Agent 会话式分析、文件分析、数据发现等能力暴露为 19 个 `data_agent_*` 工具。希望在同一个入口增加 AI 数据网关能力，逐步覆盖受控数据发现、结构理解、业务语义与安全查询。

**推荐方案：新增 `data_gateway_*` MCP 工具，南向直接调用 `Dms/2025-04-14` 的 Agentic OpenAPI，复用现有“用户 → RAM Role → STS”认证授权链。** 不再连接官方网关 MCP，也不维护“用户 → 网关 Access Token”映射。

**认证统一为阿里云 SDK 凭证链提供的 AK/STS：整个 MCP Server 不再支持 Data Agent API Key 模式，不受网关开关影响。** 原 `data_agent_*` 的普通 API 和 SSE 数据流也只保留签名调用；这是认证配置的破坏性变更，不改变 AK/STS 下的工具参数和业务语义。独立 Python CLI Skill 不属于此次移除范围。

这一变更消除的是网关 Token 的发放、绑定和轮换维护，不是身份与权限本身：

```text
原提案：已验证用户 → 单独维护的网关 Token → 官方网关 MCP
新提案：已验证用户 → 现有 identity 分组 → RAM Role → STS → Agentic OpenAPI
```

接入形式和可交付能力必须分开：已确认的元数据 OpenAPI 可以先落地；SQL、DataWiki、权限自查等能力，只有在确认等价接口和真实授权行为后才能加入。不能因为发现了 OpenAPI，就假定它完整覆盖官方网关 MCP。

> 边界：新增网关工具不会自动让原 `data_agent_*` SQL 经过网关，不能宣称旧分析链路因此获得网关行列保护、审批或审计能力。若要求所有数据库访问强制经过网关，需要另行设计旧工具限制或执行链路迁移。

## 2. 官方事实与能力范围

以下事实来自文末来源；本地工具名、配置、限额、结果封装均为项目设计。

| 能力 | 已确认事实 | 本 Spec 处理 |
|---|---|---|
| 元数据 OpenAPI | 6 个首期 Action 均支持 HTTPS GET/POST，`security: AK`，业务参数位于 query [S1] | 首期直接接入，不要求额外 AgentId/Access Token 参数 |
| 元数据标识 | Catalog、Database 使用 UUID，表使用 QualifiedName [S1][S5] | 不与旧 DMS 数字 ID、工作区 database_id 混用 |
| 鉴权 | DMS 支持 RAM 角色及 STS；AssumeRole 权限取决于角色策略及可选会话策略 [S7][S8] | 复用现有身份链；每个 Agentic Action 的 STS 兼容仍须联调 |
| 数据授权 | 平台管理员、元数据可见性、SQL 权限不是同一概念 [S2] | RAM API 调用权不等于库表访问权，角色到网关数据权限的映射必须验证 |
| SQL 凭证 | `CreateAgenticSqlExecuteCredential` 返回 OneQuery 临时执行凭证，有效 10 分钟、最多 500 次连接 [S1][S3] | 不是 SQL 执行 Action，不作为对外 MCP 工具 |
| SQL 执行 | 官方 MCP 有 `exec_sql`，但已查 OpenAPI 未确认完整的等价执行、结果取回、取消协议 [S2][S3] | 保留后续目标，首期不注册 SQL 工具 |
| DataWiki | 官方 MCP 有 `wiki_search`；OpenAPI 有 `RetrieveKnowledgeBase`，但尚未证实两者等价 [S1][S2] | 不用普通知识库检索冒充 DataWiki |
| 权限自查 | MCP 有 `acl_whoami`、`acl_my_permissions`，对应 OpenAPI 尚未明确 [S2] | 不注册占位工具，也不伪造权限快照 |
| 审计 | 有 `ListAgenticSqlExecutionHistories` 及全局历史 Action，使用 Page/Size，不是 NextToken [S1] | 后续独立评审，避免扩大敏感历史数据暴露面 |
| 数据保护 | 脱敏字段进入计算链路可能被拒绝；行级保护仅对查询生效，写操作不做行级拦截 [S4] | 未来 SQL 必须验证真实执行路径的策略，不把产品说明当作已验证保障 |

### 2.1 三个不能混淆的结论

1. **接口标记 AK ≠ 已证明每个 STS 角色可正常访问。** 首期每个 Action 都要验证角色身份、服务开通/用户初始化和数据授权要求。
2. **取消网关 Token 绑定 ≠ 不需要用户映射。** 多用户部署仍复用 `identity.groups` / `identity.default` 的角色映射。
3. **获取 SQL 凭证成功 ≠ SQL 已执行。** OneQuery 后续连接协议、结果与授权传播未确认前，不提供查询闭环。

### 2.2 当前实现与实测基线

以下是本项目本地验证记录，不是官方产品承诺：

- Go Server 的 API Key 运行分支已移除；首期自动化测试、race 检查和 vet 已通过。
- 独立二进制在 stdio 下关闭网关注册 19 个工具、开启注册 25 个；真实云端跑通全部 6 个元数据工具，包括 Catalog 翻页、空表列表和字段/索引查询。
- Streamable HTTP 与 SSE 已验证现有 default 角色的 AssumeRole 和真实 Catalog 查询；**未据此证明每个 Action 在所有角色下均可用**。跨角色反向授权、真实 JWT 平台接入及服务端权限传播仍需验收。
- 旧 `data_agent_*` 分析链已完成真实只读连通性查询和 3 行样本追问。这只证明旧分析链路可用，**不计入网关 SQL、OneQuery、行列保护或网关审计的验收证据**。
- 2026-10-29，P2a 三个 Action 已通过全量 Go 测试、race、vet 与独立代码审查；本地覆盖对象/数组互换拒绝、目标 UUID 一致性、可选筛选校验、精度/NULL、未知字段丢弃、19/28 注册和原工具回归。
- P2a 真实云端在 `cn-hangzhou` / `dms.cn-hangzhou.aliyuncs.com` 验证：stdio 使用进程 SDK 身份；Streamable HTTP、SSE 使用既有 default 角色 AssumeRole。三种传输均跑通全部 9 个元数据 Action、“数据库搜索 → 详情 → 表 → 字段/索引”、搜索第二页游标、限定/跨 Catalog 搜索、DbType/EnvType 筛选和无命中空数组；非法筛选在本地拒绝且没有上游 RequestId。Catalog/Database 详情样例分别保留 7/11 个白名单字段，无分页。
- P2a 新增 Action 的 HTTP/STS 成功 RequestId：GetAgenticCatalog `01A1854E-4C7F-5B51-A56B-23588169528F`、GetAgenticDatabase `01A1854E-4D5F-5A75-86DC-82572D0485D1`、SearchAgenticDatabase `01A1854E-4D93-5918-A2FF-979120E0BBD7`。这些仅证明该地域、既有角色及所测资源的调用成功，不证明最小权限或跨角色隔离。
- 首轮 stdio 初始化两次超时，未执行云端调用；原二进制的原始协议诊断、stdio 复测及最终全套四个启动场景随后均通过，未修改 Server 启动代码或安全配置，首次超时根因未确认。HTTP 客户端关闭时有不影响调用结果的 DELETE connection-reset 日志。
- P2a 轮次未修改真实配置、云端权限或资源，未调用 P2b/P2c/P3、管理或秘密签发接口。GetAgenticDatabase 最小 RAM 定义、详情不存在/null 语义、跨角色反向授权和真实 JWT 平台联调仍待补证，不能据上述结果宣布生产发布门槛全部通过。
- 2026-10-29，P2b 全量 Go 测试、race、vet 通过；覆盖独立开关、19/28/30 注册、参数拒绝、白名单与 signed int32、NULL/缺失/对象/数组负例、目标一致性及原 9 个工具回归。
- P2b 独立二进制在 stdio、Streamable HTTP、SSE 各验证关闭/基础/含数据源三档，共 9 个启动场景全部通过（17.76 秒）；三种传输均真实跑通列表首/次页、DbType 筛选、匹配 UUID 的详情及不存在项 ERROR，同时复测基础 9 个 Action。stdio 使用进程 SDK 身份，两个远程传输使用既有 `da-role` 的 STS 身份。
- 原始详情样本有 23 个字段（包含 Host、AuthPassword，未记录其值）；MCP 详情只返回 9 个白名单字段。HTTP/STS 的 List/Get 成功 RequestId 分别为 `01A1856C-3F65-599E-AE5E-AAE22387CE04` / `01A1856C-3FCC-54C5-8AFF-08F7B81A3B22`。不存在项真实返回 404 而非成功 null，详见 7.2。
- P2b 验证仅对既有主体及有限样本成立，没有验证普通角色与管控角色的范围差异、跨账号 UUID、最小权限或真实 JWT；不能推断角色看到全部账号资源，也不能宣称逐聊天用户隔离。真实配置仍未启用 P2b，未修改云端权限、调用连接测试或其他管理/SQL 接口。
- 2026-10-29 首轮真实双角色预检中，`da-role` 成功获取 STS 临时凭证，`DatabuddyAssumeRole` 在 AssumeRole 阶段返回 `NoPermission`，STS RequestId 为 `01A18574-77BB-5179-8320-59D7C55AE2C9`。该轮第二角色未进入 Agentic API 授权或资源查询阶段；当时可见范围未测得，不是空集合。此 STS 阻塞在同日后续重试中解除，见下文。
- 首轮同一独立 Streamable HTTP MCP 实例以临时配置分别映射两个测试用户，关闭 default 回退，共执行 10 次工具调用（1.59 秒）：A 的数据源/Catalog 列表各取 5 条且均有后页，并成功获取首项详情；B 的两项列表及 A 已知标识的两项详情均返回 `GATEWAY_CREDENTIAL_ERROR`，无 data、pagination 或 Agentic 上游 RequestId；A 再次获取两项详情仍成功。A 首次数据源/Catalog 详情 RequestId 分别为 `01A18578-F2CD-585C-B99A-ACA6237EA2F6` / `01A18578-F331-514D-98A7-AAD0A933FA3C`。仅保存状态、计数及关联 ID，不保存资源标识、名称或敏感字段值；客户端关闭时仍有 DELETE connection-reset 日志，工具调用断言全部通过。
- 上述首轮交替调用只证明当时凭证失败明确拒绝、没有回退到可用角色，以及 A 的后续调用未受影响，不证明两个可用角色间的资源隔离。
- 同日按用户要求重试，两个角色均成功获取 STS 临时凭证（0.33 秒）。在 `cn-hangzhou` 的同一独立 Streamable HTTP MCP 实例中，以临时分组分别映射 A=`da-role`、B=`DatabuddyAssumeRole`，不配置 default。两角色均取完数据源列表 2 页和 Catalog 列表 1 页：各 28 项，按原始标识比较交集均为 28，A/B 独有项均为 0；各抽 3 个数据源及 3 个 Catalog，两个角色均可读取对应详情。B 的数据源首/次页 RequestId 为 `01A186CC-F05D-547D-97CF-273611A7637D` / `01A186CC-F0B7-5FBF-99D2-C33144A3BD8D`，首个详情为 `01A186CC-F13B-5786-ADA3-C5957CE24F54`。
- 继续抽取 3 个共同 Catalog，分别取完 10/58/7 个数据库的列表，标识集合均相同；每个 Catalog 抽 2 个共同数据库，双方详情成功，6 个库的表列表分别为 68/118/68/0/8/0 项，分页取完后集合均相同。4 个非空库各抽一张共同表：其中 3 张双方字段名集合为 6/4/6、索引名集合为 1/1/2，均相同；另一张的字段及索引查询在双方均返回 `GATEWAY_ACCESS_DENIED`，无 data 或可用上游 RequestId，不能记作空集合或已归因的服务端 ACL 拒绝。该样本字段调用的本地关联 ID 为 A=`44e768a6`、B=`dd64f004`，索引为 A=`32dc28f7`、B=`643ec246`。
- 本轮最终观测共 74 次调用、9 种工具：70 次成功，4 次为上述共同拒绝，耗时 4.82 秒；未测试两个搜索工具。扩展验收脚本首轮因错误地要求所有拒绝携带上游 RequestId 而失败；核对现有可选关联 ID 契约后，仅修正临时脚本将缺失 ID 记录为证据缺口，复测仍观察到同样拒绝，未修改产品代码或将错误转换为成功。**已测可见集合一致，不是跨角色隔离验收通过**：未提供预期授权差异，未获得“A 可见、B 应不可见”的负例，也未验证所有库表、跨账号、SQL/行列保护或真实 JWT。上述测试均未修改真实配置、RAM 策略、信任关系或云端资源；P2b 真实部署开关保持关闭。

### 2.3 其他网关 OpenAPI 清单（2026-10-28 快照）

[S1] 本次快照共 133 个 Action，其中 20 个名称含 `Agentic`，另有描述明确为“创建数据网关 Agent 身份”的 `CreateAgent`。扣除已集成 6 个后，本次识别 **15 个其他网关相关 Action**；这是此版本公开定义中的范围，不声称覆盖全部内部或未来接口。下表全部为 HTTPS、AK 签名，业务参数位于 query；3 个元数据扩展支持 GET/POST，其余为 POST。

| Action | 官方用途 / 返回形态 | 副作用与敏感边界 | 本方案决策 |
|---|---|---|---|
| `GetAgenticCatalog` | `CatalogUuid` → `Data: AgenticCatalog` 对象 | 只读；扩展属性可能敏感 | P2a，实例详情 |
| `GetAgenticDatabase` | `DatabaseUuid` → `Data: AgenticDatabase` 对象 | 只读；引用模型已按 SDK 补核，RAM 定义仍缺 | P2a，数据库详情 |
| `SearchAgenticDatabase` | 跨 Catalog 搜索数据库；`Data[]`、`NextToken` | 只读；不传 Catalog 时扩大搜索范围 | P2a，受服务端授权约束的搜索 |
| `ListAgenticDataSources` | 当前账号的数据源管控列表；`Data[]`、`NextToken`、可选 `TotalCount` | 只读不等于用户数据面隔离；含认证/网络属性 | P2b，独立显式启用 |
| `GetAgenticDataSource` | `DataSourceUuid` → 数据源对象；不存在时 `Data` 为空 | 同上，另含策略、VPC 等配置 | P2b，独立显式启用 |
| `ListAgenticSqlExecutionHistories` | 当前上游调用主体可见历史；`Data[]`、`TotalCount` | SQL、执行者、IP 等敏感；Page/Size 分页 | P2c，仅精简历史摘要 |
| `ListAgenticGlobalSqlExecutionHistories` | 当前租户全部 SQL 历史 | 全局数据暴露，即使只读也属管理面 | 不开放给通用分析 MCP |
| `CreateAgenticSqlExecuteCredential` | `Data` 为 OneQuery 临时执行凭证 | 官方标记 write；签发秘密，不是执行 SQL | P3 执行链内部使用，当前不实现 |
| `AddAgenticDataSource` | 新建数据源及关联配置，返回详情 | write；接收密码、角色、网络和安全配置 | 管理面，不集成 |
| `UpdateAgenticDataSource` | `DataSourceUuid` 定位并更新连接/认证/展示信息 | write；可能改变访问目标和策略 | 管理面，不集成 |
| `DeleteAgenticDataSource` | 删除数据源及关联元数据目录，`Data:boolean` | write；影响网关访问及元数据，不等同删除底层数据库 | 管理面，不集成 |
| `TestAgenticDataSourceConnection` | 网络及认证连通性，`Data:{Success,Message}` | 官方标记 read，但会主动连接目标，入参可含密码/Host/RoleArn | 不作为普通只读工具；不得自动探测 |
| `CreateAgent` | 创建网关 Agent 并自动签发 `Data.ApiKey.Secret` | write；身份创建及一次性明文 Access Token | 与既定身份架构无关，不集成 |
| `GetAgenticAgentByInstallToken` | 用 `InstallToken` 查兑换后的 Agent；未兑换时为空 | read，但依赖安装令牌流程，返回拥有者及 Token 前缀 | 不集成，不作为 `whoami` |
| `ResetAgenticApiKey` | 重置当前创建者的控制台 Access Token，旧 Secret 立即失效 | write；一次性返回新 `Secret`，可中断现有调用 | 不集成，不恢复 Server API Key 模式 |

`CreateAgent` / `ResetAgenticApiKey` 中的 ApiKey 指网关 Access Token；已移除的 Server Data Agent API Key、阿里云 RAM AK/STS、OneQuery 执行凭证是不同对象，不能因字段同名而混用。

**公开定义的缺口：** `GetAgenticAgentByInstallToken` 描述引用 `IssueAgenticInstallToken`，但该 Action 不在本次 [S1] 快照中；不能根据引用自行构造安装/兑换流程。`AgenticDatabase` 被多个接口引用，但快照的 `components.schemas` 未包含其定义；P2a 已以官方 SDK [S15][S16] 补证字段类型（第 7.1 节），不按引用名臆造字段；最小 RAM 定义仍待补齐 [S17]。

### 2.4 官方 MCP、知识库与 OpenAPI 的对应边界

[S2] 当前列出 13 个工具：6 个 `meta_*`，以及 `datasource_list`、`acl_whoami`、`acl_my_permissions`、`wiki_search`、`exec_sql`、`gateway_describe`、`gateway_call`。它们不是本项目的注册表，也不能直接照搬其参数/分页/状态。

| 官方能力 | 本次核对结果 | 决策 |
|---|---|---|
| 数据源发现 | `ListAgenticDataSources` 提供明确管控面契约；不据工具名认定与 `datasource_list` 授权范围完全相同 | 用本地静态工具接 OpenAPI，先验证范围 |
| `acl_whoami` / `acl_my_permissions` | 当前身份/真实数据权限能力明确，但所核公开资料未建立等价 OpenAPI 映射 | 等待正式契约，不用本地角色、`CreateAgent` 或安装令牌接口替代 |
| `wiki_search(wikiUuid, query)` | 面向 DataWiki 的表含义、样例 SQL、业务口径；与通用知识库的标识映射及检索范围未证实 | DataWiki 单列契约门槛 |
| `RetrieveKnowledgeBase` | POST **formData**；必填 `KbUuid`、`Query`；`Data.Results[]`，可有 `Matches[]` | 可独立设计普通知识库检索，但不标为 DataWiki、不复用 query-only 请求模板 |
| `ListKnowledgeBases` | POST **formData**；必填 `Tag`（DataAgent 中为工作空间 ID），根级 `NextToken`；`TotalCount` 文档注明恒为 0、暂不支持 | 不能用 CatalogUuid 代替 Tag，不能把 TotalCount=0 当空库 |
| 文档/知识块 | `ListDocuments`、`DescribeDocument`、`ListDocumentChunks` 契约存在，均需 KbUuid；`DescribeKnowledgeBaseStats` 反而标记 readAndWrite | 属通用知识库范围，本次不扩张成文档管理项目 |
| `gateway_describe` / `gateway_call` | 动态长尾管控入口，未公布完整静态 Action 映射 | 不提供同类通用转发器，避免绕过白名单 |
| 审计与管理 | 控制台提供“我操作的”/管理员“全部”、数据源/用户/令牌管理 [S10][S11][S12] | UI 可见性不是 RAM/STS 下 OpenAPI 授权已验证的证据 |

### 2.5 已公布的 RAM 定义与空白

以 [S1] 的 `ramActions` 为准，而非仅看 `security: AK`：

- 本次候选中 `GetAgenticCatalog`、`SearchAgenticDatabase`、`ListAgenticDataSources`、`GetAgenticDataSource` 已公布对应 `dms:<Action>`，`authLevel=operate`、Resource 为 `*`、无已列 Condition。`*` 是官方操作级定义，**不代表所有库表的数据权限**，也不应编造资源 ARN 缩窄它。
- `GetAgenticDatabase`、两个 SQL 历史 Action、`CreateAgenticSqlExecuteCredential` 的 `ramActions` 列表为空。记录为“最小 RAM 授权待确认”，不是免鉴权；不得通过授予 DMS 全权限掩盖缺口。
- 知识库的 `ListKnowledgeBases`、`RetrieveKnowledgeBase` 有对应 `dms:<Action>`、Resource `*` 定义，但知识库的工作空间/文档授权仍须独立验证。
- 本 Spec 不执行授权变更；已实现 6 个 Action 的角色测试结论不外推到新增 Action。

## 3. 目标、阶段与非目标

### 3.1 首期：元数据闭环（已实现基线）

交付 6 个工具，完成 Catalog 搜索/列表 → Database 列表 → Table 列表 → 字段/索引查询。启用后总计 25 个工具；关闭时仍为原 19 个。

同时移除 Server 全局 API Key 模式，保留 AK/STS 下的既有工具行为；复用现有 RAM/STS 身份、限制调用目标、保持分页与数值精度、错误可关联、无秘密泄漏。此阶段不依赖读取官方网关 MCP 的 `tools/list` 或协议协商。

### 3.2 后续：按独立能力包补齐（本次提案）

1. **P2a，已实现：** 新增 Catalog 详情、Database 详情、Database 搜索 3 个工具，减少逐实例枚举；网关开启时共 28 个工具，生产发布仍受新增契约及角色验收门槛约束。
2. **P2b，已实现且默认关闭：** 数据源列表/详情 2 个工具，单独显式启用；成功 null 分支仍待补证，部署前须证实账号管控面范围符合授权预期。
3. **P2c，按需实施：** 上游调用主体可见的 SQL 历史摘要 1 个工具，单独显式启用，禁止全局历史和 SQL 正文；不声称是聊天用户个人历史。
4. **P3，执行链专项：** 先确认 Agentic 凭证到 OneQuery 的完整只读执行协议，再冻结 SQL 工具。不能直接把已发布数据服务 API 当作任意 SQL 接口。
5. **独立调研门槛：** DataWiki、ACL 自查不依赖 P2a/P2b/P2c 完成，但必须取得自身等价 OpenAPI 与主体映射证据后才能实施。

当前代码注册数为 19/28/30（关闭/基础网关/额外开启 P2b）；P2c 数量仍为未来计划值。注册或单角色调用成功不等于通过生产发布门槛。可用能力不因另一个阶段受阻而隐式回退；不注册占位工具、不承诺完整兼容官方网关 MCP。

### 3.3 非目标与实施边界

- 本次只写 Spec；下述 P2/P3 代码、SDK 调用、配置开关及真实云端新增调用均需后续实施授权。
- 不改原分析会话、工作区、数据导入、文件上传、报告、自动确认或 SSE 的业务语义；已完成的 API Key 认证移除仍须回归。
- 不增加 Gateway-only 模式、API Key 兼容模式、跨身份域体系；独立 `alibabacloud-data-agent-skill/` Python CLI/SDK 不变。
- 不维护网关 Access Token 创建/兑换/绑定/吊销，不增加 Token 配置项。
- P2 不含 SQL 执行；未来 SQL 首版也不含 DML/DDL、批量脚本、事务、人工确认旁路或未经核实的异步任务管理。
- 不开放数据源增改删/连通性探测、授权申请/审批、保护规则配置、熔断、全局审计等管理操作。
- 不提供通用 OpenAPI Action 调用器、CLI 执行、数据库直连、旧 `ExecuteScript` 或官方 MCP 的自动回退。
- 不向 LLM 暴露数据库凭证、AK/SK/STS、OneQuery 执行凭证。

## 4. 架构与复用

```text
MCP Host / Agent
    │ stdio / SSE / Streamable HTTP（现有入站协议）
    ▼
Data Agent MCP Server
    ├── data_agent_* → 原 Resolver / Client / Session Manager
    │                     └── Data Agent + DMS API（不变）
    └── data_gateway_* → 网关严格身份检查 → 现有 Resolver
                              │ 用户 → 分组 → RAM Role → STS
                              ▼
                      同一身份的 dataagent.Client
                              │ 新增 Agentic OpenAPI 方法
                              │ HTTPS + 阿里云签名 + STS SecurityToken
                              ▼
                         Dms 2025-04-14 OpenAPI
                              │ RAM 操作权限 + 服务端资源/数据授权
                              ▼
                         当前身份获准访问的元数据
```

stdio 单身份模式使用现有全局凭证链，不执行用户分组；远程多用户模式禁止退回全局服务端身份。

### 4.1 最小改造原则

- `internal/dataagent/gateway.go` 已有 9 个有类型的 Agentic 方法（首期 6 个及 P2a 3 个）；后续在此基础上增加固定方法，复用 Client、凭证来源、签名和 API 错误模型，不建设第二个 Token Registry 或 STS Registry。
- `internal/mcp/gateway.go` 已有静态工具 Schema、参数校验和 handler；后续继续通过现有 Resolver 得到当前用户的 Client。
- 继续使用 `Config.ResolveGroup()`、`Registry.Resolve()` 与可轮换的 credentials-go 角色 provider。Gateway 不需要“仅认证、不解析 STS”的独立体系。
- 复用 Resolver 会沿用 Tenant 中 Manager 初始化/恢复的现有行为。本期接受这一实现边界，不为元数据查询顺带重构会话生命周期。
- 需要的网关配置必须同时传入主 Client 和 Tenant Client 创建路径，不能只在单用户模式生效。
- 原有 `mcp-go` 仍仅承担本地 MCP 服务；本次没有南向 MCP 协议升级要求，不因改走 OpenAPI 升级该依赖。

### 4.2 OpenAPI 请求规则

1. 产品/版本固定为 `Dms/2025-04-14`，当前 Action 固定为第 6 节白名单；P2 获准实施后仅加入第 7 节对应能力包的固定 Action。现有与 P2 Agentic 方法统一 POST、query 编码，不外推到 formData 知识库或 OneQuery 执行协议。
2. Endpoint 使用 OpenAPI 地域域名，不使用 `dgate-mcp-*` 或 Data Agent `/apikey` 地址。Endpoint 存在不证明某地域已开放全部 Agentic 能力。
3. 复用已有签名基础函数，不重新手写签名算法或读取 AK/SK 环境变量；凭证依赖已有阿里云 SDK 凭证链/provider。
4. `callAPI()`、`callDMSEnterprise()` 及 SSE 的 API Key 分流已移除，整个 Server 只走 AK/STS 签名。新增网关方法仍须满足独立的响应大小、精度和严格取证要求。
5. 不自动附加旧接口的 `DmsUnit`、`Tid`、`WorkspaceId`、`CustomAgentId` 等参数；只有对应 Agentic 契约要求时才传递。
6. 参数只用于固定 API 请求的编码，不拼接到 host/path；不让调用方指定角色、凭证、region、endpoint、Action 或 API version。
7. 不缓存跨用户的元数据、游标或权限结论，不自动下载返回的资源 URL；元数据注释是数据，不是可执行指令。

## 5. 用户身份、凭证与授权

### 5.1 映射流程

```text
入站平台认证 + 用户 JWT / 可信身份头
    → 已验证 user_id / email
    → 现有 identity.groups 匹配，或显式 identity.default
    → RAM Role
    → 每用户 STS provider + RoleSessionName
    → 当前用户 Client 使用 STS 调用 Agentic OpenAPI
```

**无第二份用户配置：** 网关不新增 `identities`、`access_token_env` 或网关用户-ID绑定表。但部署者仍须为 RAM 角色配置实际需要的 API 权限与服务端数据权限；其具体映射方式是 P0 联调项。

### 5.2 角色映射语义

- 原命名组按已验证的 user_id/email 匹配，优先于显式 `identity.default`。
- `identity.default` 表示“已认证但未匹配命名组的用户共享这个角色权限”，不是匿名访问，也不是逐用户权限隔离。
- 没有身份、没有匹配组且未配置 default、AssumeRole 失败，都拒绝网关调用；不得回退到服务端 AK。
- **同一个角色、不同 RoleSessionName，原则上共享该角色的授权范围。** 会话名用于审计区分，不会自动继承 Aily/企业应用用户本人已有的数据权限。
- 确需不同数据权限的用户，必须映射到具有不同有效授权范围的角色，或采用经验证的服务端授权机制。不要把改变会话名当作数据隔离措施。
- 本期不新增会话策略生成器；如果未来使用 STS Session Policy，权限是与角色策略取交集，也仍需验证网关数据层实际执行。
- 继续沿用仓库单一可信身份命名空间；不声称可以安全合并不同组织的相同 user_id。

### 5.3 两层授权必须都成立

| 层次 | 验证内容 | 不能推导出的结论 |
|---|---|---|
| RAM API 权限 | 角色能否调用目标 Agentic Action | 拥有此 Action 不代表拥有所有库表查询权 |
| 服务端资源/数据权限 | 此角色对应的调用主体能看到哪些实例/库/表、能执行哪些操作 | 元数据可见或平台 ADMIN 不代表可执行 SQL |

每个首期 Action 的 RAM Action/Resource/Condition 以官方授权定义和实际测试为准。未公布时标记待确认，不通过添加 `AliyunDMSFullAccess` 或猜测资源 ARN 来掩盖缺口。

### 5.4 远程与本地模式

- **stdio：** 复用全局 SDK 凭证链，代表单一进程身份；不代表聊天中每位用户各有权限。需要动态凭证时必须保留可刷新的 provider，而非声称启动时快照可以长期自动续期。
- **HTTP/SSE：** 启用网关时必须启用现有 identity，并且每次网关调用都要求非空已验证身份，即使旧配置的 `require_identity=false`。
- **JWT：** 网关在 JWT 模式要求 Token 存在、有效且有可用于用户映射的身份，不能在缺失 JWT 时降级到身份头。旧 Data Agent 的入站身份行为不因此改变。
- **可信 Header：** 仅允许受信平台访问，平台覆盖客户端自报身份头，共享认证 Token 不交给普通调用者。
- **API Key：** 整个 MCP Server 不再支持，不区分网关开启或关闭。启动只从阿里云 SDK 凭证链获取 AK/STS；发现遗留 API Key 配置时按第 5.6 节明确报迁移错误，不能静默改用另一组凭证。

### 5.5 凭证生命周期

- 复用现有 credentials-go provider 和每用户 STS 隔离，不保存额外的网关长期凭证。
- 当前 `Tenant.currentCredential()` 刷新失败会返回旧快照；网关严格路径必须取得可报告错误的凭证结果，不把刷新失败静默变成旧凭证/全局凭证回退。旧分析路径的兼容行为不在本期修改。
- 对可到期的基础凭证和 AssumeRole 凭证，都要验证实际 provider 的刷新行为。不得把原 `LoadCredential()` 启动快照描述成所有链路自动刷新。
- provider 无法提供有效凭证时中止调用；禁止由工具传入 AK/SK、SecurityToken、RoleArn 或任意 RoleSessionName。
- 不承诺角色策略变更或撤权即时传播；权限缓存、STS 有效期与服务端生效时间必须以验证结果说明。

### 5.6 移除 API Key 模式与部署迁移

**移除范围覆盖整个 Go MCP Server，不仅是新网关工具：**

- 删除 `Credential.APIKey`、`IsAPIKey()`、main 认证选择分支，以及 Client/Tenant/SSE 中所有 API Key 专属配置和传递路径。
- 删除 YAML 的 `api_key`、`api_key_endpoint`、`api_key_stream_endpoint`，以及 `DATA_AGENT_API_KEY`、`DATA_AGENT_API_KEY_ENDPOINT`、`DATA_AGENT_API_KEY_STREAM_ENDPOINT` 的运行支持。
- 删除 API Key endpoint 推导、`/apikey` 请求、`x-api-key` 认证头、专属 Action 别名/数据面路由和仅用于 API Key 的封装转换；普通 API 与 SSE 都保留正式签名协议。
- 删除仅因 API Key 模式存在的功能限制和条件分支，例如跳过 DMS 元数据调用、DMSUnit/Tid 解析或采用专属会话时间参数；保留 AK/STS 原有行为。
- 共享 RequestId 提取、错误解析、分页和内容完整性处理不能按名字误删；历史 API Key 命名下曾包含签名响应的共享逻辑，移除后仍须保留这些能力。
- 删除 API Key 专属测试用例；将其中验证通用请求关联、错误、分页、精度和多内容块的测试迁移为签名凭证 fixture，不能一并丢失覆盖。

上述移除已完成，后续阶段保留此节作为回归及迁移约束。

**迁移是明确的配置变更，不提供兼容运行分支：**

1. 部署者移除上述旧 YAML 键和环境变量，使用现有 SDK 凭证链或角色身份配置；本次 Spec 不代替部署者删除秘密或修改部署。
2. 为防静默切换身份，配置边界检测上述已删除 YAML 键以及非空旧环境变量，发现即报不含秘密值的迁移错误，即使网关关闭或同时存在有效 AK。仅保留拒绝旧输入的诊断，不保留 API Key 配置模型、别名或调用能力。
3. 旧配置清理后，SDK 凭证链不可用则启动失败；只有 API Key 的旧部署不能继续运行。原纯 AK/STS 部署无须更改工具调用方式。
4. 当前使用说明、部署示例和 MCP Skill 运行时规范全部改为 AK/STS；可以保留有明确版本上下文的历史 changelog，不让其看起来仍是当前支持方式。

**不属于此次移除：** 入站 `identity.auth_token` / JWT、dacli 的 `--token`、阿里云 STS `SecurityToken`、OSS 上传签名字段，以及未来仅在服务器内部使用的 OneQuery 执行凭证。通用秘密脱敏规则也保留，不能因移除认证模式而放松。

## 6. 首期 MCP 工具契约

### 6.1 静态白名单

参数采用本地 snake_case。`page_size?` 默认 50、本地最大 100，是项目建议值，不是官方默认/上限；P0 若确认上游范围更小，按更小范围冻结。

| 本地工具 | 本地参数 | OpenAPI Action |
|---|---|---|
| `data_gateway_list_catalogs` | `page_size?`, `next_token?` | `ListAgenticCatalog` |
| `data_gateway_search_catalogs` | `search_key`, `page_size?`, `next_token?` | `SearchAgenticCatalog` |
| `data_gateway_list_databases` | `catalog_uuid`, `page_size?`, `next_token?` | `ListAgenticDatabase` |
| `data_gateway_list_tables` | `database_uuid`, `page_size?`, `next_token?` | `ListAgenticTableBaseInfo` |
| `data_gateway_get_table_columns` | `database_uuid`, `table_qualified_name` | `GetAgenticTableColumn` |
| `data_gateway_get_table_indexes` | `database_uuid`, `table_qualified_name` | `GetAgenticTableIndex` |

未知参数在 Schema 和处理器两层拒绝，尤其是身份/凭证、endpoint、action、confirm。官方 API 支持但本表未开放的过滤/排序参数不自动透传；新增参数需更新本地契约。

### 6.2 参数、分页与标识

| 本地参数 | OpenAPI 参数 | 约束 |
|---|---|---|
| `search_key` | `SearchKey` | 本地搜索工具要求非空；上游按 Catalog 名称、别名或连接地址前缀匹配 |
| `catalog_uuid` | `CatalogUuid` | 数据库列表必填 |
| `database_uuid` | `DatabaseUuid` | 表列表、字段、索引必填 |
| `table_qualified_name` | `TableQualifiedName` | 使用表列表的 `QualifiedName` 原值，不自行拼库表名 |
| `page_size` | `MaxResults` | 仅前 4 个列表/搜索工具支持 |
| `next_token` | `NextToken` | 首次省略，后续原样传递上一页返回值 |

- UUID/QualifiedName 不转换为旧 DMS 数字 ID，也不直接用于 `data_agent_create_session` 的 `database_id`。
- 前 4 个 Action 返回根级 `Data`、`NextToken`、`MaxResults` 等；NextToken 空表示结束。不存在南向 MCP 的 `pagination.hasMore` 契约。
- 一次调用只请求一页，不自动遍历所有数据。NextToken 必须用于相同身份、目标及过滤条件，拒绝后不得改用另一个身份继续。
- 字段与索引 Action 返回 `Data` 数组，无分页参数；不要假造下一页或总数。

### 6.3 输出封装

网关工具返回一个 MCP TextContent，文本是完整 JSON；旧工具格式不变。

```json
{
  "status": "OK",
  "data": [],
  "pagination": {"has_more": false},
  "request_id": "<本地请求标识>",
  "upstream_request_id": "<上游真实 RequestId>"
}
```

- 本期状态仅 `OK`/`ERROR`。同时校验 HTTP 状态与业务 `Success`/错误字段，HTTP 200 不等于成功；结构缺失或矛盾不能当作空结果。
- `data` 是已验证模型中的业务字段白名单，保留官方字段名、数组顺序、NULL、整数与精确小数，不经 `float64` 中转丢失精度。
- 至少保留后续发现所需的 CatalogUuid、DatabaseUuid、QualifiedName；具体实体字段和可选性在 P0 对照引用 Schema 及真实样例冻结。
- Catalog 的 `Properties`、`CatalogBizAttrs`，以及其他开放扩展属性不能直接全量转发；只开放已核实的必要非秘密字段。未知字段默认不暴露，避免连接串或凭证扩展字段进入模型上下文。
- `pagination` 仅用于前 4 个工具；`has_more` 由上游根级 NextToken 是否非空推导，非空时带 `next_token`。不伪造总行数。
- `upstream_request_id` 仅在确实获得 RequestId 时出现；不将本地 ID、RoleSessionName 或缺省字符串冒充上游标识。
- 失败时 `isError=true`、`status=ERROR`，增加 `error: {code, message, retryable}`；消息经过脱敏，不回显原始签名 URL、HTTP body 或秘密。
- Gateway wrapper 将请求 ID 写入 JSON，不执行原 `tagError()` 的尾部文本追加，也不额外附加旧工具的请求元数据内容块。

## 7. 后续集成契约与门槛

**P2a 与默认关闭的 P2b 已实现；P2c/P3 与 DataWiki/ACL 仍为后续提案，未注册工具。** P2 只接固定只读 OpenAPI；P3 和 DataWiki/ACL 未通过契约门槛时不注册工具，不增加无实现的运行开关。各工具保留第 5、6、9 节的严格身份、参数白名单、完整 JSON、精度及日志规则。

### 7.1 P2a：补齐元数据定位（已实现，发布门槛另列）

| 已实现 MCP 工具 | 本地参数 | 固定 OpenAPI 映射 | `data` / 分页 |
|---|---|---|---|
| `data_gateway_get_catalog` | `catalog_uuid` | `GetAgenticCatalog(CatalogUuid)` | Catalog 对象，无分页 |
| `data_gateway_get_database` | `database_uuid` | `GetAgenticDatabase(DatabaseUuid)` | Database 对象，无分页 |
| `data_gateway_search_databases` | `search_key`, `catalog_uuid?`, `db_type?`, `env_type?`, `page_size?`, `next_token?` | `SearchAgenticDatabase(SearchKey, CatalogUuid?, DbType?, EnvType?, MaxResults, NextToken?)` | Database 数组，根级 NextToken |

**参数约束：**

- 必填字符串非空；UUID、QualifiedName、游标保持原值，不根据旧 Data Agent 数据库 ID 构造。
- `SearchKey` 上游可选，本地搜索工具明确要求非空，避免一次搜索退化为无条件全范围枚举。
- `CatalogUuid` 省略时在服务端允许的跨 Catalog 范围搜索；指定后固定搜索范围，但它不是授权条件的替代品。
- `DbType` 按官方大写数据库类型传递；本地格式为 `^[A-Z][A-Z0-9_]*$`，不是官方封闭类型枚举，不自动转换或推导连接地址；`EnvType` 仅 `product/pre/dev/test/sit/uat/pet/stag`。
- 分页沿用本地默认 50、最大 100；筛选条件及身份必须与游标产生时相同。不自动遍历所有页，也不在搜索失败时循环改查其他身份/地域。
- Schema 和 handler 都拒绝未知字段；传入 `role_arn`、`action`、`endpoint`、`confirm` 均不发请求。

**输出白名单与形态：**

- Catalog 详情复用现有字段集合：`CatalogUuid/CatalogType/DataSourceUuid/DbType/Description/Name/RegionId/State`。不增加 `Properties`、`CatalogBizAttrs`、`StorageLocation`。
- Database 详情和搜索复用现有 Database 字段集合：`CatalogUuid/CatalogName/CatalogType/DatabaseUuid/DbType/Description/Name/QualifiedName/RegionId/SearchName/State`；`EngineMeta` 仅 `CatalogName/Encoding/SchemaName/StorageCapacity`。已通过官方 Python SDK 4.2.2 与 Go SDK v4.2.0 [S15][S16] 补齐模型：State 为 int32、StorageCapacity 为 int64（MB），其余上述标量为 string；EngineMeta 是对象。真实样例仍按字段白名单及类型校验，不能因列表使用过就跳过详情验证。
- 对象结果保持对象，不能包装成单元素数组；要求对应非空 `CatalogUuid` 或 `DatabaseUuid`。返回对象内 UUID 必须匹配请求目标；搜索显式限定 Catalog 时，若返回项明确携带不同 CatalogUuid，应报不兼容，不静默过滤。
- [S1] 未明确两个详情接口的不存在语义；在取得证据前，HTTP 200 + `Data:null`、缺少 Data 或错误类型均按不兼容处理，而不是成功空对象。以后若确认合法空值，只按对应 Action 修改契约。
- 已实现的 6 个工具不改变 `data` 类型或分页格式。详情无 `pagination`；搜索与第 6 节游标 JSON 一致。

### 7.2 P2b：数据源管控摘要（可选，默认关闭）

| MCP 工具 | 本地参数 | OpenAPI 参数 | 输出 |
|---|---|---|---|
| `data_gateway_list_data_sources` | `db_type?`, `page_size?`, `next_token?` | `DbType?`, `MaxResults`, `NextToken?` | 脱敏字段白名单数组，游标分页 |
| `data_gateway_get_data_source` | `data_source_uuid` | `DataSourceUuid` | 脱敏字段白名单对象；成功 null 分支待真实契约补证 |

- 对应 Action 为 `ListAgenticDataSources` / `GetAgenticDataSource`。官方列表默认 20、最大 200；本地仍默认 50、最大 100，以保持较小输出预算。
- 官方 `SearchKey` 同时匹配名称与连接地址，即使删除输出 Host，仍能通过命中结果推断地址。因此首版不开放 `search_key`，也不开放 `State/Host/Port/InstanceSource/Schema` 等其他过滤，不自动透传；无筛选表示当前上游调用主体可访问的管控范围。
- 列表/详情只返回字符串 `DataSourceUuid/Alias/DbType/EnvType/InstanceSource/RegionId/CatalogUuid/CatalogName` 与有符号 int32 `State` [S1][S16]（0 正常、-1 已删除、-2 已禁用）；可选字段存在时保留 NULL。每个实体要求非空 `DataSourceUuid`，详情须与请求 UUID 原值一致；标识不要求 RFC UUID 格式。
- 明确删除 `AuthAccount/AuthPassword/AuthType/KmsSecret/RoleArn/ResourceAccountId/Host/Port/Schema/PolicyId/VpcId/VSwitchId/ZoneId/SnowflakeRole/SnowflakeWarehouse` 及其他未列字段；即使上游称密码“已脱敏”，也不透传认证字段。不用“先全量返回再正则脱敏”代替白名单。
- Catalog 关联字段若缺失，不猜测 UUID 或自动创建 Catalog。`TotalCount` 可能为空且可能暴露账号规模，首版不返回，分页只依赖 NextToken。
- 官方说明“不存在时 Data 为空”，但 schema 未显式标记 nullable [S1][S18][S19]。2026-10-29 对既有 `da-role` 的真实探测（保留已有标识前缀和长度、更换随机尾部）返回 HTTP 404、`Code=DATA_SOURCE_NOT_FOUND` 且缺少 Data，RequestId `01A18566-AF8E-5639-94B0-EBFBEAD9CA19`；没有取得 `Success=true,Data:null` 样例。因此本轮只接受详情对象，null、缺少 Data、`{}` 或错误类型均不兼容；404 保持 ERROR，当前映射 `GATEWAY_UPSTREAM_INCOMPATIBLE`，不重试、不改成成功空对象。未来只有取得真实成功 null 样例后，才按该 Action 单独开放空值成功分支；不得放宽 P2a 详情及其他数组工具。

**发布门槛：**接口描述是“当前账号下的数据源”，不是“当前聊天用户获准查询的数据库”。必须验证 RAM/STS 对应管控主体和可见范围；若角色能看到整个账号而部署者不能允许调用者获知这些摘要，则该部署不开启 P2b。不要先广泛查询、再靠客户端筛选冒充数据隔离；不要自动调用连接测试。开关只控制工具暴露，不授予权限。

### 7.3 P2c：当前上游主体的 SQL 历史摘要（可选）

拟新增 `data_gateway_list_sql_histories` → `ListAgenticSqlExecutionHistories`；名称不使用 `my`，避免承诺企业应用用户级隔离。

| 本地参数 | OpenAPI 参数 | 首版本地限制 |
|---|---|---|
| `start_time` / `end_time` | `StartTime` / `EndTime` | 均必填，`yyyy-MM-dd HH:mm:ss`；结束不得早于开始，一次跨度不超过 7 天 |
| `catalog_uuid?` / `database_uuid?` | `CatalogUuid` / `DatabaseUuid` | 可选非空精确匹配；不是鉴权边界 |
| `page?` | `Page` | 默认 1，从 1 开始的 int32 |
| `page_size?` | `Size` | 默认 20，1–100 |

- 官方缺省查询最近 7 天，StartTime 无法解析或早于 730 天会按 730 天前处理；本地严格校验日期，开始时间不得早于请求时刻 730 天前，结束不得晚于请求时刻，跨度不超过 7 天，不继承上游宽松回退。首次请求固定时间窗后，翻页不得变化。
- 时间格式没有时区偏移；**对应接口时区仍待确认**。不能因其他 Token 接口注明东八区就外推；发布前文档必须写明该接口的真实时区，并按它校验时间窗，不能直接按服务器本地时区转换。
- 上游只包含 `AGENT_QUERY` / `WORKFLOW_QUERY`，按开始时间倒序；不是所有 DMS 日志，也不是可用于读取异步结果或取消 SQL 的协议。
- 不传 Actor 时，官方定义为“用户本人及名下 Agent；Agent 仅自己”。这是上游主体，RAM Role 到该主体如何映射须实测；同一角色的多个 RoleSessionName **可能看到相同集合**。
- 首版不接受 `ActorId/ActorName/ActorType/SqlKeyword/SqlType/Scenario`，不调用全局历史接口；不把入站 user_id 转成 `usr_` 或 `agt_`。

**输出：** `data` 为精简历史数组，仅保留 `QueryId/CatalogUuid/DatabaseUuid/DatabaseSearchName/ExecutionStatus/SqlType/Scenario/StartTime` 以及 int64 `ReturnedRows/AffectedRows`。禁止 `OriginalSql/ExecutedSql/Error/ClientIp/ActorId/ActorName/AgentOwnerId/WorkflowId` 等正文或主体扩展。`Cost` 定义未给单位，本期不返回或换算它；控制台的 ms 展示不能替代 API 字段单位证据。

- `ExecutionStatus` 官方值为 `SUCCESS/FAILED/BLOCKED`；保留其业务字符串，不转换成外层 OK/ERROR。外层 OK 表示历史查询成功，不表示每条 SQL 成功。
- 根级 `TotalCount` 按非负 int64 校验，精度完整保留；分页使用 `{"page":1,"page_size":20,"total_count":42,"has_more":true}`，不伪造 NextToken。`has_more` 按 page、实际发送 Size 和真实 TotalCount 计算，使用不会溢出的运算；缺失/非法 TotalCount 视为不兼容，不从本页行数猜总数。
- 返回超范围页允许 `data=[]`，不能把整个工具调用当成执行失败。分页是实时历史视图，不承诺跨页事务快照；新记录可能导致跨页重复/移动，不在本地缓存全量历史。
- 结果、错误原文和筛选值不进入日志，即使 `log.requests=full`；若需查询 SQL 正文或管理员全局审计，应另开管理面方案，不用参数开关逐步放宽这个工具。

**发布门槛：**确认最小 RAM 权限、时区、TotalCount 及空页协议；验证两个不同授权主体的正反向可见范围、同角色不同会话的共享范围。若不能证明拟部署场景的历史隔离符合预期，不启用 P2c。

### 7.4 P3：网关只读 SQL 执行链（有明确阻断项）

#### 已证实的两段协议，不自动拼接

1. **Agentic 凭证签发 [S3]：** `CreateAgenticSqlExecuteCredential` 可用 `DatabaseUuid` 定位数据库，或用 `CatalogUuid/CatalogName` 与 `QualifiedName`。前三者至少一个；只指定实例可产生实例级凭证。返回 `AccessKey/AccessSecret/CatalogName/CatalogUuid/DatabaseUuid/QualifiedName/ExpireTime/MaxConnectTimes`；有效 10 分钟，总连接次数最多 500，非 500 并发或 500 行。
2. **OneQuery 数据服务 [S9]：** 官方公开 HTTPS POST `/service/http` 与 SSE POST `/service/sse`，示例主机为 `onequery-cn-hangzhou.proxy.dms.aliyuncs.com`；JSON 含 `accessKey/accessSecret/query` 和可选 `params`。但这里 `query` 固定为 **`call api ${apiPath}`**，凭证是 DMS 内部凭证而非 RAM AK，调用对象是已发布的数据服务 API。
3. 数据服务 HTTP 示例含 `type/traceId/count/rows/columnMetas/attributes/message`，类型为 `RESULTSET/ERROR`；SSE 示例为 `META/ROWS/EOF`，带 `resultIndex/sessionId/traceId`。文档未明确 Agentic 临时凭证可用于此路径，也未给出任意 SQL 的目标绑定、取消或异步结果取回契约。

因此：**发现 OneQuery Endpoint 不等于已找到网关任意 SQL 的完整协议。** 不把 SQL 塞进 `call api` 字段试探，不移植旧安全访问代理的 MySQL/JDBC 认证，不把客户端示例 30 秒/5 分钟 timeout 当作服务保证。

#### 目标路径（待官方衔接契约确认）

```text
已验证入站身份 → 既有分组/RAM Role/STS
    → CreateAgenticSqlExecuteCredential(DatabaseUuid)
    → 仅本次请求内持有数据库级 OneQuery 凭证
    → 官方确认的网关执行协议（尚未冻结）
    → 有界、精确保留类型的查询结果
```

**后续拟议 MCP 入口：** `data_gateway_query(database_uuid, sql, max_rows?, timeout_seconds?)`。这是候选本地契约，不是已可调用工具；具体限额、返回列类型、截断字段及执行状态在协议确认后冻结。首版只接受明确数据库 UUID，避免实例级临时凭证扩大作用域。

#### SQL 上线前必须全部成立

1. 官方提供 Agentic 临时凭证适用的实际 Endpoint/协议、目标绑定、认证与错误样例，确认允许直接 SQL 而非仅发布 API；端点由部署配置限制，LLM 不能传 host/协议/凭证。
2. 证明 RAM/STS 调用主体传播到实际执行引擎，数据库 ACL、字段脱敏、行过滤及网关审计均在该路径生效。元数据可见和凭证签发成功都不构成证明。
3. 每个支持引擎由服务端权限强制只读，测试 DML/DDL、多语句、存储过程/函数、导出及其他副作用均被拒绝；本地语法解析只能补充，不能靠 SELECT 前缀或自动追加 LIMIT 保证安全。[S4] 行保护仅过滤查询，**不能阻断写入**。
4. 凭证仅服务端内存、本次调用和指定目标使用，首版不做连接池或跨请求缓存；验证返回 DatabaseUuid/实例信息与目标一致。不得落盘、入日志、进入 MCP 结果，也不得复用到另一个角色或库。冻结 ExpireTime 时区和有效期余量，连接失败不能自动签发无限新凭证。
5. HTTP 超时/断连不等于数据库取消。提交后结果未知返回本地 `GATEWAY_EXECUTION_UNKNOWN`、`retryable=false`；若没有正式取消/状态查询协议，不提供假取消/假轮询。不能根据历史列表猜测某次 SQL 已停止。
6. 只有完整确认成功且结果集协议完整时返回成功；精确保留列元数据、NULL、大整数、小数，限制行数及解压后字节数，截断必须明确。HTTP 200、DataAgent 会话 completed、任意非空结论均不足以判定成功。
7. 只读首版不接受 `confirm` 或复用旧 `auto_confirm`；遇到必须人工确认的执行请求即停止。真正的确认流程需要可信宿主交互、绑定身份/SQL/目标/有效期，不能让 LLM 自行填写理由绕过。
8. 凭证签发属于 write；SQL 查询可能产生执行与审计副作用。**不复用元数据的自动重试策略**，不把整个工具标成可安全重放。官方 MCP 的 `RUNNING/NEEDS_CONFIRMATION/TIMED_OUT_WAITING` 不作为本地 OpenAPI 协议。

P3 未过门槛时保持不可用；不回退原 `data_agent_*`、旧 `ExecuteScript`、直连数据库或官方网关 MCP。原分析链的成功查询仅作为回归基线。

### 7.5 DataWiki 与 ACL：独立等待正式契约

**DataWiki：**先确认 `wikiUuid` 与 `KbUuid` 的关系、知识来源、版本/更新机制、角色可见范围以及是否包含受保护表/样例数据。正式证明等价后，才设计 `data_gateway_wiki_search`；查不到资料时标为待确认，不以泛知识库结果冒充业务语义。

- 如未来独立接入 `RetrieveKnowledgeBase`，最低输入为 `KbUuid/Query`，本地可设 TopK 上限；`Filter` 是 SQL WHERE 格式，不开放给 LLM，也不开放 `OrderBy/HybridSearchArgs/IncludeMetadataFields` 等任意透传。
- 该 API 业务参数位于 **formData**，必须按官方协议编码和签名 body，不能沿用本期 Agentic 的全 query 请求；`IncludeVector=false`，白名单仅可包括 `Id/Content/FileName/Score/RerankScore/RetrievalSource`，`LoaderMetadata/Metadata/Vector` 默认丢弃。
- `Data.Results[]/Matches[]` 不是元数据 `Data[]`；关闭 RecallWindow 时仍须验证实际响应形态，不能丢弃上下文后声称完整。引用文本、样例 SQL 均为不可信数据，不执行其中指令、不自动下载链接。
- 知识库列表需要 Tag/工作空间范围，不能从 Catalog、数据库或角色推算；通用知识库列表、文档/知识块管理不纳入本次 P2 工具数量。

**ACL：**只有找到返回当前上游身份和有效数据授权的正式 OpenAPI 后，才设计自查工具。要求区分 RAM 主体、网关 User/Agent、平台 ADMIN、资源与操作/有效期；空授权必须是可信上游的真实结果。缺少接口或解析失败不能返回“权限为空/全部有权”，不能本地伪造；不得调用 `CreateAgent` 或安装令牌流程来获得看似类似的身份对象。

## 8. 配置设计

### 8.1 现有配置（已实现）

以下为非秘密示例，不修改真实配置；Endpoint 须与现有 `region` 一致。

```yaml
agent_gateway:
  enabled: false
  data_sources_enabled: false
  endpoint: dms.cn-shanghai.aliyuncs.com
  request_timeout_seconds: 30
  max_response_bytes: 4194304
```

- `endpoint` 是 OpenAPI host，示例来自官方上海端点 [S1]；启用时显式配置并验证目标地域。使用 HTTPS，不接受 scheme、path、query、fragment、userinfo 或调用方动态目标。
- 不增加网关 region、用户映射或秘密变量；已有 `region` 与所配置 Endpoint 必须一致，不能从官方网关 MCP 支持地域列表推断 OpenAPI 可用性。
- 30 秒是网关出站 HTTP 请求预算，4 MiB 是解压后响应上限，均为本地建议值，不是阿里云服务限制。
- HTTP 预算不自动涵盖现有同步 Resolver/SDK 取证耗时。实施须确认 STS/provider 自身有界超时，分别测试身份解析和 HTTP 阶段，不声称只设置 HTTP Timeout 就保证端到端 30 秒。
- 沿用 `LoadDotEnv()`、`Load()` 的配置加载方式；网关没有 Token 引用解析，也不依赖 `${VAR}` 模板展开。
- Server 始终检查已移除的 API Key 配置，并只初始化 AK/STS 凭证链，与 `agent_gateway.enabled` 无关。
- `enabled=false` 时不注册网关工具、不增加网关网络请求；开启时另行检查 Endpoint、限额和远程 identity 条件。
- 不提供 `identities`、`access_token_env`、南向 `transport`、动态 Action 白名单或首期 `sql.enabled` 配置。

现有身份配置继续使用，例如以下只是非秘密角色映射片段，不是完整部署配置：

```yaml
identity:
  enabled: true
  require_identity: true
  groups:
    finance:
      role_arn: "acs:ram::<account-id>:role/<finance-read-role>"
      users: ["<finance-user-id>"]
    operations:
      role_arn: "acs:ram::<account-id>:role/<operations-read-role>"
      users: ["<operations-user-id>"]
```

共享平台认证和 JWT 秘密仍按现有配置注入，不在示例里保存。组名和角色名本身不决定权限；部署者负责给角色配置真实授权。如果不需要用户级隔离，可以显式配置共享 `identity.default`，但必须接受该角色范围内的共享权限。

### 8.2 可选能力暴露

P2a 沿用 `agent_gateway.enabled`。P2b 已新增 `data_sources_enabled`，因涉及管控信息而默认关闭；P2c 的 `sql_history_enabled` 仍仅为提案，当前配置不支持，不提前加入空实现。

```yaml
agent_gateway:
  enabled: true
  endpoint: dms.cn-shanghai.aliyuncs.com
  request_timeout_seconds: 30
  max_response_bytes: 4194304
  data_sources_enabled: false
```

- `data_sources_enabled` 默认 false；子能力启用而总开关关闭时配置报错，避免误以为已启用。
- 总开关关闭为 19 个工具，仅开启基础网关为 28，再启用 P2b 为 30。未来 P2c 独立开启将为 29、与 P2b 都开启将为 31，尚未实现；没有 SQL/Wiki/ACL 占位项。
- 开关作用于当前 Server 的工具暴露，不是按用户授权；每次调用仍须严格解析现有身份。部署者必须确保可访问此入口的调用者获准使用对应管控面能力，不能把 `require_identity=true` 当作数据权限已隔离。
- 所有出站选项同时注入主 Client 和 Tenant Client；没有第二套 region、用户绑定、Token、可配置 Action 或动态 endpoint。
- P3 的执行 Endpoint/预算/限额配置须在正式协议确认后单独冻结；本次不提供可复制运行的 SQL 配置。DataWiki/ACL 同理。
- 关闭单个新增能力后不注册其工具、不额外调用云端，原元数据与分析工具仍可用；启动不做权限/数据源网络探测。

## 9. 安全、错误与可观测性

### 9.1 HTTP 与凭证安全

- Endpoint 限制为管理员批准的官方 OpenAPI 目标；禁用重定向，防止签名头或 STS Token 发往其他服务。测试目标通过测试注入，不开放任意生产目标参数。
- 成功和错误响应都在解压后、JSON 解码前进行大小限制，超限返回完整错误，不截断 JSON 后假装成功。
- 当前 `readAPIResponse()` 使用无上限 `io.ReadAll` 和普通 `json.Unmarshal`；不能未经调整就声称新工具满足大小和精度要求。新路径采用受限读取及有类型模型、RawMessage 或 UseNumber，并验证完整 JSON。
- 禁止日志记录 AK/SK、SecurityToken、Authorization、签名 URL、原始结果或开放扩展属性；长度截断不是脱敏。
- 数据和注释不能触发外部请求或工具注册；不新增自动导出、下载或跨身份缓存。

### 9.2 错误分类

| 情况 | 本地错误码（拟定） | 行为 |
|---|---|---|
| 未知/非法参数 | `GATEWAY_INVALID_ARGUMENT` | 不发送 OpenAPI 请求 |
| 缺失或无效入站身份 | `GATEWAY_IDENTITY_REJECTED` | 不解析其他角色，不使用全局身份 |
| 未匹配角色且无显式 default | `GATEWAY_IDENTITY_UNMAPPED` | 拒绝调用 |
| AssumeRole/凭证获取失败 | `GATEWAY_CREDENTIAL_ERROR` | 不使用旧快照或切换身份兜底 |
| 签名或凭证被上游拒绝 | `GATEWAY_AUTH_FAILED` | 不换 AK、角色、地域或接入协议 |
| RAM/服务端权限拒绝 | `GATEWAY_ACCESS_DENIED` | 保留已知拒绝层次与真实 RequestId，未知时不猜测 |
| 明确限流响应 | `GATEWAY_RATE_LIMITED` | 有 Retry-After 时遵守，否则受本地有限退避约束 |
| 不支持 Action/响应不兼容 | `GATEWAY_UPSTREAM_INCOMPATIBLE` | 不调用其他后端补洞 |
| 响应超限 | `GATEWAY_RESULT_TOO_LARGE` | 完整错误，无部分成功结果 |
| 网络故障 | `GATEWAY_UNAVAILABLE` | 仅按已确认只读调用规则处理 |

首期只读 Action 可在出站预算内对短暂网络故障或明确限流最多重试一次；鉴权/权限错误不自动重试。业务错误可能以 HTTP 200 返回，不能仅按 HTTP 401/403/429 分类。取消传播到 HTTP 请求，不增加无界后台重试。

### 9.3 请求日志与审计

- Gateway wrapper 复用 Resolver 和请求关联机制，但不能直接继承原 wrapper 的所有日志和文本输出行为。
- 仅在验证后记录 caller、group/role、tool、Action、本地 request ID、上游 RequestId、结果类别与耗时；无凭证或数据正文。
- 当前网关路径通过 `WithGatewayIdentity` 的请求上下文传递已验证 caller 元数据；后续复用它，不能从未验证 JWT 解码出用户名当作可信审计。
- 即使 `log.requests=full`，也不记录搜索词、历史筛选、SQL、Wiki 查询与返回数据。错误正文不直接记录，仅记录安全分类及必要标识。
- RoleSessionName 可关联 STS/ActionTrail，但并不保证唯一，也不等于网关内部用户；实际网关审计字段在联调时核验。
- stdout 仅承载入站 MCP，日志写 stderr；不增加独立网关 MCP Session ID。

## 10. 后续实施文件与复用点

P2a/P2b 已按以下落点实施；P2c/P3 部分仍是后续规划。API Key 移除已完成，不重复建设认证链。

| 路径 / 当前复用点 | 后续最小修改 |
|---|---|
| [internal/dataagent/gateway.go](internal/dataagent/gateway.go) 的 `gatewayParamsValid`、`gatewayEntitySchema`、有类型方法 | P2a/P2b 分别增加 3/2 个固定方法及静态参数规则；P2c 待实施，不提供外部 Action 字符串入口 |
| 同文件 `readGatewayResponse` | 已按固定 Action 分开详情对象与数组，并验证请求目标；P2b 同样只接受对象，成功 null 分支因真实样例未确认而延期。保留原 6 个 Action 的数组验证，不用接受任意 JSON 的通用 map 放宽它们 |
| 同文件 `gatewayPaged`、`GatewayResult` | 原 NextToken 分页保留；P2c 单独处理 Page/Size/TotalCount，不把页码塞入 NextToken，不把 TotalCount 经过 float64 |
| [internal/mcp/gateway.go](internal/mcp/gateway.go) 的 `gatewayClient`、`gatewayTools`、`arguments`、`invokeGateway` | 增加静态 Schema 与可选过滤参数校验，分组注册；布尔 `paginated` 不足以表示三类输出，需明确无分页/游标/页码策略 |
| 同文件 `withGateway`、`gatewayEnvelope` | 继续严格入站身份和单 TextContent；允许新增详情 data 对象及合法 null，历史用独立分页结构；原工具结果不变 |
| [internal/config/config.go](internal/config/config.go)、[main.go](main.go)、[internal/tenant/registry.go](internal/tenant/registry.go) | P2b 已新增显式暴露选项并传入注册路径，P2c 选项待实施；保持主/租户出站配置一致、严格刷新不回退。无需改身份或会话生命周期 |
| [internal/mcp/reqlog.go](internal/mcp/reqlog.go) 及网关专用日志 | 保留安全 caller、Action、请求标识、结果类别和耗时，不记录新筛选参数、数据源属性或历史正文 |
| [internal/dataagent/gateway_test.go](internal/dataagent/gateway_test.go)、[internal/mcp/gateway_test.go](internal/mcp/gateway_test.go)、[internal/config/config_test.go](internal/config/config_test.go) | P2a/P2b 已覆盖对象/数组、过滤参数、目标一致性、敏感字段丢弃、独立开关与 19/28/30；null 当前按拒绝测试，后续补合法 null 契约、历史分页及对应跨角色拒绝 |
| [config.yaml.example](config.yaml.example)、[README.md](README.md)、[MCP Skill](../alibabacloud-data-agent-mcp-skill/SKILL.md)、[安装说明](../alibabacloud-data-agent-mcp-skill/references/INSTALLATION.md)、[部署说明](../alibabacloud-data-agent-mcp-skill/README.md)、[RAM 权限说明](../alibabacloud-data-agent-mcp-skill/references/ram-policies.md) | 随每个实际交付能力同步工具、权限、启用条件和数量；待实施提案不能提前写成当前使用能力 |

P3 必须另有正式执行协议评审后才确定适配代码；不要把凭证签发塞进现有只读自动重试循环。知识库若另行集成，须单独实现 formData 编码/签名和 Results/Matches 解析，不能直接复用 query+Data[] 的假设。

现有 [dacli](cmd/dacli/main.go) 的 `call <tool> [json-args]` 足以验收新增工具，无需新建 CLI、南向 MCP Client 或 Token Registry。测试沿用接口 mock、httptest TLS Server 与表驱动模式；真实测试在隔离会话目录中启动独立二进制，不影响其他本地 Server。

## 11. 实施阶段与发布门槛

### A / P1：保留已实现基线

Server 全局 AK/STS 收敛与 6 个网关元数据工具已实现，并有第 2.2 节的本地和真实验证证据。后续改动必须回归原 19 个工具、API Key 迁移拒绝、严格身份、签名 SSE 与三种入站 transport；不重复宣称尚待首期实施授权。

### P0：每个新增能力共同的契约与授权门槛

在获得真实只读联调授权后，对每个新增 Action 分别完成：

1. 冻结版本、方法、参数位置/大小写、引用模型、空值、分页、错误和最小 RAM 权限；保存脱敏样例，不提交秘密或业务数据。
2. 验证目标地域的拟部署 SDK/STS 身份，服务初始化条件及角色到上游数据/管控主体的实际映射；记录可见范围，不能只记录 AssumeRole 成功。
3. 验证凭证更新、刷新失败、到期、RequestId、响应精度、预算和大小上限；模拟测试与真实云端结果分别标记。
4. 用不同有效授权范围的角色进行正反向验证；同角色不同会话单列为共享范围验证，不把它当隔离测试。

若无法证明多用户数据/管控范围符合部署预期，则阻断该能力在多用户入口发布。缺少云端环境时可以开发 mock，但不能标为真实验收完成；不能自动补授全权限或换全局 AK。

### P2a：元数据详情与数据库搜索

已实现第 7.1 节的 3 个工具及 Data 对象与旧数组契约的分离，GetAgenticDatabase 引用模型已通过官方 SDK 补证。最小 RAM 定义与跨角色数据授权仍为生产发布门槛；真实验收需覆盖“搜索 → 详情 → 表 → 字段/索引”。此批不依赖 SQL/Wiki/审计，也不新增配置秘密。

### P2b / P2c：独立可选能力包

- **P2b：** 实现数据源摘要及显式开关；只有管控范围、关联标识和秘密字段裁剪验收通过才启用。
- **P2c：** 实现当前上游主体的 SQL 历史摘要及显式开关；只有角色映射、时间语义、TotalCount/分页和敏感正文隔离验收通过才启用。
- 两者可独立交付，不要求同时上线；也不要求先执行新 SQL 才能读已有的获授权历史。任何失败仅影响对应工具，不使原分析工具切换身份或协议。

### P3 / DataWiki / ACL：先消除协议缺口

SQL 按第 7.4 节全部门槛通过后再实施；DataWiki 与 ACL 分别按第 7.5 节取得正式等价契约后再冻结工具和配置。没有契约时交付调研结论，不实现占位、不以 Bearer MCP、知识库或旧分析链替代。

**建议实施顺序：P2a → 按需 P2b/P2c；同时推动 SQL 执行协议与 DataWiki/ACL 契约确认。** 全局审计、凭证/Agent 管理、数据源写入与探测始终不进入此分析入口。

## 12. 验证计划 / Acceptance Criteria

### 自动化

- **注册与兼容**：关闭网关为 19 个，仅基础网关开启为 28 个，再开启 P2b 为 30 个；没有 SQL/Wiki/权限占位；AK/STS 下原工具 Schema、输出与三种入站 transport 不变，API Key 兼容性明确不保留。
- **签名与调用**：9 个准确 Action、固定版本、POST query 编码、SecurityToken 正确传递；原分析普通 API 和 SSE 也只有签名调用，整个 Server 不发送 `/apikey` 或 `x-api-key`；不向网关注入无关 DmsUnit/Tid。
- **配置/迁移**：网关开/关 × 旧 YAML 键/非空旧环境变量 × 有/无有效 SDK 凭证均验证；旧 API Key 输入必须报迁移错误，不静默切换身份。旧配置清理且无 SDK 凭证时启动失败，纯 AK/STS 配置成功。
- **清理完整性**：删除 API Key 专属结构、选项、Action 别名、传输分支及运行测试；仅迁移诊断、负向测试、通用脱敏和明确标记的历史说明可以保留相关字样。
- **网关配置**：远程未开 identity 拒绝；非法 Endpoint/限额拒绝；主 Client 与 Tenant Client 选项一致。
- **身份**：无效共享 Token、缺失/过期/非法 JWT、缺失 user/email、无匹配组、显式 default、未知用户、伪造 role 参数；禁止匿名/失败时回退全局 AK。
- **隔离**：不同用户并发不串 STS；不同角色权限按预期区分；同角色不同会话不被误测为独立数据授权；无跨用户结果缓存。
- **凭证**：成功刷新、刷新失败、到期、基础凭证轮换，禁止 Gateway 静默旧快照回退；不改变原分析路径的既定行为。
- **分页/数据**：根级 NextToken、空页但仍有下一页、错误游标、字段/索引无分页、UUID/QualifiedName 原值、NULL、大整数、精确小数、扩展属性白名单。
- **错误/预算**：HTTP 200+Success=false、非 JSON、畸形/多余 JSON、gzip 大响应、超限、429/业务限流、权限拒绝、取消与有限重试；取证与 HTTP 超时分别验证。
- **日志**：basic/full/off、SDK/HTTP/解析异常均不含秘密、签名 URL、搜索词或返回数据；单 TextContent 的错误 JSON 可解析，本地/上游 request ID 不混淆。

实施后在 `server/` 运行：

```bash
go test ./...
go test -race ./...
go vet ./...
```

### 真实环境端到端

使用获授权的测试 RAM 身份/角色和非生产数据源，不在 Spec 阶段读取真实秘密或修改云端权限。

1. 通过 dacli/宿主完成 Catalog → Database → Table → 字段/索引发现链，验证翻页、空结果和标识可继续使用。
2. 使用两种不同授权角色验证正反向可见性及拒绝；核对 RAM Action 权限与服务端资源授权分别如何生效。
3. 用同角色不同 RoleSessionName 的调用验证审计差异，明确其不等于数据权限隔离。
4. 验证凭证更新、错误地域、上游不可用和服务重启；失败不切换身份或接入协议。
5. 回归 AK/STS 下原分析会话创建/等待/继续、签名 SSE、上传签名和回调；网关开/关都可用，入站平台 Token/JWT、STS 与 OSS 签名未被误删。
6. 验证旧 API Key 部署在清理旧配置并配置 SDK 凭证链后才能启动；新版本无任何 API Key 回退。确认独立 Python CLI 文件和认证行为未修改。

**首期完成标准：** A 的 API Key 移除及迁移验收通过，P0 契约与授权证据齐全，P1 自动化及真实元数据链通过。第 2.2 节只陈述已实际验证的子集，不代表跨角色等门槛全部完成；仅 mock、工具列表可见、HTTP 200 或 AssumeRole 成功均不足以证明完成。

### 后续扩展自动化与真实验收

| 范围 | 必测内容 / 不可替代的证据 |
|---|---|
| 工具注册 | 首期 19/25 保留为历史基线；当前 P2a/P2b 验证 19/28/30，P2c 的 29/31 组合待实施；未交付能力无 Schema、handler 或空开关 |
| P2a 对象与搜索 | 对象/数组不能互换，Data 缺失/null/类型错误/目标 UUID 不符；过滤参数精确映射、非法值不出站、错误游标不重试换范围；真实搜索命中可接详情及原表发现链 |
| P2a 模型完整性 | 补齐 AgenticDatabase 引用模型；详情/列表同名字段类型核对，NULL/int64/小数不失真，Catalog 开放属性及未知字段不出结果 |
| P2b 秘密防泄漏 | 列表和详情同时注入密码、用户名、KMS/角色/Host/VPC/未知嵌套字段，全部被白名单移除；拒绝 search_key/Host 等可推断隐藏属性的筛选；当前 Data:null、缺失 Data 与空对象分别拒绝，404 不改成功；合法 null 分支待真实补证，TotalCount 不泄漏 |
| P2b 管控范围 | 真实普通角色/管控角色的可见差异，跨账号数据源 UUID 拒绝或明确不可见；账号级共享范围经部署者确认，不能宣称逐聊天用户隔离；现有 Catalog 搜索支持地址前缀，不能据 P2b 的字段裁剪宣称整个 Server 具备 Host 保密性 |
| P2c 分页/时间 | Page/Size 与 NextToken 分离；7 天跨度与 730 天回溯边界、未来时间、错误日期、无时区格式、结束早于开始、超范围页、TotalCount=0/大整数/缺失；验证实时分页不会被描述为快照 |
| P2c 真实归属 | 当前用户与其 Agent、不同角色、同角色不同会话分别验证；不接收 Actor 覆盖，不调用全局历史；既有 FAILED/BLOCKED 记录可正常作为历史返回 |
| P2c 输出 | SQL 原文、执行 SQL、原始错误、IP、Actor 信息和未知字段均不返回；完整 RequestId 与总数保留，Cost 未确认单位不输出 |
| 全部 P2 | 签名 POST query、STS SecurityToken、严格取证失败、HTTP/业务拒绝、有限重试/取消、gzip 后大小限制、错误 JSON、basic/full/off 日志；工具参数和返回正文都不进入日志 |
| 回归 | 网关开/关及新增可选能力开/关，原分析会话创建/等待/追问、签名 SSE、上传签名/回调、三种入站 transport；保留 API Key 旧配置拒绝与 dacli 数字精度测试 |
| P3（通过协议门槛后） | 真实只读执行结果及列类型、服务端写入拒绝、跨库/跨角色拒绝、字段/行保护与实际审计关联；凭证到期/连接次数、超时后结果未知不重放，不能用旧 DataAgent 查询替代 |
| DataWiki/ACL（契约确认后） | Wiki/KB 标识与引用来源验证，跨角色知识内容不可见；有效权限与执行行为一致，空权限与获取失败可区分；不从 ADMIN 或本地分组生成授权结论 |

每批实施均重新运行 `go test ./...`、`go test -race ./...`、`go vet ./...`，并通过独立二进制/dacli 完成获授权的真实云端链路。发布记录必须说明 Action、协议/样例版本、地域、身份类型、授权范围、RequestId 及成功/拒绝结论；秘密和未脱敏业务内容不得提交仓库。

**阶段完成标准：** 代码与文档匹配、对应 P0 契约/授权证据、自动化和真实验收均通过后才开启该能力。缺失 GetAgenticDatabase 模型/RAM 定义、数据源可见范围、历史主体/时区或 SQL 执行协议时，分别阻断对应阶段，不阻断其他已验证能力、不换后端补洞。

## 13. 决策与待确认项

| 项目 | 当前决策/状态 | 阻断范围 |
|---|---|---|
| 接入形态与认证 | 保持 OpenAPI + SDK/RAM/STS，Go Server API Key 模式已移除 | 不重新引入 MCP/Bearer 或 Token Registry |
| 用户映射 | 复用 identity 分组/default；同角色不等于不同数据权限 | 逐用户隔离的部署声明 |
| 首期能力 | 6 个元数据工具，开启共 25；真实 stdio 六工具和远程角色 Catalog 已验证 | 仍需跨角色、各 Action 及真实 JWT 等完整验收 |
| P2a | 已实现 3 个元数据定位工具，开启共 28；官方 SDK 引用模型已补证 | GetAgenticDatabase 最小 RAM 定义与跨角色授权等生产验收仍待完成 |
| P2b | 已实现默认关闭的 2 个管控面摘要工具，三传输真实样本验证通过；敏感配置不出结果，成功 null 未开放 | 账号级可见范围是否符合部署对象授权，跨角色及最小权限验收 |
| P2c | 可选 1 个非全局 SQL 历史摘要，不含 SQL/Actor/IP 正文 | RAM 定义、上游主体映射、时间与分页实测 |
| 地域及服务初始化 | Endpoint 存在不代表所有 Action 可用 | 对应地域/身份的发布 |
| SQL/OneQuery | 凭证签发和已发布数据服务协议各自明确，但直接 SQL 的衔接、取消/结果协议未证实 | P3 工具、执行配置及任何可用性承诺 |
| DataWiki | 通用 KB API 不能直接证明 wiki_search 等价 | Wiki 工具；不影响 P2 |
| ACL 自查 | 未建立等价 OpenAPI 映射；不伪造权限快照 | ACL 工具；不影响 P2 |
| 管理/秘密接口 | Agent/Token 生命周期、数据源写入/探测、全局历史不进入分析 MCP | 不在本次集成范围 |
| 所有 SQL 强制过网关 | 不迁移旧分析执行链 | 需另行评审限制旧工具或迁移执行路径 |

## 14. Sources

- [S1 Dms 2025-04-14 OpenAPI 机器可读定义](https://api.aliyun.com/meta/v1/products/Dms/versions/2025-04-14/api-docs.json)
- [S2 Agent 数据网关 MCP 服务（仅作能力与安全语义参考）](https://help.aliyun.com/zh/aidbs/user-guide/agent-gateway-mcp-service)
- [S3 CreateAgenticSqlExecuteCredential](https://help.aliyun.com/zh/dms/developer-reference/api-dms-2025-04-14-createagenticsqlexecutecredential)
- [S4 数据保护](https://help.aliyun.com/zh/aidbs/user-guide/data-protection)
- [S5 从 DMS MCP 迁移到 Agent 数据网关 MCP](https://help.aliyun.com/zh/dms/use-cases/migrate-from-dms-mcp-to-agent-data-gateway-mcp)
- [S6 Agent 数据网关概览](https://help.aliyun.com/zh/aidbs/user-guide/agent-gateway/)
- [S7 DMS 身份管理](https://help.aliyun.com/zh/dms/dms-identity-management)
- [S8 AssumeRole 权限与会话语义](https://help.aliyun.com/zh/ram/developer-reference/api-sts-2015-04-01-assumerole)
- [S9 数据服务：已发布 API 的 OneQuery HTTPS/SSE 协议](https://help.aliyun.com/zh/dms/data-service-new)
- [S10 网关审计与可观测](https://help.aliyun.com/zh/aidbs/user-guide/view-sql-execution-logs)
- [S11 网关数据源管理](https://help.aliyun.com/zh/aidbs/user-guide/agent-gateway-datasources)
- [S12 网关用户管理](https://help.aliyun.com/zh/aidbs/user-guide/agent-gateway-user-management)
- [S13 网关 Agent 身份与权限管理](https://help.aliyun.com/zh/aidbs/user-guide/agent-gateway-identity-permission)
- [S14 网关快速接入（非本方案的 Token 接入路线）](https://help.aliyun.com/zh/aidbs/user-guide/quick-guide-for-agent-gateway-access)
- [S15 官方 Python SDK alibabacloud-dms20250414 4.2.2](https://pypi.org/project/alibabacloud-dms20250414/4.2.2/)
- [S16 官方 Go SDK dms-20250414 v4.2.0 发布包](https://proxy.golang.org/github.com/alibabacloud-go/dms-20250414/v4/@v/v4.2.0.zip)
- [S17 GetAgenticDatabase（暂无授权信息透出）](https://help.aliyun.com/zh/dms/developer-reference/api-dms-2025-04-14-getagenticdatabase)
- [S18 ListAgenticDataSources](https://help.aliyun.com/zh/dms/developer-reference/api-dms-2025-04-14-listagenticdatasources)
- [S19 GetAgenticDataSource](https://help.aliyun.com/zh/dms/developer-reference/api-dms-2025-04-14-getagenticdatasource)

P2b 于 2026-10-29 补核：S1/S16 确认 POST query、对象/数组与有符号 int32 State。S18/S19 帮助页仍称无授权信息，但 S1 `ramActions` 已列 `dms:ListAgenticDataSources`、`dms:GetAgenticDataSource`，`authLevel=operate`、Resource `*`、空条件数组；该资料差异不能解释成免鉴权或逐用户隔离。

P2a 模型与授权缺口于 2026-10-29 补核：S15/S16 确认引用模型及位宽，S17 仍未公开最小 RAM 授权定义；SDK 的可空字段不构成成功 `Data:null` 的协议依据。

本次扩展资料核对日期为 2026-10-28；S1 快照 SHA-256 为 `7c2269cbadc4432af699ff4d6c26bd4af138aa98c1aa7a07d50585196a33f8b5`。引用到机器定义时以具体 Action、parameter.in、response schema 和 ramActions 为定位依据；接口数与缺失项只针对该快照，不断言后台能力不存在。S7/S8 延续首期身份依据。

官方事实、项目提案与待联调事项分别标记；不把官方网关 MCP 参数、状态、控制台权限或产品级能力直接当作 OpenAPI 已验证契约。

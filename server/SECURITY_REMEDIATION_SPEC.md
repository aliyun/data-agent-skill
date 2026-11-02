# Go MCP Server 安全修复 Spec

- 日期：2026-11-02。
- 状态：本地代码修复、自动化回归及源码/六产物扫描已完成；最终原生产物的三传输九配置真实元数据与协议隔离复验通过。真实身份平台、差异角色及部署等发布门禁尚未完成，证据见第 9 节；不得据此宣布生产隔离通过。
- 范围：Go Server、dacli 回归、构建及部署文档；独立 Python CLI/SDK 不变。
- 基线：[网关集成 Spec](AGENT_GATEWAY_SPEC.md) 的 19/28/30 工具注册、SDK/RAM/STS 认证、P2b 默认关闭均保留。
- 已获授权实施本地代码、测试、示例及构建要求；真实配置/权限、历史迁移、密钥轮换、云端角色/策略、部署和提交代码仍不自动执行。

## 1. 目标与边界

修复本次审查发现的本地会话越界、身份键碰撞、角色映射歧义、远程协议认证缺口和资源边界，同时加固日志、旧出站客户端与网关响应解析。

本方案实施后，以下条款取代网关集成 Spec 中对应的旧兼容约定：远程旧工具不再缺 JWT 回退 Header、不再缺身份回退全局 AK；租户键和存储布局升级；入口由应用控制；初始化移出全局锁。其他业务和工具契约不变。上述变更必须随版本明确公告，不提供保留漏洞的兼容开关。

### 不在本次范围

- 不新增 P2c/P3、SQL/Wiki/ACL 工具、通用 Action 转发、网关 Token 或云端授权管理。
- 不将旧 Data Agent SQL 改为网关执行；旧链路不会因此自动获得行列保护。
- 不建设多发行者认证平台、在线历史迁移服务、缓存权限服务或通用文件系统框架。
- 不自动删除历史文件、修改 RAM/信任策略、关闭其他 Server、轮换真实秘密或收紧现有文件权限。
- 不承诺同一 RAM Role 的不同用户在云端具有不同数据权限，也不把本地目录隔离当作云端会话 ACL。

## 2. 审查基线与威胁模型

### 2.1 修复前审查证据

以下为修复前基线，不代表当前实现状态；修复后的实际检查与未完成门禁见第 9 节。

- 本地合成测试复现了跨目录读取结果、身份目录名碰撞、跨组冲突仍返回角色、已知 sessionId 时向其他 SSE 通道注入协议响应、full 日志参数键换行注入。
- 无界请求体读取、缓存命名空间、全局锁和旧出站重定向按代码及局部测试核查；未进行攻击压测，不把所有风险描述为已在真实部署利用。
- `go test -race ./...`、`go vet ./...`、`go mod verify` 通过，但 Go 1.26.1 的 govulncheck 扫描仍报告 25 条标准库符号可达告警。告警有协议、平台或证书条件，不等于 25 条已复现攻击。
- 工作区 111 个文本文件的规则扫描未发现真实密钥；未覆盖完整 Git 历史和所有秘密格式。被 Git 忽略的本地配置含平台 Token，权限为 0644；这是本机访问控制问题，不是已证实的仓库泄漏。
- 真实双角色均看到 28 个数据源和 28 个 Catalog，抽样集合一致；尚无预期差异授权样本。共同拒绝的字段/索引请求缺少可用上游关联 ID，不能据此宣布隔离通过。

### 2.2 信任边界

1. 不可信输入包括所有 HTTP 请求、工具参数、协议 session ID、上游 JSON、持久化状态中的标识及历史迁移文件。
2. 平台认证 Token 仅交受信代理；Header 模式由代理覆盖用户自报身份头，终端用户不能持有平台 Token。JWT 必须验签后才能使用身份字段。
3. 配置、进程账户和会话根目录由部署管理员控制。不防御同一 OS 账户/root 篡改程序、挂载点或硬链接；普通调用者不得具有会话目录写权限。
4. 本轮支持一个配置明确的可信身份域。多个 JWT 密钥在该域内具有等价签名权限，可用于轮换或互信应用；不将两个互不信任的组织/发行者放进同一密钥集合。独立信任域使用独立实例、配置及存储根目录。
5. 本地存储身份、MCP 协议会话、Data Agent 云端会话、RAM Role 是不同对象，不互相替代其授权检查。

## 3. 风险登记与修复分组

下列位置为编写时的定位点，实施以符号和测试为准。

| ID | 优先级 / 来源 | 问题与落点 | 完成标准 |
|---|---|---|---|
| SEC-01 | P0 / 既有 | `LoadState`、`Manager.SessionDir` 及结果图片路径接受越界 session_id：[state.go](internal/session/state.go)、[manager.go](internal/session/manager.go)、[tools.go](internal/mcp/tools.go) | 非法 ID 在任何读取、写入、锁分配或云端调用前拒绝；根目录外路径及符号链接不可访问 |
| SEC-02 | P0 / 既有 | `sanitize` 将不同身份映射到同一路径：[registry.go](internal/tenant/registry.go) | 无损身份元组生成稳定摘要；碰撞样本、大小写及 Unicode 样本不共用目录 |
| SEC-03 | P0 / 条件性既有 | `identityKey` 未区分来源、tenant、agent 和 user_id/email 类型 | 同字符串不同命名空间/类型不能复用 Client、Manager、协议会话或磁盘历史 |
| SEC-04 | P0 / 既有 | `ResolveGroup` 接受 user_id/email 跨组命中：[config.go](internal/config/config.go) | 多组匹配必须拒绝，不按 map 顺序或默认角色兜底 |
| SEC-05 | P0 / 既有 | HTTP 在认证前读无界 body；直接调用依赖的 Start：[server.go](internal/mcp/server.go) | 所有远程方法先认证，入口有请求体/头/读取时间上限，拒绝不进入 dispatch/STS |
| SEC-06 | P0 / 既有 | SSE sessionId 未绑定主体；HTTP 默认 session manager 仅验证格式 | 所有带 ID 的 GET/POST/DELETE 验证签发、主体和生命周期；跨主体零投递 |
| SEC-07 | P0 / 工具链 | 当前 Go 标准库可达漏洞告警：[go.mod](go.mod)、[Makefile](Makefile) | 用已修复工具链重建 Server/dacli 和各平台产物，再进行源码及二进制扫描 |
| SEC-08 | P1 / 既有 | `formatArgs` 原样输出键；其他动态日志字段亦需同规则：[reqlog.go](internal/mcp/reqlog.go) | 换行/控制符不产生新日志记录，拒绝请求不冒用未验证 caller，秘密不输出 |
| SEC-09 | P1 / 既有 | 旧 API/SSE client 跟随重定向：[client.go](internal/dataagent/client.go)、[sse.go](internal/dataagent/sse.go) | 所有签名 API/SSE 拒绝重定向，目标服务收到零次后续请求 |
| SEC-10 | P1 / 既有 | 全局租户锁覆盖 SDK 取证与 RestoreSessions | 锁内无网络/磁盘恢复；同键初始化合并、不同键独立、等待可取消、并发有界 |
| SEC-11 | P1 / 部署 | 明文平台 Token 文件权限偏宽 | 经管理员授权修复权限/秘密注入，启动检查和日志不输出秘密 |
| SEC-12 | P1 / 新增网关完整性 | `gatewayDecodeRoot` 仅拒绝根重复键，非法代理项可被替换：[gateway.go](internal/dataagent/gateway.go) | 任意层重复键和无效 Unicode 转义都拒绝，保持数值精度/NULL/白名单 |

P0/P1 表示实施优先级，不是未经评估的 CVSS 分数。SEC-12 尚无证据证明普通调用者可控制上游原始响应或借此越权。

## 4. 详细设计

### 4.1 SEC-01：会话 ID 与根目录约束

- 在 session 包集中定义 `ValidateSessionID`。本地接受 1–128 字节的 ASCII 字母、数字、`_`、`-`；跨平台统一拒绝 Windows 保留设备名。此限制是本地路径契约，不是对上游 ID 规范的推断；不强制 UUID，不 trim、解码、清洗或替换调用者的 ID。
- MCP Schema 只是提示；工具 wrapper 在 Tenant Resolver 前校验会话 ID，Manager 所有接受 ID 的公开入口也实际校验，包括 watch、status、wait、result、send、stop、confirm/auto-confirm 路径。校验在操作锁/map 分配之前，内部恢复路径和云端新建返回的 SessionID 也校验。操作锁按持有者与等待者引用计数，在最后一次释放时回收；有效但不存在的 ID 不留下永久锁条目。
- 上游新建返回非法 ID 时拒绝本地落盘、消息发送和 watcher 启动，不尝试用改写后的 ID 重试创建；报告已发生的远端创建无法确认后续处理，不自动删除远端资源。
- 使用修复版 Go 的 `os.Root` 将会话存储锚定在当前租户目录。读状态、枚举历史、持久化、图片读写和原子重命名均经该根句柄执行；禁止把验证过的相对路径重新交回不受约束的 `os.ReadFile/WriteFile`。
- 替换向 MCP 返回裸路径的 `SessionDir(string) string` 接口，提供受根约束的图片枚举/读取方法；图片名称由服务生成且仅为单个文件名，不接受 URL 或外部路径。状态文件、图片目录/文件须是预期普通文件/目录，拒绝符号链接和设备文件。
- `filepath.Clean`、字符串前缀比较、先 EvalSymlinks 再普通打开均不能单独作为隔离措施。根句柄负责防止符号链接替换时越界；根目录自身必须由进程账户控制。根能力不承诺阻止 root 建立的挂载或硬链接。
- 加载状态后验证 `snapshot.SessionID == 请求/目录 ID`，不信任状态内另一个 SessionID；持久化不能覆盖 ID 不匹配的已有状态，包括大小写不敏感文件系统上的别名碰撞。
- 新建目录 0700、状态/图片/临时文件 0600，保留现有原子写入、fsync、发送未知态与 checkpoint 恢复语义。根句柄在 watcher/恢复任务停止后关闭。
- 非法 ID 使用稳定的本地参数错误，不回显完整攻击串、绝对路径或其他租户存在性；不存在与未获准访问不暴露可枚举细节。此规则不改变合法工具返回格式。

### 4.2 SEC-02/03：规范身份键与存储布局

新增 `identity.namespace`：管理员指定的稳定非秘密身份域名称，远程模式必填，格式为 1–64 字符 `[a-z0-9][a-z0-9_-]*`。不能从请求 Header、未验证 iss/kid、RAM Role、密钥内容或其数组下标推导；改变该值意味着新的身份域及本地历史空间。

认证成功后构造不可变 `Principal`，使用固定字段、固定顺序的规范 JSON 数组编码：

```text
["principal-v2", namespace, auth_mode, tenant_id, agent_id, subject_kind, subject_value]
principal_id = lowercase_hex(SHA256(canonical_bytes))
```

- `auth_mode` 仅 `jwt` / `headers`；`subject_kind` 为 `user_id` 或 `email`。优先使用非空 user_id，否则用 email；不将相同字符串的两种类型合并。
- JWT 的 tenant_id/agent_id 必须非空，且仅来自验签成功的 claims，不猜测或取未验证 Header；缺字段拒绝，不降级。Header 模式两者为空，由配置身份域提供边界，代理必须保证该域内身份唯一且覆盖自报字段。
- 身份字段仅接受字符串或相应 ID 的精确整数字面量；email 仅字符串。保留 19 位数字、大小写及 Unicode 原值，拒绝对象、数组、布尔、控制字符及隐式浮点转换。沿用明确的外围空白处理规则，同一规则用于校验、匹配及编码。
- `tenant_id/agent_id` 值变化会产生不同主体，不能自动把历史合并；同主体的 JWT 内容、exp、签名密钥轮换不改变 principal_id。
- 全部验证密钥属于同一个管理员声明的信任域；相同 tenant/agent/user 的声明在该域内被认为同一主体。namespace 和摘要不是验签或发行者授权的替代品，角色映射还必须按第 4.3 节检查 tenant/agent 范围，不能只隔离缓存而继续按跨域裸 ID 授权。
- 首版不支持独立不互信的发行者混接。现有 `secret/secrets` 配置不改成泛用多发行者路由；其部署说明必须明确等价签名权限这一限制。无法提供所需 claims 的 JWT 上游须先完善身份契约，不以缺失范围字段的 JWT 上线；Header 接入只能作为管理员明确选择的受信代理部署模式。

内存缓存按 principal_id 索引，磁盘采用 `{sessions_dir}/identity-v2/{principal_id}/`。不再使用 sanitize 的有损字符串；固定小写摘要避免大小写不敏感文件系统上的身份目录碰撞。

租户目录建立 `owner.json`，仅含格式版本与 principal_id，使用 0600 原子写入。打开已有目录时须核对归属；拒绝 tenant 目录符号链接、归属不符或非空无归属目录，不能自动补标记认领已有数据。该文件证明本地布局一致性，不证明云端授权。

#### 历史迁移

1. 新版本不扫描或回读旧 `identity/<sanitize(key)>`，不提供“新目录不存在就读旧目录”的兼容分支；旧目录保留不删除。stdio 的单进程历史仍使用其受约束的本地根，不自动导入租户历史。
2. 升级前停写并备份旧目录；新版本可以从空的 identity-v2 开始，历史暂不可见不等于历史被删除。
3. 如需保留历史，由管理员使用可信业务记录确认旧目录到完整 Principal 的唯一归属后离线复制并校验状态 ID/文件类型/权限。一个旧目录可能因清洗碰撞混有多个用户数据，不能只看目录名、当前组映射或 DescribeSession 成功来自动分配。
4. 无法证明唯一归属的数据留在离线备份，不导入在线目录。禁止 symlink、共享目录、多主体认领以及覆写非空目标。首版不交付自动认领/迁移工具，迁移操作另行授权。
5. 认证模式、namespace 或 subject_kind 改变视为显式迁移；JWT 密钥轮换保持稳定身份。回滚不能把新目录重新映射回旧有损布局。

### 4.3 SEC-03/04：有范围的分组匹配，歧义必须拒绝

- JWT 模式为 `identity.groups.<name>` 和 `identity.default` 增加必填 `tenant_id`、`agent_id`，均为管理员配置的精确值；不支持通配符或从请求自动补全。Header 模式这两个配置项必须为空，禁止让 Header 冒充 JWT 范围。它们限定同一可信域内的成员匹配，不建立新的发行者信任。
- `ResolveGroup` 改为接收已验证的 Principal/Claims：先筛选 auth_mode 和 tenant/agent 范围，再在范围内匹配 user_id/email。无命名组匹配时，只有 default 范围也完全一致才可使用；否则拒绝。即使 user_id 相同，另一个 tenant/agent 的调用也不能命中该组或 default。
- 范围内恰好一个组才成功，多个不同组报 `IDENTITY_GROUP_CONFLICT`。即使两组 RoleArn 相同，也拒绝歧义，避免 workspace/defaults 不一致；同组 user_id 和 email 同时匹配只计一次。
- 启动时重复成员校验使用 `(范围, 原始成员值)`：同范围跨组重复拒绝，不同明确范围内同号成员允许分别映射。请求期仍检测 user_id/email 交叉命中；Header 代理必须保证成员标识在整个配置身份域内唯一，不满足时分实例。
- 每次请求重新根据已验证身份计算映射；缓存命中仍核对 group、RoleArn、选定 RoleSessionName 和会话 defaults，不能沿用首次创建的不同授权上下文。配置首版仍需重启生效，不新增热重载。
- 对外只给安全分类，不显示其他组成员。网关继续映射为 `GATEWAY_IDENTITY_UNMAPPED` 的固定安全消息，内部可记录冲突类别；旧工具保留 MCP error 形态。JWT 旧配置缺少成员范围时启动报迁移错误，不默认为全域。

### 4.4 SEC-05：远程入口认证与有界读取

采用应用自有 `http.Server`，组合 mcp-go 的 Handler，不再用其无应用边界的 `Start`。顺序固定为：

```text
Header/读取时间预算 → 平台 Token 与身份验证 → 映射校验
→ 协议 session 归属/初始化预留 → 有界 body → MCP dispatch
→ 仅业务工具按需获取 Tenant/STS → 固定上游调用
```

- 所有远程入口均要求 `identity.enabled=true`、`require_identity=true`、非空 auth_token 和 namespace，不依赖网关开关。旧匿名/可回退部署启动报迁移错误，不能静默启用或降级。
- 平台 Token 使用常量时间比较；JWT 模式下 initialize、tools/list、通知、工具调用、SSE GET、HTTP GET/POST/DELETE 均必须带有效 JWT，缺失/无效不回退 Header。
- JWT 沿用 HS256、必需 exp 和精确数字解析；只允许已配置密钥，不根据 token 提供的地址获取密钥。Header 模式保留，但共享 Token 仅可由受信代理持有并覆盖身份头。
- 从 Registry 提取纯认证/映射步骤，将验证后的 Principal 放入私有 context；此步骤不创建 Tenant、不触发 SDK/STS 或恢复历史。工具 Resolver 只能消费此可信结果，不能再次采用弱回退。
- 在读取 body 前拒绝重复/冲突的身份头、超限头和非法方法。无认证返回 401，已认证但映射禁止返回 403，请求体超限 413，body 读取超时 408；错误不输出身份或秘密。
- 用 `http.MaxBytesReader` 限制实际读取，不只检查 Content-Length；覆盖 chunked 和省略长度。只接受支持的 Content-Encoding，不自动解压不受限入站 body。
- 路由 initialize 时使用经过完整语法校验的同一份有限 body，拒绝重复 method/id 等顶层键、尾随 JSON 和不支持的 batch，防止入口和依赖解析出不同生命周期。非法请求不得先创建 session 或发送业务调用。
- 读完 body 后清除读取 deadline，再交给 MCP；禁止给整个 SSE/55 秒等待套统一的短 ReadTimeout、WriteTimeout 或 TimeoutHandler。反向代理限额只能补充，不能替代应用入口。
- stdio 保持本地进程单身份，不应用远程 Header/JWT 要求或安装远程用户 Resolver；不把 stdio 描述为逐聊天用户隔离。
- 多租户远程启动不再提前恢复全局 Manager 的历史；共享基础 SDK provider 仅用于 AssumeRole。全局单身份历史恢复仅在 stdio 路径执行。

### 4.5 SEC-06：协议会话绑定与生命周期

绑定记录为 `(transport, session_id) → principal_id + 生命周期状态`，不使用原始 Token/JWT、角色或可伪造 Header 作为 owner。每次请求先认证，再与绑定比较；同主体换签名密钥/续期 JWT 可继续，其他主体即使共享 RoleArn 也不行。业务阻塞等待去重同样以 `(principal_id, cloud_session_id)` 为键，不让不同主体共享云端会话时互相触发 `duplicate_wait`；stdio 使用其单进程本地作用域。

**v0.32.0 可复用的实际接口：**

- SSE 使用 `SSEHandler()`；`WithHooks` 的 `AddOnRegisterSession` 在首次 endpoint 事件输出前同步取得 `ClientSession.SessionID()`，写入已预留容量的绑定。`AddOnUnregisterSession` 及外层 handler 退出共同执行幂等清理。无须解析/缓存 SSE 文本。实测 `MessageHandler()` 的 hooks 无法覆盖全部完成分支，因此 POST 采用应用最小适配：仍返回 202，通过公开 `WithContext` / `HandleMessage` / `SendEventToSession` 完成有界异步 dispatch，用无条件 defer 释放真实任务 lease；不 fork 或修改依赖。
- Streamable HTTP 使用 `ServeHTTP`、`WithHTTPContextFunc`；其 POST 不触发 RegisterSession hook，不能照搬 SSE 注册逻辑。应用在确认合法 initialize 后预留名额，在 context callback 中从 `ClientSessionFromContext(ctx)` 取得生成 ID，在 dispatch 前建立待确认绑定。
- 对初始化结果使用小型状态观察器：只有成功 initialize 才将待确认绑定变为可用；失败、写回失败、断连、超时均回收。已存在 ID 的 initialize 拒绝，不允许重新绑定。
- 通过 `WithSessionIdManager` 接入应用的有界签发记录，拒绝仅格式正确但未签发的 ID；Generate 没有请求 context，不能靠它完成身份绑定。HTTP GET 本身绕过该 manager 的 Validate，因此外层必须显式验证所有 GET/POST/DELETE。
- 包装 ResponseWriter 必须正确支持 `http.Flusher`、`Unwrap`/ResponseController 及实际使用的可选接口，不得缓存整条流或吞掉状态头。一次请求的状态观察需考虑依赖的通知写入并发。
- v0.32.0 的 SSE MessageHandler 在 context callback 之后调用 `context.WithoutCancel`，先返回 202 再异步 dispatch。不能认为 callback 中关联取消或外层 handler 返回就结束了工具任务；应用将独立的 session 生命周期/请求 lease 作为 context value 传递，在实际业务 handler 内重新关联可取消 context，并在进入业务前再次检查 session 是否撤销。
- 外层在允许 message POST 前预留有界 in-flight lease；通过实际 dispatch 的 hooks/业务 wrapper 关联完成通知，引用计数在任务真正结束时释放，而非 202 写回时释放。解析错误、未知方法、通知、取消与异常分支均须幂等释放；原始 HTTP context 正常结束不取消已接受任务，session 关闭/租约过期才取消可取消的等待与上游请求。若现有 hooks 无法覆盖某条完成路径，须完成最小适配或阻断 SSE 发布，不能用超时提前释放仍运行任务的名额。

**清理与并发规则：**

1. session 首次绑定必须在对外发布 ID 前完成；绑定与容量预留是原子的，容量不足在进入依赖前拒绝，不能先注册后补 owner。
2. 未知、过期、已删除、非本主体 ID 统一返回安全的 404，不暴露 owner；同一 HTTP session 同时最多一个通知 GET，重复连接拒绝而非共享队列。
3. SSE 断开撤销绑定，重新连接分配新 ID。HTTP 通知 GET 断开不等于 session 删除；只有 DELETE、空闲回收、认证租约到期或服务退出撤销。
4. DELETE 先阻止新请求、取消该 session 连接，再清理依赖的 sessionTools/request tracking 与应用记录；在途请求使用引用/取消管理，不能被重新归属。ID 不复用，不保留无界 tombstone。
5. session 与流均有容量及租约：HTTP 空闲且无在途请求才能回收；活动 SSE/通知连接最迟在当前 JWT exp 或连接最长租期到达时结束，客户端凭新凭据重连。Header 模式使用最长租期。秘密更换不构成云端撤权即时生效承诺。
6. 断连/DELETE/超时只取消本地等待和可取消 HTTP，不声称已经取消远端分析或 SQL；原有“发送结果未知，不自动重放”约束保留。
7. 不 fork 或修改 module cache；先使用已核对的公开扩展点实现。若实际回归发现公开接口无法保证 dispatch 前拒绝或完整清理，则阻断该 transport 发布，另行评审最小依赖升级，禁止带缺口上线。

### 4.6 SEC-10：初始化锁、SDK 与恢复生命周期

- Registry 全局锁仅保护 cache/in-flight 表，不在锁内 newProvider、GetCredential、打开会话目录或 RestoreSessions。同 principal_id 合并成一个初始化任务，不同主体可独立进行。
- `identity.max_tenants` 同时限制已缓存 Tenant 与初始化预留名额；先原子预留总量再创建 provider/根句柄，失败或停止后在工作真正退出、句柄关闭时释放。协议 session 关闭不意味着 Tenant 关闭，不能用 HTTP session 上限冒充租户/root/housekeeping 的总量限制。
- 首版不自动淘汰有历史 watcher 的 Tenant，也不新增 LRU 后台系统；达到总量上限时拒绝新主体初始化，已缓存主体仍可用。部署者需根据用户规模及 OS 文件描述符限额配置容量，扩容/受控重启另行操作；这一明确容量限制优于无限积累根句柄。
- `tenant(ctx, ...)` 的等待支持调用者取消；一个等待者取消不能取消其他等待者。初始化使用服务生命周期派生的独立 deadline，失败不缓存半成品。
- 同时初始化任务有固定上限，满额时安全返回忙碌，不为每个请求创建等待 SDK 的 goroutine。超时返回的 SDK 工作在真正结束前仍占用原槽位/in-flight 条目；不能靠超时后立刻再建任务突破并发限制。
- 当前 credentials-go 的 GetCredential 不接收 context。保留 SDK 默认凭证链、底层串行保护及 STS connect=5000ms/request=10000ms；不能声称 context.Cancel 已中止 SDK 网络或外部 provider。共享基础 provider 的刷新仍可能形成跨角色等待，须单列测试和发布限制。
- Manager 创建与网络恢复分开：安全目录和凭证初始化完成后只发布一次 Tenant，再以该租户生命周期启动一次有界恢复与 housekeeping；网关元数据调用不等待扫描全部旧会话。Restore/watch/send 继续依靠每个会话的操作锁协调，不能产生重复 watcher 或自动重复消息。
- Manager 恢复有并发限制和取消检查，不在停止后新建 watcher。服务停止时先撤销入口、取消恢复/会话、等待受控工作退出，再关闭根句柄；不静默丢弃 sending/unknown 状态。
- 网关严格取证失败仍拒绝，不切换基础 AK、其他角色或旧快照。旧分析同主体快照兼容未作为此轮新增修复项，不将其描述为与网关相同的严格刷新保证。

### 4.7 SEC-09：旧签名客户端禁止重定向

- 普通 Data Agent/DMS API 和出站 SSE 的生产 http.Client 均设置拒绝重定向策略，覆盖主 Client、Tenant Client 与重新建连；网关已有规则保留。覆盖 301/302/303/307/308、同域、跨域和 HTTPS 降级，不为“看似安全的同域”放行签名重放。
- 3xx 返回受控错误，不将响应 Location、签名 URL、Authorization 或 x-acs-security-token 写入结果/日志；不自动更换 endpoint 重试。
- 保留管理员已有合法 endpoint 配置能力，但不新增工具参数控制目标。测试注入用本地合成凭证，接收端断言零重定向请求，不发送真实 STS Token。
- 不扩大到 OSS 签名直传或用户下载流程；这些不是本次发现的签名 API 客户端路径。

### 4.8 SEC-08：单行、安全的审计日志

- 统一转义所有不可信动态字段，包括参数键和值、tool、caller、group、session ID 与错误摘要；采用标准 JSON/strconv 引号转义，不手写换行替换。键、值及整条记录均有输出预算，截断发生在转义前的有效字符边界且结果仍是单行。
- `formatArgs` 对完整原键先做敏感字段判断，再进行输出截断；嵌套敏感键同样脱敏。不得因键被截断而漏判秘密；限制嵌套深度与参数总输出，避免日志放大。
- 身份认证前仅记录生成的请求 ID、方法和固定拒绝类别，不记录用户声称的 caller 或 body。成功认证后从 Principal 获取身份，禁止解码未验证 JWT 用于审计。
- 旧工具 full 日志仍可能含业务问题，仅用于显式诊断；网关即使 full 也不记参数/结果正文。所有模式禁止凭证、签名 URL 和原始 SDK 错误；不能把长度限制当作脱敏。
- 保留 stdout 仅用于 stdio MCP、日志写 stderr、local/upstream RequestId 区分和网关单 JSON TextContent。

### 4.9 SEC-12：严格网关 JSON 与 Unicode

- 在解压后大小限制内，递归解析对象/数组；每一层对象对解码后的字段名去重，包括 `Data`、数组元素、EngineMeta 及未知字段。`"Name"` 与等价转义的键视为重复，即使两个值相同也拒绝。
- 在 JSON 字符串 token 层验证 Unicode：高代理项必须紧跟低代理项，低代理项不得单独出现；适用于键和值。合法非 BMP 字符、普通 UTF-8 与本来就合法的 U+FFFD 必须保留，不能用“拒绝所有替换字符”冒充检测。
- 保留完整语法/UTF-8/尾随数据检查和 `UseNumber`；不经 float64、不清洗 UUID/QualifiedName、不改变 NULL、数组顺序、int32/int64 与白名单。
- 限制嵌套深度为 64；超过上限或出现歧义返回 `GATEWAY_UPSTREAM_INCOMPATIBLE`、无 data、不重试，不先取一部分字段返回成功。解析深度和 4 MiB 限额都是本地安全约束。
- 安全 RequestId 仍可从已验证的 HTTP header 提取；歧义 body 中的 ID 不作为可信关联 ID。不放宽不存在数据源的 404、Data:null 等当前契约。

### 4.10 SEC-11：秘密文件与本机权限

- 优先通过现有环境变量/受管秘密注入平台 Token 和 JWT key，不新增秘密存储系统；继续从阿里云 SDK 默认链取得 AK/STS。
- POSIX 上检查实际加载且包含秘密的 YAML/JSON/.env：应为进程账户拥有、group/other 无访问位的普通文件；0600/0400 可用。公开占位示例或不含秘密的配置不因 0644 被一概拒绝。检查值只参与布尔判断，不输出原文。
- 检查在 dotenv/配置读取阶段完成，校验安全打开的文件句柄和父目录边界，避免“检查一个文件、读取被替换后的另一个文件”。本轮拒绝最终文件为符号链接的配置；容器投射秘密应改用受管环境注入或受控普通文件，不可盲目 chmod 只读挂载。
- 检查失败给出文件类别和迁移指引，远程启动拒绝；不由程序自动 chmod、chown、移动文件或改环境。Windows 必须验证等价 ACL 或明确阻断未经验证的文件秘密部署，不能用 POSIX mode 位宣称完成。
- 对现有本机配置的 0644 问题，实施部署步骤前单独获得管理员授权；收紧权限不代表秘密从未暴露，有访问/泄漏证据时另行授权轮换。Git 忽略规则保持，报告/测试只用假凭据。

### 4.11 SEC-07：工具链升级与产物验证

- 本轮最低构建工具链定为 Go 1.26.10（或更新的受支持且经复扫版本），相应更新 go.mod 的最低版本、构建检查及部署要求。升级提供根约束文件 API，也消除本次扫描已列标准库告警；不把改 go.sum 当作标准库修复。
- 构建和发布必须检查实际 Go 版本，不允许只改文档仍用旧本机或缓存二进制。分别重建 Server、dacli 和目标平台产物，检查 `go version -m` 并执行二进制漏洞扫描。
- 当前仓库未发现 Dockerfile 或 GitHub Actions workflow，不凭空增加 CI 平台；优先在 Makefile 和现有构建说明落地校验。若实际发布流水线在仓库外，由其维护者完成等价升级后才发布。
- mcp-go 保持 v0.32.0：入口漏洞由应用边界和公开接口最小适配修复。产物复扫另发现 `credentials-go → tea → golang.org/x/net/proxy` 引入的 x/net v0.20.0 告警，已将 x/net 最小升级到扫描建议的 v0.62.0；保留 SDK 默认凭证链，不顺带升级其他业务依赖。
- govulncheck 有告警导致非零退出与工具执行失败分别记录；JSON 模式退出 0 不是“无漏洞”。新告警必须评估真实可达前提，不能静默忽略或以单测通过替代。

## 5. 新配置与错误契约

以下本地契约已落实到配置解析与入口校验；未列出的网关、分组、STS 配置保持原义。示例不等于真实部署已经迁移，发布仍受第 7.1、8、9 节门禁约束。

| 配置/常量 | 初始值 | 约束 |
|---|---|---|
| `identity.namespace` | 无默认值 | 远程必填；稳定可信身份域，不由请求提供 |
| `identity.groups.<name>.tenant_id/agent_id` | 无默认值 | JWT 模式必填精确范围；Header 模式必须为空 |
| `identity.default.tenant_id/agent_id` | 无默认值 | 配置 default 且为 JWT 模式时必填；不允许跨范围兜底 |
| `identity.max_tenants` | 128 | 正整数；cache 加初始化预留总量，不以协议连接释放代替 Tenant 释放 |
| `http.max_request_bytes` | 1048576 | 正整数；覆盖所有 MCP POST，包括通知；超限 413 |
| `http.max_header_bytes` | 65536 | 正整数；按 Go HTTP 头解析语义执行，含实现固定余量的实测边界须记录 |
| `http.read_header_timeout_seconds` | 5 | 正整数；不覆盖整个流生命周期 |
| `http.read_body_timeout_seconds` | 10 | 正整数；仅从读 body 开始至读取完成 |
| `http.idle_timeout_seconds` | 60 | 正整数；仅空闲 keep-alive，不是活动 SSE 时限 |
| `http.max_sessions` | 4096 | 进程总协议 session 数上限，包含初始化预留 |
| `http.max_sessions_per_principal` | 64 | 不大于总量；匿名请求不能占名额 |
| `http.max_inflight_requests` | 256 | 所有已接受但未实际结束的 dispatch 总量，包含 SSE 异步任务 |
| `http.max_inflight_per_session` | 8 | 不大于总量；HTTP 202 或等待取消不提前释放运行任务名额 |
| `http.session_idle_timeout_seconds` | 1800 | HTTP 无连接、无在途调用的空闲回收时间 |
| `http.max_stream_lifetime_seconds` | 3600 | 活动 SSE/通知连接最长租期，JWT 模式还受 exp 限制 |
| 租户初始化并发/等待预算 | 8 / 15 秒 | 首版内部常量，可在测试注入；不是 SDK 可取消或整体请求时限承诺 |
| 恢复任务并发上限 | 4 | 首版内部常量，独立于入口协议连接数 |
| JSON 深度 / 单条请求日志输出预算 | 64 / 8 KiB | 首版内部常量，超限拒绝/受控截断，不新增运行开关 |

- 数值配置显式 0/负数报错，缺省才应用默认值；实现需区分缺省与显式零，避免“0 关闭安全限制”。所有 duration 转换必须检查溢出。
- 新增 HTTP 配置仅作用于远程传输；`MCP_TRANSPORT`、端口和路由沿用原配置。禁止加入 `allow_unsafe`、`legacy_identity_fallback` 等开关。
- 容量满返回 HTTP 429，已认证业务初始化忙碌/超时使用安全的可重试错误；调用者取消不触发后台重试。身份冲突/未知 session/非法 ID 不自动重试。
- HTTP 层尚未进入 MCP dispatch 的错误不伪造工具 envelope；进入网关 handler 后继续使用已有 GATEWAY_* JSON 契约，不返回部分 data 或原始错误。

## 6. 实施批次与依赖

| 批次 | 内容 | 依赖 / 可交付边界 |
|---|---|---|
| R0 | 固化合成失败用例、记录扫描版本，升级 Go 并验证根约束 API | 只建立本地复现基线；先验证现有代码确实失败，再改成修复后断言 |
| R1 | SEC-01/02/03/04：受限存储、Principal/namespace、目录布局、确定性分组 | 依赖 R0；旧历史保持离线，不能以未迁移为由开启回退 |
| R2 | SEC-05/06：全部远程方法先认证、有界 body、协议 session 绑定与生命周期 | 依赖 R1 的 Principal；主/Tenant 接线和 dacli 三传输共同回归 |
| R3 | SEC-10/08/09/12：初始化并发、日志、旧重定向、严格网关解析 | 依赖 R1；日志/解析/出站子项可独立测试，不放宽 R2 发布门禁 |
| R4 | SEC-11 部署迁移、权限确认、全量/二进制扫描及真实云端验收 | 依赖 R1–R3；真实配置/权限/密钥操作均需单独授权 |

R0/R1 完成不代表可以对外上线；R2 不能单独以“认证通过”替代文件隔离。所有 P0 和适用 P1 的验收完成后才形成安全修复发布候选。

### 实施落点

- session：修改 manager/state/watcher 与现有测试；如需新增文件，仅新增实际共享的受根约束存储实现，不建设额外通用抽象层。
- tenant/config：修改 Claims/认证拆分、Registry 初始化、ResolveGroup、配置校验及测试；更新远程 require_identity/namespace 和 JWT 信任域说明。
- mcp/main：自有 HTTP server/生命周期、会话绑定、结果图片接口和入口认证接线；更新 sessionManager 接口、fake 实现及 dacli 端到端测试。
- dataagent：旧 client/SSE redirect 策略、网关严格 JSON 解析及对应负例；不重写签名或默认凭证链。
- 构建/文档：go.mod/必要 go.sum、Makefile、Server README、示例配置、MCP Skill 安装与 RAM 说明随实际实施同步；不能在实施前把提案配置写成现有能力。

## 7. 验收矩阵

| 范围 | 必须通过的自动化断言 |
|---|---|
| 路径输入 | 空值、`..`、相对/绝对路径、双分隔符、Windows 路径/设备名、控制符、超长 ID 全部拒绝；编码后的路径串不解码成路径；锁/map/云端调用计数不增长 |
| 存储边界 | 两租户合成状态/图片不可互读写；状态内 ID 不符、tenant/session/image symlink、目录替换均拒绝；合法持久化、原子替换、重启恢复、图片输出保持 |
| 身份键 | `a+b`/`a_b`、user_id/email 同字符串、不同 namespace/tenant/agent/auth_mode、大小写/Unicode 不混同；同 Principal 多密钥轮换稳定，数值 ID 无精度损失 |
| 迁移 | 新目录缺失不回读旧目录；无 owner/错误 owner/非空目标不自动认领；含多个旧用户的碰撞目录只能隔离保存 |
| 角色映射 | 单组双字段命中成功；跨组匹配始终拒绝，不因 map 遍历改变；同号异 tenant/agent 不命中他域组或 default；default 仅用于同范围零匹配；旧 JWT 配置缺范围拒绝启动；缓存指纹不一致拒绝 |
| 入站认证 | HTTP/SSE 的 initialize/tools/list/tools/call/notification/GET/DELETE，网关开与关，分别测试缺/错平台 Token、缺/错/过期 JWT、缺 tenant/agent、伪造身份头、未映射用户；拒绝时零 dispatch、零 Tenant/STS |
| 请求限额 | 最大值前/等于/后、Content-Length/chunked/无长度、慢头/慢 body、小而深的 JSON；使用有限本地 fixture，不向真实服务进行压测 |
| SSE 绑定 | A 建连接，匿名/B 持 A ID 的 message 被拒绝，A 通道无新增事件；同角色 A/B 同样隔离；202 后任务仍占名额，POST 结束不误取消任务，GET 断开/租约到期与异步 dispatch 交错时及时取消且不再新发上游；所有退出分支释放 lease |
| HTTP 绑定 | 未签发的合法格式 ID、异主 GET/POST/DELETE、重复 initialize、并发通知 GET、失败初始化、容量满、空闲清理、DELETE 与在途调用交错；无重新归属、泄漏或残留可用 ID |
| 长连接回归 | body deadline 清除后等待超过 55 秒、SSE 持续通知正常；不以缩短旧 wait 上限掩盖 timeout；包装器保留 flush、headers、取消传播 |
| 初始化 | 同主体一次 SDK 初始化；慢 A 不阻塞 B 的 cache 查找；取消等待及时返回，实际 SDK 阻塞数始终不超过上限；晚返回不发布失效 Tenant，不重放消息；共享基础 provider 慢刷新单列观测 |
| 资源总量 | 连续创建并关闭不同主体协议 session，Tenant/root/housekeeping 仍受 max_tenants 约束；额满新主体拒绝、旧主体可用；初始化失败与服务退出回收预留和句柄，SSE/HTTP 实际在途数不超过各自及全局上限 |
| 日志 | 键/值/tool/caller/错误含 CR/LF/控制符、嵌套假秘密、超长键及 body：每条仍单行、有界、不泄密；拒绝请求无伪造 caller；gateway full 仍无参数/数据正文 |
| 重定向 | API/SSE 对所有 3xx 的同域/跨域/降级目标均零转发；错误不带 Location、签名或合成 Token；网关原测试继续通过 |
| 网关 JSON | 所有层级重复键、转义等价键、孤立高/低代理项、错误代理对、过深嵌套拒绝；合法代理对/U+FFFD/NULL/大整数/小数精确；无 partial data |
| 权限与退出 | 0600/0400 秘密文件可用，0644/错误所有者按平台拒绝；不自动改权限；退出关闭连接/恢复任务/根句柄；sending/unknown 状态不丢失 |
| 全功能回归 | 19/28/30 工具、P2b 默认关闭、原 19 工具合法 Schema/结果、会话新建/等待/继续/确认、签名 SSE、OSS 直传/回调、API Key 旧配置拒绝、stdio/HTTP/SSE 及 dacli |

实施后至少运行以下检查，并记录工具版本与实际结果：

```bash
go test ./...
go test -race ./...
go vet ./...
go mod verify
govulncheck ./...
```

另对路径、网关 JSON 和身份键做限定时长 fuzz；对构建后的 Server/dacli 做 `govulncheck -mode=binary`。安全回归用例应断言“拒绝攻击/保持边界”，不能保留以“成功读到其他租户”为 PASS 的审计复现断言。测试、fuzz 和 mock 均只使用合成秘密/数据。

### 7.1 真实云端门禁

1. 经授权在独立二进制及隔离的会话根运行，记录实际工具链、地域、身份类型、角色、请求分类和 RequestId；不把业务正文或秘密写入仓库。
2. stdio 使用进程 SDK 身份；HTTP/SSE 使用真实 STS，验证完整 9 元数据工具及显式开启 P2b 时的 2 工具、分页和拒绝，确认鉴权入口没有误伤协议握手。
3. 真实 JWT 平台需覆盖 initialize/持续 SSE/通知/DELETE 的身份头携带及续期；仅本地签 JWT 不算平台验收。平台无法为某方法带认证时阻断接入，不按方法豁免身份。
4. 准备明确的“A 可见、B 不可见”资源与预期表；验证列表不可枚举、用 A 已知 ID 请求详情仍拒绝，以及失败后不回退角色。若授权环境未准备好，该门禁保持未完成，不修改策略凑结果。
5. 同角色不同用户单独验证本地历史和 MCP 通道隔离；云端共享范围必须据真实授权声明，不能把同角色共见资源记录为本地修复失败，也不能称为逐用户云端隔离成功。
6. 回归旧 Data Agent 的只读分析及多轮追问；其成功只证明旧链路回归，不证明网关 SQL/行列保护。取证失败、重启和取消不得重复发送业务消息。
7. 对共同拒绝而缺少上游 RequestId 的样本保留“未归因”，补证可用 header/云端审计后再判定授权层；不伪造 ID、不将拒绝变空数组。

## 8. 发布、迁移与回滚

- 发布前列明破坏性变更：Go 最低版本、远程强制身份/namespace、JWT 不降级且必需 tenant/agent claims、组/default 的精确范围、协议 ID 绑定/租约、identity-v2 历史布局、请求及租户容量限制、秘密文件要求。客户端需在所有请求携带认证并支持重新 initialize。
- 经管理员确认后停写旧实例、备份配置/会话、部署新二进制、使用新身份空间验证，再决定是否迁移可证明归属的历史；不能先把新版本接入生产用户再验证隔离。
- 保持 P2b 默认关闭及所有云端角色/权限不变。业务确需增权、文件 chmod、密钥轮换、旧数据迁移、代理调整、发布或回滚时分别获取授权；本 Spec 不授予这些操作权限。
- 回滚到存在漏洞的二进制不能重新暴露多用户入口。必要时停止远程服务或仅保留受控 stdio，旧二进制和新存储不混写；不删除 identity-v2 或覆盖备份。
- 完成报告分别列出：已修复与测试证据、扫描告警及适用性、未完成的真实平台/差异授权门禁、操作人员执行的部署项。缺少门禁不得标记“生产隔离通过”。

**完成定义：**全部风险有对应修复或经部署边界证明不适用，P0/P1 自动化与产物扫描通过，目标部署的真实平台/角色验收完成，迁移不引入共享历史或弱认证回退。只写完本文、只通过单测或两个角色都调用成功，均不构成修复完成。

## 9. 实施与验证记录（2026-11-02）

### 9.1 已落实的本地修复

| 范围 | 实现与回归证据 |
|---|---|
| SEC-01 | [受根约束存储](internal/session/storage.go)、Manager 全入口及工具 Resolver 前 ID 校验、状态 ID 一致性、图片字节接口、0700/0600 原子落盘、关闭后不再恢复；补充不存在 ID 的锁回收与同 ID 并发串行测试 |
| SEC-02/03/04 | [Principal](internal/tenant/principal.go)、[精确范围映射](internal/config/security.go)、可信认证 context、identity-v2 归属校验，无旧目录或弱身份回退；跨域/类型/Unicode/分组冲突测试 |
| SEC-05/06 | [应用 HTTP 入口](internal/mcp/http_transport.go)、[协议归属与真实完成租约](internal/mcp/http_sessions.go)、有界读取、失效及 DELETE 清理、SSE 最小适配；56 秒长等待、202 后实际占位、同角色跨用户通道测试；业务等待按 Principal 隔离 |
| SEC-07 | Go 1.26.10、x/net v0.62.0、Makefile 版本核查；Server 与 dacli 的原生及 Linux 双架构产物重建；不再 UPX 打包 |
| SEC-08/09/12 | 请求日志递归脱敏/单行/预算、仅可信 Principal 审计、会话与 SSE 日志不含原始上游错误或正文；旧签名 API/SSE 拒绝重定向；网关递归重复键/Unicode/深度与精确数值校验 |
| SEC-10/11 | 缓存及初始化总量、8 个真实初始化槽位、4 个恢复槽位、等待可取消且 SDK 未退出不释放配额；秘密文件安全句柄/所有者/权限检查；真实文件权限修改不在已执行项内 |
| 接线与文档 | 远程不启动全局历史 Manager；stdio 响应服务取消；未知 transport 拒绝；dacli 全生命周期携带 JWT/平台 Token、JWT 不混发身份头、认证 DELETE、禁止重定向；同步示例与 Skill/安装说明 |

### 9.2 本地验证记录

- 工具链：Go 1.26.10；扫描器：govulncheck v1.9.0。保留 mcp-go v0.32.0 与 credentials-go v1.4.3。
- x/net v0.62.0 升级后的最终复验通过：`go test ./...`、`go test -race ./...` 的 8 个包均通过，`go vet ./...` 无诊断，`go mod verify` 输出 `all modules verified`，`git diff --check` 通过。
- 最终依赖版本的 6 类限定 fuzz 均复验通过：会话 ID、Principal、HTTP envelope、网关 JSON、Unicode 代理项、解码后重复键；每类 `-fuzztime=3s -parallel=2`，仅使用合成数据。
- Server/dacli 的 darwin/arm64、linux/amd64、linux/arm64 共 6 个产物均按 `-trimpath -ldflags '-s -w'` 重建，并记录 `go version -m` 与 SHA-256；升级后的源码及全部 6 个产物扫描均明确输出 `No vulnerabilities found.`。此结论限于扫描器及当次数据库覆盖范围，不等于系统无安全风险。
- 初次源码扫描为 0 符号可达告警，但 x/net v0.20.0 在发布参数二进制扫描中输出 21 条告警；已据此升级依赖。不能只看外层任务退出 0，也不把“扫描已运行”写成“扫描通过”。
- 所有日志和构建产物留在本机隔离的临时验证目录，未部署、提交或推送；原始业务数据和真实秘密未写入这些测试。

### 9.3 尚未完成的发布门禁

1. **历史进程终止原因：**先前初始化失败及最小无凭据进程 SIGKILL（-9）的来源仍未确认；不归因为云端权限拒绝或直接断言由 EDR 导致。此次同路径、同哈希最终原生产物已独立启动并完成第 9.4 节复验，当前启动阻塞不再复现；未删除安全属性、停用安全软件、重签或改变执行路径。
2. **真实身份平台：**缺少真实 JWT 平台全方法携带身份及续期证据；此次临时 Header 身份、随机平台 Token 与真实 STS 测试不是平台 JWT 验收。
3. **差异授权/最小权限：**缺少明确的 A 可见/B 不可见资源及预期表；历史双角色集合一致和共同拒绝未归因的结论不变。同角色本地历史与通道隔离通过不等于云端差异授权通过，不修改角色或提升权限凑结果。
4. **真实部署操作：**现有含秘密的 0644 配置未修改；权限修复、密钥轮换、离线历史迁移、生产部署及回滚须另行授权，不能把临时安全配置启动成功当成真实部署操作已经完成。
5. **目标平台与业务回归：**Linux 产物构建/扫描不等于 Linux 运行验收；Windows 未验证 ACL/存储按实现拒绝。此次旧分析验证限于一个常量只读探针及同会话追问，严格固定标记校验未通过，不能据此宣称完整业务回归或所有输出要求通过。

### 9.4 最终产物端到端复验（2026-11-02）

- **产物与身份：**使用第 9.2 节已扫描的 darwin/arm64 Server，SHA-256 `986bd81dcdcfc20077418768f305d587fcaa58267983389d0a03913ca78629e9`，未重建或替换；stdio 使用进程 SDK 身份，HTTP/SSE 使用临时 Header 身份与已配置 `da-role` 的真实 STS。临时配置为 0600、每次使用随机平台 Token 和隔离会话根；未修改真实配置、角色、策略或部署。

| 验证 | 实际结果 |
|---|---|
| stdio / Streamable HTTP / SSE × off / base / full | 9 个独立进程场景全部通过，注册数分别为 19/28/30 |
| 网关真实元数据 | 每种传输分别覆盖全部 9 个基础工具与显式启用的 2 个摘要工具；分页游标、过滤、无匹配、非法参数拒绝、不存在摘要详情与字段白名单通过 |
| 旧工具真实发现 | 每个场景的 `data_agent_list_workspaces(type=ALL)` 返回 264 项并附上游 RequestId；原生 dacli 的 `workspaces`（默认 MY）返回 2 项 |
| 远程协议边界 | HTTP/SSE 的网关关闭及全开共 4 场景通过；缺/错 Token、跨用户持有他人协议 ID、非法会话 ID 拒绝；拒绝及纯协议请求未生成租户存储目录 |
| 生命周期 | HTTP 已认证 DELETE 返回 200，删除后请求返回 404；SSE 关闭后旧 ID 失效；测试进程可正常退出。GET 通知流按保留的 mcp-go v0.32.0 行为返回 202 与 `text/event-stream` |
| 同角色本地历史 | 只读挂接本次已创建的云端会话：A 可列举 1 条并读取结果，B 本地历史为 0 且持已知 ID 读取结果被拒绝；未重新创建或发送消息 |

- **真实请求示例：**Streamable HTTP/full 网关成功请求 `01A19AE4-3583-580A-A941-61B9AC931C8F`，SSE/full 成功请求 `01A19AE4-4DEE-526F-BB25-6ED552DF079C`；仅记录请求标识与统计，不落业务正文。
- **旧分析及追问：**默认空间没有已导入库，正常发现本人空间后选取实时列表中的库/表，创建前再次刷新库元数据。仅创建一次会话，要求只执行 `SELECT 1 AS e2e_probe`，首轮 completed、checkpoint 217、1 条结论；只发送一次“不重新查询、依据上轮结果回复并包含 FOLLOWUP_OK”的追问，第二轮 completed、checkpoint 883、2 条结论。
- **严格标记校验未通过：**第二轮 `get_result` 没有 `FOLLOWUP_OK`，保留原始失败结果。随后仅以相同角色只读重放该会话 SSE 取证，没有重发查询/追问：标记存在于第二轮 `llm` 内容（checkpoint 745/806），不在上游正式 `task_finish`（880）；两轮正式结论（212/880）均包含 `e2e_probe` 的值 1。服务端按正式结论优先于 LLM 备用输出的契约取值；这不证明 SQL 未执行或追问未送达，也不满足固定标记输出要求。该只读 SDK 取证用于解释差异，不能替代最终二进制的端到端结果断言。
- **验证脚本修正：**初次把旧工具附加的 RequestId 元数据块误判为错误；实际没有 tool error。mcp-go 测试客户端的 Close 异步发送 DELETE 且不携带配置的身份头，初次随即关闭进程造成连接重置；临时测试传输补齐逐请求认证并等待 DELETE 完成后复验通过，没有放宽服务端认证。未为消除标记失败而重新提交业务消息或改写通过断言。
- **证据：**本机临时验证目录中的 `retry-e2e-matrix-corrected.log`、`retry-e2e-protocol-corrected.log`、`retry-e2e-history-isolation.log` 为通过记录；`retry-e2e-analysis-workspace.log` 保留严格标记失败，`retry-e2e-analysis-final-evidence.log` 为后续只读事件分类证据。临时测试通过 Go overlay 加载，未写入仓库业务代码；发布前仍须按实际平台、身份与资源补齐第 9.3 节门禁。

**结论：本地安全实现及上述限定端到端验证已交付，严格输出校验和完整 Spec 的发布完成定义尚未全部满足；不得标记生产隔离通过。**

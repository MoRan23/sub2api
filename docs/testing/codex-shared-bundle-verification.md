# Codex 共享票据包与账号授权恢复验证

## 原账号调度恢复（2026-09-22）

根据用户进一步确认，`d62c56269` 的摘要布尔修复并未完全恢复原调度；它仍保留了多系统凭据引入的第二道授权准入。本次直接对照 `a5411ee97` 的前一个版本 `41501a3de`。以下四个生产文件恢复后与该历史版本没有差异：`openai_account_scheduler.go`、`openai_gateway_scheduling.go`、`openai_ws_forwarder_support.go`、`openai_profit_control.go`。同时移除插件目录的凭据过滤、Redis 授权摘要和相应专用 503 分支。

OS 只在选中账号后分配身份；账号 token 缓存、刷新条件、401／403 与测试恢复使用原账号策略。实际凭据仍由 `accounts.credentials` 提供，保留的 generation/revision CAS 只防迟到写入，不以旧系统授权状态决定资格。旧 metadata 中的布尔字段被忽略，正常快照无需强制失效或重建。没有新增数据库迁移；此前 256、257 仍是共享凭据与 HTTP 票据包的基础。

隔离 PostgreSQL／Redis 集成回归在 WSL Ubuntu 24.04 执行，**155 项测试及子测试通过，无跳过**，包含当前凭据刷新、旧授权状态不干扰 CAS、撤销无 token 复活、完整账号错误与冷却、测试恢复、授权错误持有的暂停与重新授权恢复、人工暂停保留，以及迟到版本不覆盖新授权。只使用合成数据和本地容器。

```sh
CI=true go test -race -tags integration ./internal/repository \
  -run '^Test(AccountRepoSuite|OpenAIOAuthMetadata|OAuthOSCredentialMigration|OpenAIOAuthShared|OpenAIOAuthRuntime)' \
  -count=1 -json
```

初次集成检查修正了两个旧断言：同一冷却截止时间不能替换既有原因，测试需设置更晚截止时间再验证原重试筛选；撤销后的身份解析不再替代 token 校验，因此验证改为“新快照凭据为空、原 token provider 拒绝、旧请求代次失效”。测试过程中曾因并行修改测试名称造成编译中间态，最终统一检查以文件稳定后的结果为准。

账号调度、刷新、连接测试、插件目录、候选快照和错误分类的合并单元回归 **868 项测试及子测试通过，无跳过或竞态报告**：

```sh
go test -race -tags unit ./internal/service ./internal/repository ./internal/handler \
  -run '^Test(OpenAIScheduling|OpenAIOAuth|OpenAIOS|OpenAISharedAuthorization|OpenAIAccountScheduler|DefaultOpenAIAccountScheduler|Scheduler|FilterScheduler|AccountRepository_ListOAuthRefreshCandidatePage|ClassifySelectionFailure|OpenAIPlugin|OpenAIToken|TokenRefreshService|OpenAITokenRefresher|OAuthRefreshAPI|RateLimit|HandleUpstreamError|RecoverAccount|OpenAIAccountState|OpenAIGatewayService_(SelectAccount|RecheckSelected|OpenAIAccountSchedulerMetrics)|AccountTest|OpenAIRefreshCredentials)' \
  -count=1 -json
```

调度回归包括普通／高级调度器、粘性会话及三系统／未知系统组合；使用没有 token 和系统授权信息的脱敏候选，确认它们按原账号条件进入选择，不再由测试夹具自动补授权来绕过问题。另验证空 token 仍由原 token provider 在发送阶段报错，不因此发送空凭据。

新增同请求身份冻结回归：母账号及 Spark 在默认系统改变或 UA／安装 ID／同步根重生成后，重试、首次计划生成及凭据刷新均保留原请求身份，新 Gin 请求使用新配置。身份准备不读取授权状态，不修改原账号 token。上述定向身份检查通过后，再运行 HTTP／WebSocket 相邻转发回归。

转发检查还修正了旧测试夹具：遥测使用完整、已持久化的三系统身份及账号凭据投影；撤销必须实际清除账号凭据并推进代次，不能只依赖旧私有状态字段。票据准备在凭据已撤销时返回不启用的被动结果，真正的空 token 拒绝继续由 token provider 负责。

相邻回归中保留两项已确认的既有失败，不扩大本次修改范围：

- `TestOpenAIHTTPPassthroughStripsOnlyOAuthLegacyResponsesBeta`：API Key 路径的实验 beta 头保留断言，已在此前交付的未修改基线复现，见本文末尾历史记录。
- `TestOpenAIWSHTTPBridgeAcceptsFirstFrameAboveLegacy16MiB`：本次在隔离的 `d62c56269` 源码副本再次复现；默认 Codex 指令使转发正文长于测试的“消息上限＋4 KiB”断言（`17848623` 与 `17829888`），此前桥接、响应和用量断言均通过。该测试是 API Key 路径，与本次 OAuth 账号选择无关。最终定向回归明确排除这两项，不将它们计为通过。

最终相邻转发回归 **630 项测试及子测试通过**，无新增失败或竞态报告，明确排除上述两个基线失败：

```sh
go test -race -tags unit ./internal/service \
  -run '^Test(OpenAIWS|OpenAI.*WebSocket|OpenAIHTTPOAuth|OpenAIOAuthRequestScope|OpenAIOAuthIdentity|OpenAIOSIdentity|OpenAISharedAuthorization|CodexTelemetry.*Identity|CodexTelemetry(Persistent|SharedGrant|OSAuthorization|ProfilePersistence)|OpenAIHTTPPassthrough|OpenAIStreamingTerminalAndClientCancellation|CodexTurnState.*(HTTP|WS|Shared|OSCache))' \
  -skip '^Test(OpenAIHTTPPassthroughStripsOnlyOAuthLegacyResponsesBeta|OpenAIWSHTTPBridgeAcceptsFirstFrameAboveLegacy16MiB)$' \
  -count=1 -json
```

根据用户追加问题，单独核对 TLS：内置 OAuth HTTP 的真实 RoundTrip 根据最终 UA 选择三系统 `codexnative` 模板；连接池键包含模板摘要、账号、代理和用途，WS→HTTP 桥接沿用该链路。遥测保留 exporter UA，TLS 选择使用冻结的业务 UA。模板仍是已有 `codex-cli-0.154.0-native-20260917-v1` 采样配置，本次不更新握手参数。**原生 WS 仍使用标准 TLS，没有接入三系统模板**；其连接虽然按最终身份摘要隔离，不能因此声称 TLS 指纹与系统一致。

补充 TLS、HTTP 实际握手、CONNECT／SOCKS 代理、遥测 scope 与标准 WS 拨号回归 **116 项测试及子测试通过**，无跳过、失败或竞态报告；所有连接均为本地模拟端点：

```sh
go test -race -tags unit ./internal/pkg/codexnative ./internal/pkg/httpclient ./internal/service ./internal/repository \
  -run '^Test(Native|OpenAINativeHTTP|OpenAINativeReq|WithOpenAINativeHTTPScope|OpenAINativeHTTPConcurrent|CodexTelemetryNativeHTTP|CodexAuxiliaryTransport|CoderOpenAIWSClientDialer|TLSFingerprint|CodexTurnStateCollectorNativeTransport)' \
  -count=1 -json
```

本次未修改前端，未重复前端构建或浏览器验收，未运行全仓所有后端测试；未部署或调用真实业务、采集、授权、遥测端点。无关 `pelican-bicycle.html` 保留。

## OAuth 调度摘要回归修复（2026-09-22）

本节为 `d62c56269` 的历史验证记录。其新增摘要判定已由上面的原调度恢复取代。

`f7683086e` 恢复账号级凭据后，候选准入直接检查了 token，但 Redis 调度候选使用刻意去除 token 的 `sched:meta` 摘要，导致正常 OAuth 账号在加载完整凭据前被误判为不可用。账号页面的正常状态不受影响，因此可能出现全部候选被排除并返回 `No available accounts have usable OpenAI OAuth authorization`。之前的定向回归没有包含仓储调度摘要测试，漏掉了该路径。

修复在摘要生成前计算账号凭据是否存在，只向摘要增加布尔值；完整账号与实际发送仍校验账号最新凭据。旧摘要缺少该字段时返回缓存未命中，使用现有受控数据库回退和快照重建，不将旧系统授权标记当作凭据，也不向摘要写入 token。不需要新增数据库迁移或重新授权；服务端必须更新到包含此修复的版本。

以下本地 WSL 检查 **139 项测试及子测试通过**，无失败、跳过或竞态报告；服务包 1.181 秒、仓储包 1.428 秒。覆盖普通与高级调度器、三系统共用授权、Spark 使用母账号、完整凭据加载、撤销后旧摘要不能放行，以及 miniredis 真实缓存读写、敏感字段剔除、旧摘要重建和 API Key／PAT／Agent Identity 豁免。

```sh
cd backend
go test -race -tags unit ./internal/service ./internal/repository \
  -run '^(TestOpenAIOAuthSchedulerMetadata|TestOpenAISharedAuthorization|TestOpenAIOSAuthorization|TestOpenAIOAuthAccountCredentials|TestSchedulerSnapshot|TestSchedulerCache|TestSchedulerMetadata|TestBuildSchedulerMetadataAccount|TestMarshalSchedulerCacheAccount|TestFilterSchedulerCredentials)' \
  -count=1 -json
```

本次是后端候选缓存修复，没有修改前端或数据库结构，未重复前端检查及 PostgreSQL 集成测试；未部署、未调用真实业务或授权端点。该验证证明代码回归及修复，不代表已检查线上账号的实际凭据状态。

## 后端与隔离存储验证（2026-09-22）

Go 检查在本机 WSL Ubuntu 24.04 中运行。HTTP、原生 WebSocket 和插件发送使用合成请求、内存服务或本地模拟传输；测试没有请求真实业务、采集、授权或遥测端点。

隔离 PostgreSQL 18.1 / Redis 8.4 由测试容器创建并清理，执行：

```sh
cd backend
CI=true go test -race -tags integration ./internal/repository \
  -run 'Test(AccountRepoSuite|CodexState|CodexHistory|CodexCollectorProxy|OpenAIOAuthMetadata|OAuthOSCredentialMigration|OpenAIOAuthShared)' \
  -count=1 -json
```

244 项测试及子测试通过，无跳过，14.610 秒。覆盖单一账号凭据源、撤销不复活、未授权状态迁移、授权暂停来源、刷新及重新授权 CAS、共享模型键、整包原子发布、配置和授权代次防护、旧记录归并、重启恢复与跨实例互斥。

服务端依赖注入编译检查 `go test ./cmd/server -run '^$'` 通过。Cookie 包、插件边界与 HTTP 实际发送链的定向 race 检查通过，包括开关关闭、完整目标包保存、失败不发布、同票不延寿、响应模型证据随包提交，以及拒绝包时同时恢复原请求头和正文载体。

合并回归命令如下。两项在基线复现的旧测试先完整运行并记录失败，再从最终相关回归中明确排除；具体归因见本文末尾。

```sh
go test -race -tags unit \
  ./internal/service ./internal/handler/admin ./internal/repository ./internal/pkg/openaicookies \
  -run 'Test(Codex(TurnState|WSState|State)|OpenAICookie|Cookie|Bundle|Attempt|OpenAIHTTP|OpenAIReq|OpenAIOAuth|OpenAIOS|OpenAISharedAuthorization|OpenAIAccount|OpenAITokenProvider|OAuthRefreshAPI|TokenRefreshService|OpenAITokenRefresher|AdminBindOpenAI|ApplyOAuth|CodexAuth|CodexImport|AccountData.*OpenAI|AccountRepo.*OpenAI|GetCodexTurnState|PluginTurnStateMerge|OpenAIPluginCookieBundle|OpenAIGatewayPluginRouting|PluginManagerRouting|OpenAIStreamingTerminalAndClientCancellation)' \
  -skip '^(TestOpenAIHTTPPassthroughStripsOnlyOAuthLegacyResponsesBeta|TestOpenAIRequestBodyLimitFailover_HTTP413SwitchesAccountsBeforeWrite)$' \
  -count=1 -json
```

最终所选范围 **1812 项测试及子测试全部通过**，没有新增失败或竞态报告；服务包耗时 27.500 秒，另外三个包均通过。原生 WS 的直连、连接池、透传及 HTTP→WS 路径验证为完全旁路票据，不读取或修改共享包，不更新活动或触发采集；实际 WS→HTTP 桥接和其他 HTTP 入口继续经过票据包边界。HTTP 成功发布测试必须捕获真实模拟响应的 Cookie 并观察到成功终态，只有下游交付而缺少终态时拒绝发布。

本轮未运行全仓后端所有测试或实际部署。升级前必须排空旧进程，备份数据库后应用迁移 256、257；旧票据与 Cookie 会清空重采，合法账号授权及三系统身份保留。

## 前端验证（2026-09-22）

基线 `dev@f18e7212e`。检查只使用合成账号、模拟接口和本地前端；未调用真实业务、采集、授权或遥测端点。

### 自动检查

在 `frontend` 目录执行：

```sh
pnpm exec vitest run \
  src/components/account/__tests__/OpenAIOAuthOSProfiles.spec.ts \
  src/components/account/__tests__/EditAccountModal.spec.ts \
  src/components/account/__tests__/CodexTurnStateFields.spec.ts \
  src/components/admin/account/__tests__/CodexTurnStateStatusModal.spec.ts \
  src/components/admin/account/__tests__/CodexTurnStateStatusModal.diagnostics.spec.ts \
  src/components/admin/account/__tests__/AccountCodexTurnStateCell.spec.ts \
  src/components/admin/account/__tests__/CodexResponseEvidenceDetails.spec.ts \
  src/api/__tests__/admin.accounts.codexTurnState.spec.ts \
  src/composables/__tests__/useCodexTurnStateBatch.spec.ts \
  src/i18n/__tests__/localeKeyCompleteness.spec.ts
pnpm run typecheck
pnpm run build
```

- 10 个测试文件、212 项测试通过。包括旧授权面板移除、三系统身份控制保留、观测系统切换不清空共享缓存、Cookie 提前到期、定时刷新与错误退避分开、Cookie 诊断白名单与秘密字段不渲染。
- `typecheck` 通过；15 个变更 Vue／TypeScript／语言文件的 `pnpm exec eslint` 检查通过（未执行自动修复）。
- 生产构建通过，同时再次执行 3 项语言键完整性测试。构建保留现有 Browserslist 数据过旧、Node `DEP0190`、模块混合静态／动态导入及大分块提示，未出现构建错误。
- 本轮未运行全量前端测试或全仓 lint；已覆盖变更组件、相邻编辑流程、接口封装及批量刷新。

追加授权暂停审阅后，普通账号编辑仅在状态相对打开弹窗时发生变化时提交 `status`，避免名称或配置编辑夺取后台 OAuth 失败的暂停来源。新增覆盖初始正常／错误状态的仅改名称、明确改状态及改后复原。再次运行 `EditAccountModal.spec.ts`（81 项通过）、`pnpm run typecheck` 及两个变更文件的 ESLint，全部通过；累计相关测试为 215 项。全部调整完成后再次运行生产构建，通过（17.43 秒），其前置 3 项语言键完整性测试也通过；仅保留上述既有构建提示。

按最终 HTTP 边界补充共享缓存的中英文说明：“仅 HTTP 出站使用；原生 WebSocket 不使用缓存票据或触发采集。”随后运行详情诊断组件及语言键完整性测试（26 项通过），并检查三个变更文件的 ESLint，通过；没有新增配置或接口字段。

### 合成接口浏览器验收

通过隐藏 Codex IAB 访问本机 Vite（`127.0.0.1:18100`）和合成 API（`127.0.0.1:18101`）。临时脚本保存在 `.git/codex-shared-bundle-browser-mock.mjs`，从 `frontend` 目录启动，使用虚构账号和 Cookie 名称，完全没有真实令牌或 Cookie 值。

- 账号编辑页没有额外“账号 OAuth 授权（三系统共用）”面板和其重新授权／解绑按钮，即使模拟旧响应仍包含 `reauth_required`。Windows、macOS、Linux 的 UA、安装 ID、默认系统和重生成入口保留，身份卡片没有可编辑输入框。
- 账号操作菜单原有“重新授权”入口仍存在。
- 缓存详情说明按凭据账号及模型跨系统共享，区分票据本地到期时间、HTTP 包可复用截止时间与包剩余可用秒数。
- 成功后正常等待显示“等待定时刷新／按活跃模型计划持续刷新”，并说明 30 秒间隔、12 秒单次上限和 30 分钟业务活跃条件。
- Cookie 先到期的合成目标票显示“无可用缓存”和 Cookie 到期原因，但仍显示“符合目标形态”；列表保持模型名与状态点的原布局，Cookie 临期黄色、已过期灰色。
- 系统筛选位于观测区。Linux 切换 macOS 后，观测的真实请求系统随筛选变化，左侧两项共享模型缓存保持可见。
- Cookie 来源显示“本次票据包”，保存原因显示“Cookie 已与本次目标票据一起保存”，没有输出包内容或凭据值。
- 完成后的浏览器错误日志为空；临时标签和合成服务已关闭。

环境调整记录：第一次从仓库根目录启动临时 Vite 时，Tailwind 未找到前端主题配置；改为从 `frontend` 启动后完成验收，无需修改产品代码。宽表的固定操作列会遮住靠右缓存按钮，本次通过原有“更多 → 查看 turn-state 状态”入口完成详情验收。

前端模拟验收不证明后端事务、跨实例并发、真实 OAuth 有效性或真实上游行为；这些由后端回归和隔离存储集成测试覆盖。

## 后端定向失败归因

使用 `git archive HEAD backend` 创建 `.git/head-fixture-baseline/backend` 独立副本，没有切换、覆盖或回退当前工作树；基线为 `f18e7212e`。在 WSL 的副本中执行：

```sh
go test -tags unit ./internal/service \
  -run '^(TestOpenAIStreamingTerminalAndClientCancellationDoNotQuarantineProxy|TestOpenAIHTTPPassthroughStripsOnlyOAuthLegacyResponsesBeta)$' \
  -count=1
```

- **既有失败**：`TestOpenAIHTTPPassthroughStripsOnlyOAuthLegacyResponsesBeta/api_key_explicit_beta_remains_caller_controlled` 在 HEAD 上原样复现。测试第 189 行期望保留 `responses=experimental, future_feature=v1`，实际只保留 `future_feature=v1`。现有协议判断把 OpenAI API Key 账号也纳入 Codex 身份协议，透传构建器和最终规范化会清理旧 Responses beta；相关实现和测试均无本次差异。本轮保留该行为与失败记录，未放宽断言。
- **本次凭据来源调整后的旧夹具**：上述客户端取消／代理熔断测试在 HEAD 通过，但旧夹具只标记系统授权为有效，没有填写 `accounts.credentials`。新账号级准入因缺少 token 返回 `oauth_authorization_unavailable`，并非代理被错误熔断。已为该单个测试显式加入虚构 access token，保留“兼容为 true、原因为空”及全部终态／取消断言。
- 该夹具修改后的独立 race 尝试曾因并行实现期间暂缺 `openaicookies.WithFallback` 而止于编译；API 补齐后，最终后端 race 中该取消测试已通过。基线结果日志为 FastCtx 作业 `j-9dm66z`，中间态编译日志为 `j-5mgrvx`。未请求真实上游。
- **另一项既有失败**：同一 HEAD 副本执行 `go test -json -tags unit ./internal/service -run '^TestOpenAIRequestBodyLimitFailover_HTTP413SwitchesAccountsBeforeWrite$/api_key_passthrough$' -count=1`，在测试第 85 行原样复现请求体字节相等断言失败（Go JSON 记录 `/tmp/sub2api-head-http413.jsonl`，摘要作业 `j-9y0tgn`）。此前的 413 账号切换、禁止同账号重试、写出前切换及响应体关闭断言均已通过。HEAD 的 Responses finalizer 已自动补默认指令并投影客户端元数据，而该测试仍要求无指令输入原字节不变；测试、透传构建器、finalizer 及默认指令实现均无本次差异。本轮未修改该既有行为或放宽测试。

# Codex 共享票据包与账号授权恢复验证

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

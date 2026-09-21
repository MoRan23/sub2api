# Codex turn-state 采集与状态展示验证记录

基线：`dev@26d980d46`。本次验证使用仓库声明的 Go 1.27.0、`GOEXPERIMENT=jsonv2`、WSL Ubuntu 24.04，以及隔离的 PostgreSQL 18.1 和 Redis 8.4 测试容器。没有连接收费模型上游或部署应用。

## 验证范围

- 编译全部后端包；运行 service、repository、admin handler、DTO、migrations 中相关 turn-state 回归与竞态测试。
- 验证 292/312、332/356 封装和时间、自然响应优先、无状态不采集、异常历史凭据隔离及成功交付边界。
- 验证 10 秒退避、账号级 429/401/403 约束、提前 5 分钟续采、同值不延寿、旧有效缓存保留、真实业务活动窗口。
- 验证慢采集 CAS、业务抢占、取消通知丢失、状态行锁等待后新租约可见，以及快速 WS 响应在成功写回调前完成的两种交错。
- 验证缓存开启但指纹关闭，以及维护初始化失败时的 HTTP/Chat/Messages/WS 观测；独立采集观测标明来源且不延长业务活跃时间。
- PostgreSQL/Redis 集成验证配置行锁、凭据代次、历史消费水位、单飞、代理删除和导入映射；真实 Redis 验证通知载荷、断线重连和订阅取消清理。集成测试设置 `CI=true`，不将跳过算作通过。
- 前端相关 132 项测试、类型检查、lint、生产构建；浏览器模拟验收 8 项，包括最多五行、无占位、额外观测动态补入、红绿灰、宽屏左右列和窄屏堆叠、五秒刷新保持内容、无重叠请求及关闭取消。

## 可复现命令

后端在 `backend` 目录执行：

```bash
env -u OPENAI_API_KEY GOEXPERIMENT=jsonv2 go test ./... -run '^$'
env -u OPENAI_API_KEY GOEXPERIMENT=jsonv2 go test -race \
  ./internal/service ./internal/repository ./internal/handler/admin ./internal/handler/dto ./migrations \
  -run 'Codex(State|TurnState)|AccountConfigurationCommit' -count=1
env -u OPENAI_API_KEY GOEXPERIMENT=jsonv2 go test -tags unit \
  ./internal/service ./internal/repository ./internal/handler/admin ./internal/handler/dto ./migrations \
  -run 'Codex(State|TurnState)|AccountConfigurationCommit' -count=1
```

真实存储测试使用 `go test -tags integration ./internal/repository` 的相关测试；TestMain 创建隔离容器并应用迁移 245/246。完整命令、输出及浏览器截图保存在本地 `.git/task-artifacts/codex-turn-state-demand/` 和 `.git/task-artifacts/codex-turn-state-status-refresh/`。

## 额外发现与基线对照

第一次集成选择式意外扩大到整个账号仓储套件，额外出现两项失败，未将其计为本次相关测试通过：

- `TestAccountRepoSuite/TestEnsureOpenAIInstallationIDSupportsSetupTokenOwnerOnly`：当前树和固定基线均因同一 `chk_accounts_parent_dimension` 约束失败，已确认是原有 fixture 问题。
- `TestAccountRepoSuite/TestUpdateExtra_SchedulerNeutralSkipsOutboxAndSyncsFreshSnapshot`：组合运行出现 outbox 计数失败；当前树和基线分别单独运行均通过。组合运行问题未归因，未删除或放宽断言。

本记录不宣称后端所有无关测试或全量后端 lint 已通过。模型长度分类只说明可观察封装形态，不验证解密内容或模型质量。

## 采集正文与 SSE 限流调整（基线 `45ebf0935`）

采集正文对齐 `ccodex-sleep-state@b18fabf9`；保留现有账号身份与独立请求隔离。结构化 SSE 限流沿用 HTTP 429 的账号级采集退避，真实 HTTP 状态保持不变。按后续要求，普通失败及无目标状态的最短重试间隔由 10 秒改为 30 秒，更长 Retry-After 和账号冷却优先。

新增测试使用模拟 HTTP transport，覆盖完整正文、18 组限流码/事件/位置组合、字段优先级、SSE event 回退、多行/单字节分块、畸形错误与文本误判防护、失败立即停止及 token 清空、Retry-After，以及 HTTP/SSE 限流后旧缓存保留和跨模型冷却。没有调用真实模型接口。

本轮在 `backend` 目录执行以下相关检查；测试输出保存在 `.git/task-artifacts/codex-collector-sse-limits/`：

普通回归和带 `unit` 标签的竞态测试各通过 484 项测试结果（含子测试），其中新增 56 项；两轮均无失败、无跳过。使用仓库声明的 Go 1.27.0 和 `GOEXPERIMENT=jsonv2`。本轮未改数据库或前端，未重复运行真实存储集成和前端套件。

```bash
env -u OPENAI_API_KEY GOEXPERIMENT=jsonv2 go test ./internal/service \
  -run 'Codex(State|TurnState)' -count=1 -json
env -u OPENAI_API_KEY GOEXPERIMENT=jsonv2 go test -race -tags unit ./internal/service \
  -run 'Codex(State|TurnState)' -count=1 -json
```

## 采集与业务并行（基线 `cbaa6cf97`）

移除采集启动、运行、历史需求创建、到期扫描及仓储发布中的业务租约拦截。业务开始不再取消采集；业务取得满足需求的新目标后仍取消多余采集，同一临期 token 不取消续期。保留账号级采集单飞、30 秒最短退避、配置代次、模型策略和发布 CAS。

并发回归覆盖业务在采集前后开始、续期与业务并行、调度版本变化不破坏冻结快照、业务异常失效 CAS 冲突重试、重复异常业务期间采集合格结果仍可发布，以及取消通知丢失时新业务缓存仍受保护。真实 PostgreSQL 测试覆盖业务租约存在时扫描和发布、状态行锁等待后业务租约提交、新版本提交后旧 CAS 拒绝，以及历史需求消费幂等性。

本轮使用 Go 1.27.0、`GOEXPERIMENT=jsonv2` 和隔离 PostgreSQL / Redis 测试容器。以下命令在 `backend` 执行，输出保存在 `.git/task-artifacts/codex-collector-parallel/`：

```bash
env -u OPENAI_API_KEY GOEXPERIMENT=jsonv2 go test ./internal/service ./internal/repository \
  -run 'Codex(State|TurnState)' -count=1 -json
env -u OPENAI_API_KEY GOEXPERIMENT=jsonv2 go test -race -tags unit ./internal/service ./internal/repository \
  -run 'Codex(State|TurnState)' -count=1 -json
env -u OPENAI_API_KEY CI=true GOEXPERIMENT=jsonv2 go test -tags integration ./internal/repository \
  -run 'TestCodex(CollectorPublicationPostgres|HistoryDemandPostgres|StatePostgres(ScanSelectsOnlyDueCollectors|ConcurrentCASAndGenerationFence|DisableSerializesWithPublication))' \
  -count=1 -json
```

普通回归和带 `unit` 标签的竞态测试各通过 513 项测试结果（含子测试）；真实存储集成通过 14 项。三轮均无失败、无跳过。额外覆盖 PostgreSQL 微秒精度不会误保留本轮防崩溃预留，以及并发更长账号冷却在采集普通失败、自然业务成功后仍有效。

本轮没有前端代码或数据库结构变更，未重复运行前端套件；没有发送真实收费模型请求或部署应用。

## 采集错误展示与代理切换保留缓存（基线 `342f1f68a`）

后端保留固定脱敏的采集失败类别，前端显示原因与对应提示，合并重复错误并安全处理未知类别。账号不可采集的原因拆分为状态非正常、调度关闭和过期。沿用现有状态字段，不新增迁移，不改变采集并发、缓存发布与重试策略。

追加的代理切换修复在账号配置事务中保留正常有效缓存及其原到期时间，仍更新代次拒绝旧结果；异常既有需求继续处理。覆盖个人／Team、单条／批量、取消代理、分类／凭据同时变化、到期后采集、异常提前失效、同 token 不延寿、跨模型、空闲恢复、历史水位及事务回滚。

验证覆盖模拟传输与 SSE 错误、HTTP 错误、超时、限流、未知错误不泄漏、失败响应不发布 token，以及前端中英文文案、错误去重、历史错误标识、自动刷新恢复和未知错误安全回退。本轮输出和浏览器模拟验收截图保存在 `.git/task-artifacts/codex-collector-errors/`；所有采集响应与管理 API 均使用本地模拟，没有访问真实收费模型接口。

相关 service 普通回归通过 576 项；最终代码的 service / repository 带 `unit` 标签竞态测试通过 589 项（均含子测试）。隔离 PostgreSQL / Redis 的相关集成选择式通过 64 项。上述检查均无剩余失败或跳过。首次集成运行发现三个新增 fixture 的测试账号名超过数据库 100 字符限制，缩短子测试名称后重跑通过，未修改产品约束或测试断言。

```bash
env -u OPENAI_API_KEY GOEXPERIMENT=jsonv2 go test ./internal/service \
  -run 'Codex(State|TurnState)' -count=1 -json
env -u OPENAI_API_KEY GOEXPERIMENT=jsonv2 go test -race -tags unit ./internal/service ./internal/repository \
  -run 'Codex(State|TurnState)' -count=1 -json
env -u OPENAI_API_KEY CI=true GOEXPERIMENT=jsonv2 go test -tags integration ./internal/repository \
  -run '^TestCodex(State|TurnState|CollectorProxyChangePostgres|HistoryDemandPostgres|CollectorPublicationPostgres)' \
  -count=1 -json
```

前端六个相关测试文件共 70 项通过，包括账号列表、状态弹窗、管理 API 及中英文翻译；变更文件 ESLint、`vue-tsc` 类型检查与生产构建通过。浏览器使用模拟管理接口验收错误提示、重复提示合并、历史错误、未知类别安全回退、五秒刷新恢复、窄屏布局，以及代理切换后正常缓存继续可用；该状态保留真实本地过期时间，最早可重试时间不会被伪造为缓存到期时间。未运行无关的全量后端测试或部署应用。

## 多采集代理与按模型轮换（基线 `eab22c5c3`）

新增有序代理配置、兼容单值读取及迁移 247；运行态持久化代理选择、异常计数和精确采集尝试标识。覆盖第三次有效 312／356 后循环轮换、普通错误保留计数、跨模型隔离、成功后保留代理并清零、空闲恢复、配置变更保留正常缓存、迟到结果及迟到取消通知的隔离。配置测试还覆盖显式空数组和畸形新字段不能恢复旧单代理、旧快照保护、全部代理引用与删除锁、按映射键保序导入导出。

本轮使用 Go 1.27.0、`GOEXPERIMENT=jsonv2`，相关 service / admin / DTO 普通回归通过 719 项；扩大的 unit 竞态选择式通过 734 项，另有一项下述基线失败，无跳过。隔离 PostgreSQL / Redis 相关集成通过 85 项，无失败或跳过。命令在 `backend` 执行，输出在 `.git/task-artifacts/codex-multi-proxy/`：

```bash
env -u OPENAI_API_KEY GOEXPERIMENT=jsonv2 go test \
  ./internal/service ./internal/handler/dto ./internal/handler/admin \
  -run 'CodexTurnState|CodexState|CodexMultiProxy' -count=1 -json
env -u OPENAI_API_KEY GOEXPERIMENT=jsonv2 go test -race -tags unit \
  ./internal/service ./internal/repository ./internal/handler/admin ./internal/handler/dto ./migrations \
  -run 'Codex(State|TurnState|MultiProxy)|AccountConfiguration' -count=1 -json
env -u OPENAI_API_KEY CI=true GOEXPERIMENT=jsonv2 go test -tags integration ./internal/repository \
  -run '^TestCodex(State|TurnState|MultiProxy|CollectorProxyChangePostgres|HistoryDemandPostgres|CollectorPublicationPostgres)|^TestMergeCodexImportRealPostgres' \
  -count=1 -json
```

`TestAccountConfigurationPreservesLatestAfterStaleSnapshot` 的整个 Extra 对象相等断言没有计入服务端初始化的私有 `codex_turn_state_credential_epoch`，在当前代码与 `git archive eab22c5c3 backend` 提取的固定基线中，均以相同 Go 工具链和竞态参数复现同一失败。原断言未删除或放宽；基线输出为 `go-baseline-stale-snapshot.jsonl`。首次集成编译发现新增测试将仓储具体方法误当作接口方法调用，修正测试接收者后完整集成重跑通过。

前端九个相关测试文件共 202 项通过，变更文件 ESLint、类型检查和生产构建通过。浏览器使用本地模拟接口通过八组验收：旧单值回显、列表增删与排序、重复过滤、显式空数组及新数组提交、Spark 只读、代理名称与计数轮换、五秒刷新及关闭取消、390px 下新增配置与状态组件布局。没有外部请求、浏览器错误或真实模型调用；预览服务已关闭。另观察到未改动的 WS mode 表单区域在窄屏下横向溢出，未在固定基线复跑，也未计入本轮新增组件的通过范围。

## WS 异常补偿与请求诊断修复（基线 `c1912aaa6`）

确认并复现了已有正常缓存时的 WS 时序缺陷：异常响应已成功交付，而成功发送绑定尚未完成，原代码在补记发送后只回填历史；历史保护有效缓存，因而没有废除旧缓存或建立采集需求。新增回归先在旧实现中让个人／Team、Finish 内／后绑定四种组合失败，再修复为按原请求缓存身份补执行异常 CAS 发布。补偿只保存安全形态摘要，覆盖并发新缓存保护、重复绑定、未发送／未交付、配置／模型策略变化、时间校验、目标优先及现有退避。

另修复采集错误原因被压成通用错误，以及总截止时间抹掉已知传输阶段／SSE 限流原因；新增类型化网络错误、依赖固定模板、隐私保护、真实发送边界及最终持久化错误分类测试。观测新增同次请求的发送时间、注入快照、交付结果和独立关联 ID，补测 WS 未绑定／失败发送不覆盖旧摘要、指针隔离及日志不含 token 或私有尝试标识。

本轮使用 Windows Go 1.27.0 和 `GOEXPERIMENT=jsonv2`，以下统一回归与竞态命令通过 **815 项测试结果（含子测试），0 失败、0 跳过**。新增完整 HTTP 流程覆盖个人／Team、Responses／透传／Chat／Messages、响应头／metadata：正常缓存实际出站，异常成功交付后撤销旧缓存、排队、独立采集并在下一次业务注入新缓存；无状态响应保留旧观测及其原始时间。

```bash
env -u OPENAI_API_KEY GOEXPERIMENT=jsonv2 go test -race -tags unit \
  ./internal/service ./internal/handler/admin ./internal/handler/dto \
  -run 'Codex(State|TurnState|MultiProxy)' -count=1 -json
```

输出在 `.git/task-artifacts/codex-state-diagnostics/go-race.jsonl`。WSL 启动多次返回 `Wsl/Service/0x8007274c`，Windows 环境也没有 Docker CLI；本轮未能重跑隔离 PostgreSQL／Redis 集成，不能将此前的 85 项通过算作本次验证。本次没有修改数据库迁移或仓储 SQL，没有为恢复测试环境停止 WSL 或更改服务器。

前端相关 **78 项测试**、类型检查、变更文件 ESLint 和生产构建通过。模拟接口浏览器六组验收通过：采集不携带旧状态的说明、业务正常出站／异常响应及失败交付说明、维护降级和细分错误、折叠关联信息、390px 布局、五秒刷新／不重叠／关闭取消。报告及截图在 `.git/task-artifacts/codex-state-bugfix/`；无浏览器错误或外部请求，预览服务已关闭。20 秒采集上限、30 秒重试、代理轮换和列表红绿灰规则不变；未发送真实收费采集请求，未部署。

## 多代理执行计划及调查子任务回看（基线 `40875b265`）

对照已批准的“Codex turn-state 多采集代理与按模型轮换”计划重新核查配置兼容、显式清空、旧客户端保护、数组深拷贝、账号和代理锁、全列表引用及删除保护、有序导入导出、目标优先、三次有效异常轮换、跨模型隔离、空闲保留、精确尝试标识及正常缓存保留。上述要求没有发现新的确定漏做项。核对过原多代理执行的集成日志，85 项确为当时 PostgreSQL／Redis 实测通过，不等同于本次重跑。

同时回看单独任务“排查 Codex 采集连接失败与超时”的全部调查结论：一条失败约 1.43 秒，随后同代理成功，旧日志不足以证明代理并发不足、TLS 特征问题或 20 秒超时过短。失败切换丢弃的响应不建需求、独立采集出站长度为 0，以及请求发送早于新缓存取得，均属于已确认行为；缺少旧请求快照证据的个案不能倒推为未注入。这些结论不支持擅自更改 TLS 配置或增大超时。

本次确认并修复的边界问题：

- 已成功交付的有效异常在业务活动写入、运行态读取或 CAS 连续冲突时可能丢失重采需求。先复现七组故障，再以正常完成／WS 迟到绑定共用的有界待发布队列修复；覆盖并发新缓存保护、后台重试、过期、空闲、配置及模型变化、容量上限和停止后迟到回调。此队列为本进程短期补偿，尚未落库项在进程重启后不恢复。
- 摘要此前仅按账号和模型索引，凭据变更后可能继续显示旧红绿状态。现在按实际发送时确认的私有凭据代次隔离，旧业务和采集响应不能污染新凭据观测；维护准备失败时仍保留能够通过实际认证头校验的被动观测。
- 未知套餐的合法观测在管理员手动分类后仍显示“无法分类”。单账号和批量读取现在使用原观测的私有封装校验证据重新匹配当前套餐，原时间不变，非法或观测时已过期的数据不会升级为有效。
- 采集适配器虽然重新读取账号，但未再次核查停用、调度关闭及账号到期。新增三种回归先复现真实发送，再修复为发送前拒绝。

按随后确认的新要求，固定 30 秒等待仅用于未取得可用目标状态及同一临期状态未完成续期；普通网络、HTTP、流内错误在下一次每秒调度可重试。429 保留 Retry-After／账号冷却，401／403 保留暂停，异常计数与轮换规则不变。

补充正式 `codexnative → req` 的本机 TCP／TLS／代理测试，实际触发 TLS 证书错误、TLS 握手超时、响应头超时、CONNECT 407 和 SOCKS 认证拒绝，五种情况下的真实 `httptrace` 阶段及最终脱敏错误类别均正确；没有手动调用回调或连接真实上游。首次测试夹具未读完请求体导致清理等待，修正夹具后五种场景通过，没有据此修改生产传输代码。

本轮 Windows Go 1.27.0／`GOEXPERIMENT=jsonv2` 的最终竞态选择式同时包含原先遗漏的插件交付组合，并扩大到仓储及迁移的相关 unit 测试：

```bash
env -u OPENAI_API_KEY GOEXPERIMENT=jsonv2 go test -race -tags unit \
  ./internal/service ./internal/repository ./internal/handler/admin ./internal/handler/dto ./migrations \
  -run 'Codex(State|TurnState|MultiProxy)|TestPluginTurnStateMerge' -count=1 -json
```

最终通过 **921 项测试结果（含子测试），0 失败、0 跳过**，五个包均通过。扩大测试时发现旧错误展示测试仍期待“退避”；按新规则精确更新为普通错误可排队、鉴权失败暂停、代理不可用阻塞，并继续严格检查最近错误和隐私字段，重跑通过。最终输出位于 `.git/task-artifacts/codex-state-review/go-race-final.jsonl`。

本轮没有修改页面、数据库迁移或仓储 SQL；没有将上一轮的前端验收或此前的真实存储测试计为本轮重跑。WSL 启动故障及 Windows 缺少 Docker 的限制仍在，真实 PostgreSQL／Redis 集成未重新执行。没有部署、改动服务器或发送真实收费采集请求。

## 独立采集缓存临期与空闲过期颜色（基线 `fd366f5b5`）

账号列表增加黄色状态点：独立采集的可用目标缓存剩余 5 分钟以内时优先显示黄色，即使最新续采结果为异常形态。缓存到期后，仅在后台明确返回 `idle/idle`（该模型超过 30 分钟无真实业务）时优先显示灰色；其他情况保留原有观测规则。按原 `expires_at` 判断边界，不把观测时间当作签发时间；不足一秒但尚未到期的缓存仍为黄色。中英文悬停说明及空闲原因文案同步更新，模型行数和行高不变。

账号状态点、状态弹窗、诊断及批量刷新共 **93 项前端测试通过**；修改文件 ESLint 和 `pnpm run build` 通过，构建包含翻译完整性及 `vue-tsc` 类型检查。覆盖个人／Team、5 分钟与过期边界、续采异常、空闲过期、暂停但缓存仍有效、禁用和被撤销缓存、刷新稳定性。构建仍提示部分产物超出建议块大小。结果位于 `.git/task-artifacts/codex-state-colors/`。本轮仅改前端展示及说明，未改后台调度、未部署、未发送真实采集请求。

## 账号连接测试响应摘要（基线 `2d71ba10f`）

OpenAI 文本连接测试和原生压缩测试增加 `response_info` SSE 事件，在测试完成或错误事件之前输出一次。展示真实返回模型，以及常规 OAuth 的 turn-state 实际长度、目标长度和匹配结果。没有模型字段不回填请求模型；套餐及时间校验复用缓存解析规则，Spark 按凭据母账号分类。API Key Chat Completions 只报告实际返回模型。此诊断不维护缓存、不建立采集需求，不改变原测试成功／失败判定。

Windows Go 1.27.0、`GOEXPERIMENT=jsonv2` 下运行：

```bash
go test -race -tags unit ./internal/service \
  -run 'TestAccountTestService_.*(OpenAI|OAuth)|TestAccountTestResponseInfo|TestParseTestSSEOutput' \
  -count=1 -json
```

共 **49 项测试结果（含子测试）通过，0 失败、0 跳过**，其中新增响应摘要回归 23 项。覆盖个人 292／312、Team 332／356、未知套餐、套餐不匹配、非法封装、未来及过期时间、缺失状态、实际模型不同、响应头与 metadata 目标优先、Spark 母账号、失败流不转成功、API Key Chat、压缩模式及无 token 泄露。日志在 `.git/task-artifacts/account-test-response-info/go-race.jsonl`。

前端账号测试弹窗 **12 项测试**及 **3 项 i18n 测试**通过；相关 ESLint、`pnpm run build`（包含翻译完整性和 `vue-tsc`）通过。构建保留既有工具提示和产物体积警告。所有上游响应均为本地模拟；未调用真实收费接口，未改数据库、后台采集策略或部署。

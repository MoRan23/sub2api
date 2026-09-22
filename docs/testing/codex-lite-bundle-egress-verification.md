# Lite 取票与 HTTP 出票代理绑定验证

实现基线：`26366aa4f`。参考采集模板：`ranxi2001/sub2api@f69b681543999e9dd28575bb2e2bc5767066ccac`。

## 交付行为

- 独立采集采用 fresh session、ping/pong、noop 声明与 Lite，请求不带旧 Cookie、旧票据或业务正文。累计读取上限 1 MiB，严格完成及失败优先，保留模型校验。
- 票据、Cookie、协议、代理 ID 与连接代次原子发布。非 Lite 不转换协议、不改为采集出口；原生 WS 旁路。
- 业务在路由确定后生成身份、时区、TLS 及关联遥测。发送前本地拒绝完整重建原代理请求，网络请求失败不触发此重建。
- 三系统共享包但保留各自身份；只有实际发出的合格 Lite HTTP 请求更新独立采集活动时间。
- Cookie 诊断来自发送边界，明确区分未发送、已到发送边界及历史未知，不由票据长度推断。

## 本地验证范围

所有测试使用合成票据、假凭据、本地 HTTP transport 或隔离 PostgreSQL / Redis。没有发送真实业务、采集、OAuth 或遥测请求，没有部署。

覆盖采集模板、12 秒超时、240 / 30 / 5 秒边界、异常代理轮换、完整流与尾部失败、总读取限制、协议不匹配、Cookie 作用域和整包过期、原子发布、并发失效、代理同 ID 改连接配置、前置失效重建、固定重试快照、三系统、Spark、WS 旁路及页面字段。

已执行：

- 隔离 PostgreSQL / Redis：相关仓储、完整 `ProxyRepoSuite`、导入兼容及代理代次测试，`-race -tags integration`，221 项测试及子测试通过，0 跳过、0 失败。包含迁移 258、代理变更及锁等待到期、失效水位、旧包保留调度和原子发布。
- HTTP 普通／透传／Chat／Messages、WS→HTTP、三系统身份与 Spark、时区、遥测和代理归因的定向 Go 回归及竞态测试。
- Codex 状态全组 `go test -race ./internal/service -run '^TestCodexTurnState' -skip '^TestCodexTurnStateCollectorStopsWhenProfileStorageFails$' -count=1` 通过；排除项及基线证据见下方。
- Cookie、HTTP 发送边界、插件前置拒绝、遥测拒发不创建活动及回退只创建一次活动的定向竞态测试通过。
- 管理接口及 DTO 的 Codex 状态定向测试；native HTTP 三系统采集连接隔离测试。
- 服务端 `go build ./cmd/server`、迁移包及 native transport 包测试通过。
- 前端 202 项相关 Vitest、类型检查、变更文件 ESLint、Vite 构建；独立 Playwright 浏览器验证绑定、原出口旁路、未发送、历史未知、包不可用、独立采集六场景，以及英文和移动宽度。浏览器控制台无错误，未访问实际 API。

未运行整个仓库的无关测试套件；未进行真实上游验证。

## 既有失败

以下两项在隔离的原始 `26366aa4f` 源码中同样失败，未通过修改断言掩盖：

- `TestCodexTurnStateCollectorStopsWhenProfileStorageFails`：旧 fixture 预期身份存储错误，但授权读取更早返回 `collector_account_unavailable`。实际请求仍被阻止。最终 Codex 状态竞态回归显式排除此已知失败。
- `TestOpenAIWSHTTPBridgeAcceptsFirstFrameAboveLegacy16MiB`：断言要求转换后的消息小于原阈值，现有默认指令增加正文长度导致失败。大消息本身的桥接能力与本次代理绑定无关；其他相关桥接回归单独执行。

## 升级

先排空并停止旧进程，备份数据库后统一应用迁移 258，再启动新代码。迁移清除无可信出口绑定的旧包与旧尝试，不从旧业务时间推断 Lite 资格；保留真实业务历史、限流、暂停及历史消费水位。不得让旧进程继续写入新结构。详见 [使用说明](../codex-turn-state.md)。

## 账号“使用出票代理”开关补充验证

实现基线：`e0a9cc232`。默认开启；关闭时仍注入同协议票据和 Cookie，实际业务出口使用账号原代理或直连。出票来源和实际出口分别记录。切换不清票，已开始的请求及重试冻结原选择。

- Go 定向测试和竞态测试覆盖 Responses、透传、Chat、Messages 的实际 Cookie 发送边界，分别使用账号代理与直连，并执行关闭→开启→关闭切换。检查票据、Cookie、实际出口、出票来源及旧包保留。
- 覆盖 Spark 继承母账号、重试冻结、新自然票按实际出口发布、关闭后仍拒绝协议不匹配、来源代理代次校验、原生 WS 旁路，以及未提前选路的请求准备和状态查询。
- 配置及导入导出测试覆盖缺省开启、显式关闭、旧客户端省略字段、非法值和配置意图复制。
- 隔离 PostgreSQL / Redis 集成测试覆盖单账号及批量编辑、各账号独立保留省略值、迁移 259 的数据库触发器与票据包保留，并确认缓存总开关变化仍推进代次。相关多代理配置回归通过。
- `go test -race ./internal/service -run '^(TestCodexHTTP|TestCodexTurnState)' -skip '^TestCodexTurnStateCollectorStopsWhenProfileStorageFails$' -count=1` 通过；保留上文既有失败的显式排除。
- 前端 140 项相关 Vitest、类型检查、全量 lint 与构建通过。构建仍有 Browserslist、动态导入及包大小提示。

本次不重复完整仓库测试或浏览器验收；没有真实上游验证、部署或外部业务请求。升级需应用迁移 259，它仅调整配置触发器，不清票。

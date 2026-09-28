# 糖果题测试执行链验证记录

本记录覆盖固定账号执行器、服务端测试用途及相关授权隔离。队列、迁移、页面和完整仓库验收由对应验证记录补充。全部测试使用合成凭据、内存替身或本地模拟；没有发送真实推理、授权、遥测或收费请求。

## 执行契约

- `AccountCandyTestTransport` 实现 `CandyTestExecutor.Options` 和 `Execute`；选项实时读取各账号上游模型目录，使用上游原始 ID，不从业务白名单或映射生成候选项。目录读取失败或为空时返回账号级原因，不回退本地模型列表；创建批次重新核对能力。
- 通过现有 `OpenAIGatewayService.Forward` 固定账号发起 HTTP 请求，直接发送所选上游模型 ID，保留 Responses／透传／Chat 转换、Lite、代理、TLS 和默认系统身份。只有测试用途绕过业务模型白名单及映射，普通业务规则不变。
- 服务端上下文携带测试用途，用户不能通过请求头或正文设置。实际发送入口限制一次推理调用，禁用自动重定向；测试保留原有截止时间和取消信号。
- 只保存最终回答，限制为 1 MiB；单个 SSE 行／事件及输出项目分类也有界。明确失败优先于迟到完成，完整终态中的最终输出优先于早期增量，不保存推理或明确 commentary 内容。
- 必要的 OAuth 刷新沿用现有凭据 CAS，但不回退旧令牌，也不改变账号状态。Agent Identity 首次 task 注册只对原认证元组做 `task_id` CAS，不全量覆盖凭据。

## 已执行

在 `backend` 目录执行：

```sh
go test ./internal/service -run '^TestCandy' -count=1
go test -race ./internal/service -run '^TestCandy' -count=1
go test -race ./internal/repository -run '^TestCandyHTTPTransportDoesNotReplayRedirectedPOST$' -count=1
```

三项通过。服务包竞态检查输出为 `ok github.com/Wei-Shaw/sub2api/internal/service 7.237s`。该表达式同时覆盖名称以 `TestCandy` 开头的执行器、队列和题目隔离测试，不等于完整服务包或完整判分器测试套件。专用实际 HTTP 重定向竞态检查输出为 `ok github.com/Wei-Shaw/sub2api/internal/repository 6.546s`。

执行器的专项覆盖：

| 范围 | 证据 |
| --- | --- |
| 一次发送、无失败重试 | Responses、透传、Chat 三条路径分别模拟 400／401／403／429／500，共 15 个子用例；检查物理发送替身计数为 1 |
| 账号副作用隔离 | 检查账号仍为 active／schedulable，原账号对象未被测试用途标记污染；限流仓储替身拒绝未预期写入 |
| 上游模型 ID 和原始证据 | 测试直接发送所选上游模型，不应用业务模型映射；保留公开名称改写前的上游声明模型，检查没有 tools、历史或 continuation |
| 系统及凭据类型 | 三系统默认 UA，Spark 使用母账号默认系统，以及 OAuth、setup-token、PAT、已有 task 的 Agent Identity |
| 能力选项 | 实时查询账号上游目录；未知能力仅默认档位，失败账号不生成本地回退候选项 |
| 授权刷新 | 合成仓储验证 CAS 期望元组、成功凭据更新、失败不回退旧令牌、过期缺刷新凭据不暂停账号 |
| 授权更换及 Agent task | 签名密钥变化被拒绝；旧 task 初始化不能覆盖替换后的密钥；等待初始化锁可被取消 |
| 截止与取消 | 两种原有上下文分离方法均保留测试截止时间；目的标记穿过实际请求准备链 |
| 响应语义与内存 | 推理不保存、失败后完成不成功、单独 DONE 不完成、工具调用失败、commentary 排除、最终回答／事件／128 个项目上限 |
| 安全错误 | 仅固定错误码，不把上游错误正文写入测试记录 |

具体用例位于 `backend/internal/service/account_candy_test_transport_test.go`。

## 2026-09-28 实时上游目录调整

此前“打开弹窗仅读取本地目录／账号映射”的行为已被替代。弹窗在加载时展示等待状态与账号级目录错误；关闭时取消目录读取，历史和已有批次独立恢复。手动刷新重查目录，后台进度轮询不重复查询目录。选项与创建请求的前端超时按账号数预留每组 15 秒及额外 30 秒，覆盖最多 3 路并发的目录请求。

前端合成接口验证已通过：`AccountCandyTestModal.spec.ts` 14 项、`admin.candyTests.spec.ts` 2 项、`localeKeyCompleteness.spec.ts` 3 项；全前端 `pnpm run typecheck` 与 `pnpm run lint:check` 通过。覆盖部分账号失败而其他模型仍可选、关闭取消目录读取且不取消测试、慢目录期间恢复历史与批次、手动刷新重试、跨页账号以及自适应 HTTP 超时。本次前端验证未请求真实上游。

后端全部 `TestCandy` 及新 `TestFetchCandyTestModels` 默认／竞态测试通过。覆盖 Responses、透传、Chat 三路径与精确映射、通配映射、白名单外模型的 9 种组合，确认发送所选上游 ID 且普通业务映射不变；另覆盖实时目录不读写共享缓存、原始能力、最多 3 路并发、取消、目录为空／失败／所选模型消失时不发送推理，以及目录请求的 401／403／429 不改变账号健康状态。`go build ./...` 和前端生产构建通过。

审查补充发现 OAuth 的旧模型归一也会改变部分实时 ID，已仅对糖果测试旁路。OAuth 和 Spark 的专项／竞态回归确认 `gpt-5.1-codex` 原样出站，普通请求仍按原规则归一至 `gpt-5.3-codex`；现有模型归一与映射专项通过。

扩展模型目录回归中，`TestFetchCodexModelsManifestOAuth401OnlyCoolsSelectedAuthorization` 与 `TestFetchCodexModelsManifestOAuth401TokenRevokedOnlyDisablesSelectedAuthorization` 失败。这两项已在干净基线 `0c7c6f7dd` 使用相同工具链复现：旧测试仓储缺少账号级状态适配，未修改旧断言或以此掩盖新失败。本次所有验证使用本地替身与合成凭据，未发送真实上游目录、推理或授权请求；没有数据库结构变更，未重跑存储迁移集成测试。

后端全量 lint 与前次基线按文件、检查器、问题文本比较，没有新增问题；全量检查仍因既有问题退出非零。初次检查发现的新测试类型断言未检查问题已修正，复查未再报告。前端验证使用组件及接口模拟，未重复执行完整浏览器验收。

## 仍需结合其他验收记录评估的范围

- 本专项使用仓储替身验证 Agent Identity 首次 task 的 CAS 分支；后续 `account_candy_agent_task_cas_integration_test.go` 在隔离 PostgreSQL 中补充通过 5 项测试及 11 个子用例的竞态检查，覆盖只更新 task、身份／归属变化拒绝和并发仅一个成功。详见[存储验证](CANDY_TESTS_STORAGE_VERIFICATION.md)。
- 自动重定向通过既有 `WithHTTPUpstreamRedirectsDisabled` 上下文禁用。新增 `repository/account_candy_redirect_test.go` 用实际传输的标准／native 两条路径，各测试 307 和 308：第一个本地服务器接收可重放 POST 后重定向，第二个本地服务器未收到测试请求。再复用相同客户端发送普通上下文请求，第二端成功收到，证明限制为请求级且未污染普通请求。
- 传输仓储已按服务端测试用途跳过共享 HTTP/2 健康状态的成功／失败学习，保留读取既有协议选择；相应传输回归结果由 HTTP 传输验证记录提供。
- 本专项没有发送真实 OAuth 刷新、Agent 注册、糖果题推理，也没有用线上账号验证 UA、TLS 或代理端点。代理／TLS 的真实底层链与存储集成需结合仓库模拟传输及隔离数据库验收结果。
- 30 分钟上限、跨实例全局并发、租约丢失、持久化取消及最近五条清理由后台队列负责；执行器接受并保留其上下文，不自行重试。`timeout` 仅表示本地截止；上游在更早时间报告流失败仍保留为 `upstream_stream_failed`，不由本地截止配置覆盖。

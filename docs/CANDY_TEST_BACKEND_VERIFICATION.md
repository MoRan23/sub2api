# 糖果题测试执行链验证记录

本记录覆盖固定账号执行器、服务端测试用途及相关授权隔离。队列、迁移、页面和完整仓库验收由对应验证记录补充。全部测试使用合成凭据、内存替身或本地模拟；没有发送真实推理、授权、遥测或收费请求。

## 执行契约

- `AccountCandyTestTransport` 实现 `CandyTestExecutor.Options` 和 `Execute`；选项仅读取本地模型目录、账号映射及已保存能力。
- 通过现有 `OpenAIGatewayService.Forward` 固定账号发起 HTTP 请求，保留既有模型映射、Responses／透传／Chat 转换、Lite、代理、TLS 和默认系统身份。
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
| 模型映射和原始证据 | 请求别名映射至最终模型，保留公开名称改写前的上游声明模型；检查没有 tools、历史或 continuation |
| 系统及凭据类型 | 三系统默认 UA，Spark 使用母账号默认系统，以及 OAuth、setup-token、PAT、已有 task 的 Agent Identity |
| 能力选项 | 未知模型仅默认档位；已知映射模型使用最终模型能力 |
| 授权刷新 | 合成仓储验证 CAS 期望元组、成功凭据更新、失败不回退旧令牌、过期缺刷新凭据不暂停账号 |
| 授权更换及 Agent task | 签名密钥变化被拒绝；旧 task 初始化不能覆盖替换后的密钥；等待初始化锁可被取消 |
| 截止与取消 | 两种原有上下文分离方法均保留测试截止时间；目的标记穿过实际请求准备链 |
| 响应语义与内存 | 推理不保存、失败后完成不成功、单独 DONE 不完成、工具调用失败、commentary 排除、最终回答／事件／128 个项目上限 |
| 安全错误 | 仅固定错误码，不把上游错误正文写入测试记录 |

具体用例位于 `backend/internal/service/account_candy_test_transport_test.go`。

## 仍需结合其他验收记录评估的范围

- 本专项使用仓储替身验证 Agent Identity 首次 task 的 CAS 分支；后续 `account_candy_agent_task_cas_integration_test.go` 在隔离 PostgreSQL 中补充通过 5 项测试及 11 个子用例的竞态检查，覆盖只更新 task、身份／归属变化拒绝和并发仅一个成功。详见[存储验证](CANDY_TESTS_STORAGE_VERIFICATION.md)。
- 自动重定向通过既有 `WithHTTPUpstreamRedirectsDisabled` 上下文禁用。新增 `repository/account_candy_redirect_test.go` 用实际传输的标准／native 两条路径，各测试 307 和 308：第一个本地服务器接收可重放 POST 后重定向，第二个本地服务器未收到测试请求。再复用相同客户端发送普通上下文请求，第二端成功收到，证明限制为请求级且未污染普通请求。
- 传输仓储已按服务端测试用途跳过共享 HTTP/2 健康状态的成功／失败学习，保留读取既有协议选择；相应传输回归结果由 HTTP 传输验证记录提供。
- 本专项没有发送真实 OAuth 刷新、Agent 注册、糖果题推理，也没有用线上账号验证 UA、TLS 或代理端点。代理／TLS 的真实底层链与存储集成需结合仓库模拟传输及隔离数据库验收结果。
- 20 分钟上限、跨实例全局并发、租约丢失、持久化取消及最近五条清理由后台队列负责；执行器接受并保留其上下文，不自行重试。

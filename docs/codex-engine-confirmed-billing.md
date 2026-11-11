# Engine 确认事件计费

本版本与支持 `confirmed-events` 的 Engine Platform 配套使用。公开 JSON/SSE 的标准 `usage` 用于客户端上下文；Sub2API 的钱包、订阅、Key 额度、账号用量和使用记录来自独立确认事件。

## 派发、恢复和去重

1. 选定账号后，通过 `GET /v1/engine/capacity` 确认 `instance_id` 和 `capabilities.billing_contract=confirmed-events`。按地址及凭据身份缓存 60 秒，保存失败或合同不符时不派发模型请求。
2. 派发前持久保存内部关联 ID、用户／Key／账号归属、原始分组、计费策略及非敏感请求摘要。v4 快照不再保存模型目录、渠道价卡、搜索或图片单价；收到确认用量后读取当前价格。快照不包含账户密钥、提示词、文件和密文正文。客户端提供的内部关联头不会被采用。
3. 转发层确认正常终态响应已完整写出并 Flush 后，后台才按原身份查询 `/v1/usage/receipts?client_request_id=...`，取得回执后查询 `/v1/usage/receipts/{id}`。404、网络故障或原响应头丢失继续等待；只读查询不会启动推理。已确认未派发的请求单独终止，无伪造封口。
4. 已交付任务先持久接收分页中的消费事实，再独立结算可定价事件。未定价或缺少必要信息的事件保留待处理；事实与原因保留。源封口和费用结清分别记录。未完整交付的任务清除暂存事件，退出回执查询和结算。
5. `(instance_id, receipt_id, event_id)` 全局唯一。事件事实、扣款、各额度更新及使用记录同事务成功或回滚；重复回执不重复收费。汇总只用于核验，不是另一笔费用。

扣款账本提交后，接收记录仍保留待处理状态；余额、Key 速率、订阅及平台配额缓存处理成功才确认完成。缓存失败或进程重启后，根据已提交账本去重，只重试缓存后处理，完成后才允许任务封账。

事件保留原模型、实际服务档位及来源声明的适用计量维度。总输入包含缓存／图片子集，拆分时不叠加子集。搜索操作与搜索调用使用各自确认单位，不重复计算同一数量。GIF 帧数不当作图片张数。价格、必要数量或实际档位未知时保留未结算，不能套用其他模型或估计用量。

### 缓存写入 TTL 明细

合同名称仍为 `confirmed-events`。每个事件的 `usage` 可额外包含 `cache_write_30m_input_tokens`、`cache_write_12h_input_tokens`，单位均为 token，且在 `applicable_usage` 中声明。两桶必须同时是非负整数，总和等于 `cache_write_input_tokens`；明确的零保留，缺省不补零。已声明适用但尚未取得完整用量的事件继续等待，不能提前按聚合结算。

没有可靠完整明细的源事件继续只发布聚合字段，Sub2API 使用现有聚合价格。可靠明细按既有 30 分钟与 12 小时价格分别计费，aggregate 不再额外相加；12 小时未配置专门价格时沿用已有聚合价回退，明确的零价格保持有效。当前价与兼容旧快照价均采用相同桶语义。

原始 OpenAI 明细路径沿用 `input_tokens_details.cache_write_tokens_details.{ttl_30m_tokens,ttl_12h_tokens}`。Engine 仅从匹配本次原生用量的真实字段提取，不能根据模型、缓存设置或价格猜测。当前部署是否产生这些字段取决于上游实际输出；本次以模拟上游验证契约，不为历史事件补造明细。

事件第一次发布后字段不可变，同一 ID 改写 TTL 会被指纹校验拒绝。汇总只对实际出现的键求和：同组混合明细事件和聚合事件时，两桶汇总可以小于聚合总量。事件、摘要和 JSONB 恢复均区分缺省与显式零。新增计量键不修改完整交付、断连免计费及事务幂等条件，无需新增数据库列。

`confirmed-events` now accepts optional, complete 30-minute / 12-hour cache-write buckets. Both nonnegative integer fields must be explicitly applicable and sum to the event's aggregate. Missing evidence keeps aggregate-only pricing; explicit zero remains distinct from absence. Summary groups can combine detailed and aggregate-only events, so their known buckets may sum to less than the aggregate. Published events remain immutable. Delivery gates, disconnected-request no-charge handling and ledger idempotency are unchanged. No database migration is required. Deploy the compatible Sub2API consumer before an Engine producer starts emitting these optional keys; older consumers reject unknown usage dimensions.

账号停调不影响原账单恢复；地址／密钥／接入模式改变后停止新查询，已用原身份取得并验证的事件仍可结算。租约控制多实例领取，重启后恢复待结算任务。事件账本独立于短期运维记录保留。

价格快照 v2 同时保存总额度订阅的准入批次 ID、有序消费列表、超额承接批次和准入时间。后台恢复时使用这些冻结数据：即使原额度在执行中到期，也结算到原批次，不消费请求准入后追加的额度。新总额度请求缺少有效准入快照时在派发前报错。旧 v1 任务仍可读取并按原规则结算；其未保存的批次信息不能从当前额度推算，补丁不会伪造历史快照。新快照仍不保存正文或凭据，无需数据库列迁移。

Pricing snapshot v2 persists the admitted total-quota batches, their spending order, overflow target and admission time. Recovery settles against those original batches after expiry or later top-ups. New total-quota dispatch requires valid admission evidence. Existing v1 jobs remain readable with their original settlement behavior; missing historical batch evidence is not reconstructed from current grants. No database-column migration is required.

断连、写入失败及 Flush 失败立即取消 Engine 请求。新任务只有正常终态响应完整写出后才进入计费；断连或未完整交付时，任务转为免计费终态，不保存迟到 usage／费用记录、不扣余额、订阅、Key 额度／速率金额或用户平台额度，也不再查询该任务的迟到回执。仅保留内部终止标记，防止重启后重新补记。正常收到输出 token 上限终态的响应仍沿用原计费行为。后台不重发推理，不恢复搜索或工具。同步图片同样取消；已创建的异步图片任务按自己的任务生命周期执行。

v4 结算使用任务原始分组 ID 查询当前分组、渠道和模型价格；Key 后续换组不会改变这笔请求的归属。请求发生时间、已确定倍率及准入额度批次仍保留。价格调整会影响尚未结算的 v4 事件；已提交的事件由账本去重，不重新计价或重复扣费。旧 v1／v2 快照继续按保存的价格结算。

New v4 jobs retain request ownership and admission policy, but no price catalog. Confirmed events use current pricing for the original group at settlement. Client disconnection or incomplete delivery ends the job without usage logs, cost records or customer charges. Such jobs are not polled for late receipts. Legacy pricing snapshots retain their original semantics. A crash before the delivery outcome is persisted leaves the job pending and excluded from polling and settlement.

## 平台配额缓存

平台配额将普通消费和 Engine 已确认消费保存为独立分量，对外仍展示合计。普通 Redis 待刷增量不被 Engine 缓存刷新删除；旧绝对快照也不能覆盖 Engine 的数据库增量。管理员重置推进窗口并清对应分量，迟到旧窗口写入不能恢复旧消耗。

新缓存使用 `billing:upq:v2:<user>:<platform>`，脏集合为 `billing:upq:v2:dirty`。旧缓存 `billing:user_platform_quota:*` 和 `billing:upq:dirty` 不再读取，避免旧秒级窗口与新微秒窗口混用。

## 升级和回退

本变更需要迁移 276（任务／扣款事件账本）、277（平台配额分量）、278（接收事件检查点）和 280（交付结果）。280 保留旧任务原有计费语义，新任务显式以待确认交付状态创建。不能混跑新旧结算进程或新旧配额 flusher；旧程序不识别 v4 快照和交付门禁。

升级顺序：

1. 暂停新入口，排空旧请求及旧记账队列。
2. 等旧平台配额 flusher 全部批次完成，确认旧脏集合为空并且没有执行中的刷回事务，再停止旧实例。
3. 保留数据库备份，统一执行新迁移并切换支持相同合同的 Engine Platform 和 Sub2API；新配额缓存从数据库冷加载。
4. 检查容量合同、待结算队列和异常原因，再开放入口。无需发送真实模型请求验证合同可用性。

回退优先使用仍兼容确认事件账本及配额分量的版本。直接退到旧二进制前，必须停止新入口、排空请求与两类记账工作，解决所有未结清的原账单，并完成 v2 配额刷回；仍有未知消费时不得丢弃账本。停止新结算器后，可在维护事务中将三个 `engine_*_usage_usd` 分量归零、保留已计费总用量，将其重分类为普通分量；保留迁移、任务和事件历史。退休两代配额缓存与脏集合后，旧版才能从数据库冷加载。此步骤是维护方案，不由应用自动执行。

## 离线验证

在 Go 1.27.3（`greenteagc,jsonv2`）运行服务、处理器及仓储定向单元测试、相关竞态检查和后端编译。覆盖派发前持久化、缺头恢复、断连、取消、WS 每轮关联、v4 当前价格、旧 v1／v2 冻结价格、事件去重、分页、源封口和未知值；无分组 Key 的长上下文定价与普通路径对照通过。

在 WSL Docker 的隔离 PostgreSQL 18.1／Redis 中验证真实扣款事务、回滚、跨账号回执归属、租约恢复、扣款后缓存失败重试、窗口重置及重复迁移。额度读取适配器、热刷新和冷填后普通增量重复刷回均有回归覆盖；普通消费与 Engine 分量不会重复合并。测试使用模拟 Engine，不依赖线上数据或真实模型。

2026-11-10 增量验证：定向单元测试、服务／处理器竞态测试、后端编译及隔离仓储 `TestCodexEngineBilling*` 集成测试通过。新增覆盖待交付任务不领取、断连后暂存事件删除、旧 worker 租约失效、未交付不产生用量／账本／额度变更、正常 token 上限终态、原分组当前定价和 JSONB 快照边界。生产数据未修改，未调用真实模型。

建议上线观察任务状态及 `last_error`、已接收／已结算事件差额、源封口积压、平台配额脏集合，以及运维中的 DNS／连接／TLS／请求发送阶段。未知请求只查询原回执，不能以重新发送推理作为账务恢复手段。

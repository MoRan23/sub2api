# OpenAI 目录、Cyber 推理档位与审核内存 / OpenAI catalog, Cyber reasoning and audit memory

## 模型目录

OpenAI 分组默认使用新的目录流程，无需新增开关。普通 `/v1/models`、Codex manifest 及单模型查询共享型号选择规则；普通列表继续包含其原本支持的媒体型号，Codex manifest 继续只展示适用的语言模型。

分组选源、账号映射和白名单决定可见型号，描述符来源独立决定这些型号的字段：

1. 管理员提供的 `gpt-5.6-cyber`、`gpt-6-cyber` 完整附件。
2. 本组 OAuth 账号实际读取的官方目录。
3. 公开官方目录及最近成功缓存；首次无法读取时使用完整内置官方快照。
4. 官方未收录型号使用已有的合成描述符。

新流程不会因账号配置了模型映射而跳过官方源。组内 OAuth 来源按既有优先级选取，最多四个不同授权来源，共享请求时限。API Key-only 分组可直接使用公开官方元数据。精确白名单可补入存在合法账号路由的型号，通配符只展开已知候选。唯一映射别名使用目标模型描述符并修改公开名称。

“固定账号获取模型列表”仍控制可见型号来源：开启时仅合并选定账号提供的范围，再应用映射和白名单；成功的空目录保持为空。选定账号全部失败时是否回退，继续由原有选项决定。固定来源为 API Key 或 Engine 时也不会因公开官方目录额外增加未配置的候选。

官方已知型号保留全部原始字段，包括未知扩展字段及 `model_messages`；不再覆盖旧 Sol 500k 上下文或裁剪 Lite、搜索、code mode 声明。所有最终描述符的 `shell_type` 为 `unified_exec`。两份 Cyber 描述符保留附件中的所有字段与显式 null，仅修正 5.6 附件片段末尾的逗号。

目录反映客户端元数据；业务鉴权、路由、Daybreak 及计费继续走各自逻辑。公开快照和附件不写入 OAuth 真实能力证据缓存。Composite 保留原目录规则与原合成资源。

API 密钥的“使用密钥 → 下载模型目录”调用同一 manifest 接口；远程目录使用新规则，旧本地文件需要重新下载替换。OpenAI HTTP 和 WS 教程不再自动写入 `model_reasoning_effort`，由客户端模型目录默认值或用户选择决定。

### 官方来源与缓存

- 公共源固定为 `https://raw.githubusercontent.com/openai/codex/main/codex-rs/models-manager/models.json`，不携带账号凭据。
- 内置完整快照来自提交 `126a0ee92ed2c5e73cd8a8f7e37563e8527552d7`，包含 12 个型号。原来的合成基底单独保留，以避免改变其他目录路径。
- 公共源每六小时条件刷新，支持 ETag/Last-Modified、单飞、15 秒超时和 1 MiB 上限。Redis 只保留一个最近成功快照；失败不覆盖有效结果，失败重试有一分钟退避。
- OAuth 目录继续复用原有授权槽、授权代际、代理和请求头隔离缓存。分组过滤与 shell 修改只作用于投影副本；返回 ETag 对应最终内容。

## Cyber 推理档位

两个 Cyber 型号使用独立严格识别，不加入会影响 compact 和家族计价的宽泛 GPT 分类。实际出站的 `max` 不再误记为 `xhigh`。

Ultra 保留为原生 Codex 的客户端选择：客户端开启主动多智能体，并将推理强度解析为模型声明的 `multi_agent_reasoning_effort`，否则优先 `max`，再回退到最高受支持的普通档位。按本次附件，6 Cyber 的 Ultra 对应 `xhigh`，5.6 Cyber 对应 `max`。分组上限、强度映射、价格倍率及超时配置不新增独立 Ultra 档位；日志继续记录实际出站值。

入站强度记录保留用户选择，实际强度按最终映射型号及出站正文解析。HTTP、WS 后续轮和 Chat/Messages 转换共用该判断。历史记录和历史费用不重算，Daybreak 的入站型号规则保持独立。

## 审核内存

本次优化仍按完整原文匹配，保持关键词优先、组合规则语义、白名单、Guard 范围、证据坐标、脱敏及事件保留规则。

- 同步检测借用不可变正文，请求内共享文本提取结果；多组组合规则继续共享准备视图。
- 未命中本地规则时不提前生成无用的完整事件快照；真正记录事件或进入 Guard 时才生成所需元信息。
- 异步入队持有独立文本及必要身份字段，不保留完整请求 JSON 或图片 Base64。
- UTF-8 截断按边界扫描，避免先将全文展开成 rune 数组；短结果独立拥有存储，脱敏正则未命中时复用原文。
- HTTP 审核仍位于重试循环之前。429 窗口、次数及业务正文处理不变；不会跨账号复用可变请求树或最终出站正文。

性能基线为 `f823a9cec`，工具链 Go 1.27.3，`GOEXPERIMENT=greenteagc,jsonv2`。原始记录和比较见 `docs/performance/prompt-audit-20261111/`。生产采样仅作为问题背景，所有本轮性能验证使用合成输入。

## 升级与回退

无需数据库迁移或开启新选项。升级应用后新的 OpenAI 目录默认生效；已下载的本地目录需重新获取。Redis 公共目录快照为独立缓存，可在回退旧应用时保留。回退恢复旧目录和旧推理处理，因此新客户端本地目录也应与使用中的应用版本保持一致。

本次不自动部署、不修改线上规则、不调用生产模型。

## 验证记录

- 目录服务、HTTP handler、普通/manifest/单模型路由、公开目录 Redis 存储及 Cyber 强度定向测试通过；目录与 Cyber 并发路径通过 race，后端 `go build ./...` 通过。
- UseKeyModal 32 项、目录工具 5 项、i18n 完整性 3 项通过；前端类型检查、生产构建和改动文件 ESLint 通过。
- 独立本地 PostgreSQL 18.1 / Redis 8.4 中完成 securityaudit 全包测试，数据库集成未跳过；事件写入、claim fencing、事务回滚、稳定身份及 Redis TTL/删除测试通过。最终实现的全包 race 通过（25.441 秒）。原始记录保存在 `performance/prompt-audit-20261111/integration.jsonl` 和 `integration-race.txt`；按需启用的堆测量另外执行并保留三轮结果。
- 扩大目录回归时发现两个既有失败：`TestFetchCodexModelsManifestOAuth401OnlyCoolsSelectedAuthorization` 和 `TestFetchCodexModelsManifestOAuth401TokenRevokedOnlyDisablesSelectedAuthorization`。在未优化基线副本复现相同失败，原因是测试 fixture 未配置 OAuth account state repository；本次未删除授权保护或弱化断言。
- 本地验证不调用真实模型，仓库 Actions 未执行；审核分配、耗时及堆占用的三轮数据见 `prompt-audit-memory-20261111.md`。

## English

OpenAI groups use the unified catalog automatically. Standard model lists, Codex manifests and individual-model lookup share visibility rules; existing media-only differences are retained. Account mappings, group allowlists and pinned source accounts determine visible IDs. Descriptors prefer the supplied Cyber resources, then authenticated official OAuth metadata from this group, then the public official catalog (cached or bundled), and finally the existing synthesis for unknown models. Pinned sources retain authority over their candidate range, including a successful empty list.

Official and supplied Cyber descriptors preserve full metadata, nested instructions, explicit nulls and unknown fields. Old context-window and tool-capability overrides no longer modify known official entries. Every final descriptor uses `unified_exec`. Public metadata is separate from authenticated OAuth access evidence; existing authorization, routing, Daybreak, billing and Composite behavior remain in their own paths. Downloading a catalog from the API-key instructions uses the same manifest endpoint. Existing local files must be downloaded again. Generated OpenAI HTTP/WS configuration no longer forces a reasoning effort.

The public source refreshes conditionally every six hours, with a 15-second timeout, 1 MiB response limit, singleflight and a Redis last-good snapshot. Failures retain valid data; a cold failure uses the complete bundled official snapshot. OAuth cache identity remains credential- and authorization-scoped. Response ETags are generated after group projection.

Cyber models now retain the actual upstream `max` instead of misreporting it as `xhigh`. Ultra remains a native Codex client choice: it enables proactive multi-agent behavior and resolves to the model's `multi_agent_reasoning_effort`, then `max` or the highest supported ordinary effort. In the supplied descriptors, Ultra resolves to `xhigh` for GPT-6 Cyber and `max` for GPT-5.6 Cyber. No independent Ultra policy, multiplier or timeout tier is added. Requested effort and final upstream effort remain distinct. No historical usage or charges are rewritten.

Audit processing shares request-local extracted text, defers event preparation until required, removes redundant body clones, and transfers only independently owned text/identity data to asynchronous work. UTF-8 truncation avoids whole-string rune arrays. Matching scope, ordering, evidence, redaction, queue behavior and retry semantics remain unchanged. Baselines and post-change measurements use synthetic workloads and the same Go toolchain. No database migration is required; the independent public cache may remain during application rollback.

Targeted catalog, routing, Cyber and Redis tests, affected race checks, backend compilation, frontend tests, type checking, production build and targeted ESLint passed. Isolated PostgreSQL 18.1 / Redis 8.4 audit integration ran without skipped tests. Two pre-existing OAuth 401 fixture failures were reproduced in an unmodified baseline checkout and are reported separately above. No production model calls or automatic deployment were performed; repository Actions were not run.

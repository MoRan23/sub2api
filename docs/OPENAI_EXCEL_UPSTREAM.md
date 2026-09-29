# OpenAI OAuth Excel 上游

常规 OpenAI OAuth 账号可在账号编辑页启用“Excel 上游”。开关默认为关闭；Spark、API Key、PAT、setup-token、Agent Identity 账号不适用，Spark 也不会继承母账号设置。启用后，Responses、透传、Chat、Messages、HTTP bridge、账号连接测试和糖果题测试使用内置 Excel 适配器；关闭后继续使用原 Codex 路径。原生上游 WebSocket 不建立 Excel 连接，WS bridge 会按实际 HTTP 请求处理。

适配器使用固定的 `https://bps.openai.com/basispoints/api/`。进入 Excel 路径后，最终出站请求统一使用 Windows Excel WebView2 兼容 UA 模板（Windows 10 x64、Chrome/131、Edg/131），native HTTP transport 选择 Windows TLS。该模板是协议兼容配置，不表示来自真实抓取或本机安装的 Office 版本。这只覆盖 Excel 请求，普通 Codex 请求仍使用用户实际系统对应的 UA/TLS。installation ID、账号代理和会话身份仍来自冻结的账号请求计划。Excel 模型目录是本地内置目录，不是实时上游发现，支持 `gpt-5.6-luna`、`gpt-5.6-terra`、`gpt-5.6-sol`、`gpt-6-sol`、`gpt-6-luna`、`gpt-6-astra`，推理档位为 `low`、`medium`、`high`、`xhigh`，未指定时为 `medium`。

工具调用只转接到客户端，不在服务端执行。function、custom 和 namespace 调用及工具回传会校验账号、授权代次、路由代次、租户和会话范围。Redis 中只保存加密后的最小调用关联和附件 ID：调用关联最多 7 天，附件映射最多 24 小时；不保存 token、完整会话、工具输出或图片原文。缺少可证明的授权代次、跨后端 opaque 状态、未知 file ID、`previous_response_id` 单独续接或不支持的 `/responses/compact` 会明确返回错误，不静默丢历史或回退 Codex。客户端声明的 MCP、文件读写、命令执行、技能和可视化工具均可转接，不按类别禁用。只有没有可用客户端工具目录时，适配器才发送 `tool_choice=none`；协议提示只禁止本轮未声明的工具，不能根据对话中提到的工具名自行创建可执行工具。

工具转换不能以“丢掉不能转换的调用，再返回成功”降级。任何调用无法恢复、缺少真实 `call_id` 或违反关闭并行的约束，整次响应都明确失败，不把前面的准备说明包装成完成。启用客户端工具时，仅有明确 `commentary`、没有最终回答或工具调用的响应也会失败；普通简短回答不按字数或内容猜测是否完成。工具协议提示包含实际行动要求和稳定前缀提醒，但不会增加隐式推理重试。

SSE 必须收到真实的完成终态。终态省略输出或输出为空时，可以使用本次流中已完成且索引连续的 item 恢复输出；不能从 EOF、`[DONE]` 或单独的 item.done 推断成功。已宣布的工具不得在终态消失或改变关联。长思考的 15 秒进度保活只沿用已收到的响应身份，取消和连接关闭仍会停止读取。转换失败记录固定原因码（如 `invalid_tool_call`、`commentary_without_action`、`incomplete_output_items`），不记录工具参数、正文或原始上游流。

工具名称同时支持声明中的全限定名称和独立 `namespace` 字段；直接调用与 `run_officejs` 内层使用同一声明匹配规则。按 Codex 的规则，未填写与显式 `functions` 属于同一默认命名空间；其他命名空间仍精确匹配。默认空间中有多个同名声明时明确拒绝，不猜测要执行哪个。原生 `update_plan` 的参数和结果回传同样识别默认空间；保存与回传的原生调用保持原样。未声明工具、缺少真实调用 ID 仍会失败。

工具目录同时读取顶层 `tools` 和 Codex Lite 的结构化 `input[].type=additional_tools` 中的 `tools`。Lite 请求可以没有顶层目录，这不表示不能调用工具。适配器读取声明后将其转换为 Excel 的客户端工具目录，保留完整名称、命名空间及参数定义；载体重复携带的完全相同定义合并，同名冲突明确拒绝。普通消息中的文字或 `tools` 字段不作为声明，不能从技能说明或历史文本猜测工具权限。

工具回传保留空字符串、空数组及结构化图片结果，不把空结果改写为成功说明。输出字段缺失或为 `null` 时明确拒绝；已声明原生计划工具继续使用其既有结果协议。中继结果通过 `call_id` 关联到保存的原生调用，移除客户端附带的 `name` / `namespace`，避免把 MCP 名称错当成 Excel 原生工具身份。

`invalid_tool_call` 会附带固定细分原因（如 `relay_tool_undeclared`、`function_schema_mismatch`、`call_id_missing`）。客户端错误不含工具名或参数。服务端 `openai.excel_response_translation_failed` 日志包含 `reason`、`tool_reason` 和 `tool_diagnostic`：原生/候选工具名与命名空间、失败阶段、参数类型与长度、调用 ID 是否存在、匹配结果、默认空间候选数量，以及排序后最多 32 项声明工具（另记总数和截断标记）。标识符只记录完整合法且不超过 128 字节的名称；其他内容整段隐藏。日志不记录参数键值、schema、工具描述、调用 ID 值、请求头、令牌、图片或业务正文。用同条日志的请求 ID 关联实际账号与业务错误记录。

排错时先查上述日志事件及请求 ID，再查看以下字段；`tool_diagnostic.version=1`。诊断只对新版实际处理的失败生成，不能补回历史请求的工具信息。

| 字段 | 排查用途 |
| --- | --- |
| `stage`、`tool_reason` | 区分外层参数、转接 JSON、声明匹配、调用 ID、输入/schema 和历史保存失败 |
| `native_name`、`candidate_name`、对应 `namespace` | 区分上游直接调用的工具与 `run_officejs` 内层目标；名称在 `value` 中，被隐藏时显示 `redacted` |
| `transport`、`relay_depth` | 是否经过转接，是否发生多层嵌套 |
| `name_matches_declared`、`matched_name`、`matched_namespace` | 查找是否成功，以及最终匹配的客户端声明 |
| `default_namespace_candidates` | 当前候选名在默认空间的声明数量；大于 1 表示歧义，别名匹配结果仍以 `name_matches_declared` 为准 |
| `catalog_total`、`catalog_truncated`、`catalog` | 核对本轮实际声明的工具；清单截断时不能将未显示误当作未声明 |

图片输入通过受限 URL 获取或附件上传，单次最多 64 个输入，解码及 multipart 总工作量最多 64 MiB；Excel Images API 使用 `gpt-image-2`，结果按实际返回图片计数。Responses 内置 `image_generation` 不自动注入，显式请求该能力会返回不支持。图片生成、编辑、附件上传和工具转接均使用账号代理，以及上述 Windows Excel UA/TLS 规则。

Excel 路径仅写本地用量、错误、模型证据、系统、TLS、代理和耗时观测，不发送 Codex Analytics、OTLP、模拟活动或 Guardian 辅助请求。账号开关变化会推进私有路由代次；排队中的糖果测试在发送前和心跳时检查代次，切换后以配置变化结束，不重试或换模型。糖果题每项仍只发送一次推理请求，并保留取消、账号状态隔离和 30 分钟截止时间。

配置保存在 `extra.openai_excel_upstream_enabled`；导入导出可以保留此开关，私有路由代次和 Redis 运行态不参与备份或 `auth.json`。本次没有新增数据库表或修改已应用迁移，糖果排队项在已有 execution JSON 内保存私有路由快照，管理接口不会返回该私有代次。

升级时应先排空旧进程，再启动包含适配器的版本，避免旧节点用旧配置快照覆盖开关或路由代次。各实例必须连接同一个 Redis，并配置同一套现有 AES 加密密钥（`totp.encryption_key`）；缓存或解密不可用时，需要工具历史的请求会失败，不会明文降级或借用别的账号。验证码加密设施仅复用加密器，不在 Redis 中保存账号 token。

没有开启过 Excel 的账号继续使用原协议来源保护。切换过后，新请求和已有 WS 的下一轮会检查私有路由代次；已开始发送的普通请求沿用原快照完成，WS 发生切换时要求重连。Codex 与 Excel 的 opaque 状态不能跨后端使用，切换后应开始新会话。普通 OAuth 刷新不推进路由代次，授权、用量查询、额度重置也不改用 Excel。

实现参考本地 `excel-codex-bridge@9254d3f25b0323bbf71db4f25a242fe32027a544`（Unlicense），原生移植到 Go，不需要 Python 服务。验证使用本地模拟上游、合成凭据和隔离 Redis，没有发送真实业务、授权、生图或遥测请求。

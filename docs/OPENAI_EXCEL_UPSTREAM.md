# OpenAI OAuth Excel 上游

常规 OpenAI OAuth 账号可在账号编辑页启用“Excel 上游”。开关默认为关闭；Spark、API Key、PAT、setup-token、Agent Identity 账号不适用，Spark 也不会继承母账号设置。启用后，Responses、透传、Chat、Messages、HTTP bridge、账号连接测试和糖果题测试使用内置 Excel 适配器；关闭后继续使用原 Codex 路径。原生上游 WebSocket 不建立 Excel 连接，WS bridge 会按实际 HTTP 请求处理。

适配器使用固定的 `https://bps.openai.com/basispoints/api/`。进入 Excel 路径后，最终出站请求统一使用 Windows Excel WebView2 兼容 UA 模板（Windows 10 x64、Chrome/131、Edg/131），native HTTP transport 选择 Windows TLS。该模板是协议兼容配置，不表示来自真实抓取或本机安装的 Office 版本。这只覆盖 Excel 请求，普通 Codex 请求仍使用用户实际系统对应的 UA/TLS。installation ID、账号代理和会话身份仍来自冻结的账号请求计划。Excel 模型目录是本地内置目录，不是实时上游发现，支持 `gpt-5.6-luna`、`gpt-5.6-terra`、`gpt-5.6-sol`、`gpt-6-sol`、`gpt-6-luna`、`gpt-6-astra`，推理档位为 `low`、`medium`、`high`、`xhigh`，未指定时为 `medium`。

工具调用只转接到客户端，不在服务端执行。function、custom 和 namespace 调用及工具回传会校验账号、授权代次、路由代次、租户和会话范围。Redis 中只保存加密后的最小调用关联和附件 ID：调用关联最多 7 天，附件映射最多 24 小时；不保存 token、完整会话、工具输出或图片原文。缺少可证明的授权代次、跨后端 opaque 状态、未知 file ID、`previous_response_id` 单独续接或不支持的 `/responses/compact` 会明确返回错误，不静默丢历史或回退 Codex。

图片输入通过受限 URL 获取或附件上传，单次最多 64 个输入，解码及 multipart 总工作量最多 64 MiB；Excel Images API 使用 `gpt-image-2`，结果按实际返回图片计数。Responses 内置 `image_generation` 不自动注入，显式请求该能力会返回不支持。图片生成、编辑、附件上传和工具转接均使用账号代理，以及上述 Windows Excel UA/TLS 规则。

Excel 路径仅写本地用量、错误、模型证据、系统、TLS、代理和耗时观测，不发送 Codex Analytics、OTLP、模拟活动或 Guardian 辅助请求。账号开关变化会推进私有路由代次；排队中的糖果测试在发送前和心跳时检查代次，切换后以配置变化结束，不重试或换模型。糖果题每项仍只发送一次推理请求，并保留取消、账号状态隔离和 30 分钟截止时间。

配置保存在 `extra.openai_excel_upstream_enabled`；导入导出可以保留此开关，私有路由代次和 Redis 运行态不参与备份或 `auth.json`。本次没有新增数据库表或修改已应用迁移，糖果排队项在已有 execution JSON 内保存私有路由快照，管理接口不会返回该私有代次。

升级时应先排空旧进程，再启动包含适配器的版本，避免旧节点用旧配置快照覆盖开关或路由代次。各实例必须连接同一个 Redis，并配置同一套现有 AES 加密密钥（`totp.encryption_key`）；缓存或解密不可用时，需要工具历史的请求会失败，不会明文降级或借用别的账号。验证码加密设施仅复用加密器，不在 Redis 中保存账号 token。

没有开启过 Excel 的账号继续使用原协议来源保护。切换过后，新请求和已有 WS 的下一轮会检查私有路由代次；已开始发送的普通请求沿用原快照完成，WS 发生切换时要求重连。Codex 与 Excel 的 opaque 状态不能跨后端使用，切换后应开始新会话。普通 OAuth 刷新不推进路由代次，授权、用量查询、额度重置也不改用 Excel。

实现参考本地 `excel-codex-bridge@9254d3f25b0323bbf71db4f25a242fe32027a544`（Unlicense），原生移植到 Go，不需要 Python 服务。验证使用本地模拟上游、合成凭据和隔离 Redis，没有发送真实业务、授权、生图或遥测请求。

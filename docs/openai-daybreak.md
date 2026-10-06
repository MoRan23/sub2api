# OAuth 账号的 Daybreak 开关

在**账号管理 → 编辑 OpenAI OAuth 账号 → Daybreak**中读取上游能力，再开启 Blue 或 Red。新建账号默认关闭；Red 必须同时开启 Blue，关闭 Blue 会一起关闭 Red。API Key、Codex-Engine、Setup Token、PAT 和 Agent Identity 不提供这两个开关。

开关控制 Sub2API 自动补充请求参数，不代表授予官方访问权限，也不是强制禁用策略：客户端已填写 `access_programs.cyber` 时保持原值；开关关闭时不会补 `standard` 或删除客户端字段，上游仍可能按自身默认策略选择程序。

## 按模型填写请求

模型映射完成后，只有当前 OAuth 授权的真实模型目录 `available_access_programs.cyber` 明确支持目标值，Sub2API 才会补充。档位和请求值不是同一个概念：

| 模型 | 所需开关 | 自动补充值 |
| --- | --- | --- |
| GPT-5.5、5.6 Sol、6 Sol、6 Luna、`gpt-daybreak-blue-latest` | Blue | `daybreak_blue` |
| GPT-6 Astra、6.1 Sol | Blue + Red | `daybreak_blue` |
| GPT-5.5 Cyber、5.6 Cyber、`gpt-daybreak-red-latest` | Blue + Red | `daybreak_red` |

例如 Astra 获得相应授权后，发送的字段是 `"access_programs":{"cyber":"daybreak_blue"}`，而不是 `daybreak_red`。未知型号不会按名称前缀推断，也不会自动改成 Daybreak alias。规则依据 [官方档位说明](https://help.openai.com/en/articles/20001258-openai-daybreak-trusted-access-for-cyber-overview)及 [Responses 参数文档](https://developers.openai.com/api/docs/guides/daybreak)，具体可用性始终以账号上游目录为准。

HTTP Responses、WS `response.create`，以及 Chat／Messages 转换后的 Responses 使用相同规则。独立压缩、计数和 WS 预热不会自动添加；正常 Responses 中的 `compaction_trigger` 不影响补充。客户端显式值（包括 `null` 或其他无效值）交由上游验证。

## 能力与配置

- 管理员接口：`GET /api/v1/admin/accounts/:id/daybreak-capabilities`，返回检查时间、可启用档位、支持模型和原因。
- 账号偏好保存在 `extra.openai_daybreak_blue_enabled`、`extra.openai_daybreak_red_enabled`，默认均为 `false`；不需要数据库迁移。
- 静态目录、分组名及导入文件不能作为能力证明。查询失败或没有能力证据时禁止开启，始终允许关闭。
- 能力沿用账号目录缓存及单飞刷新，按凭据所有者、授权槽位和授权代际隔离。普通令牌刷新保留开关；重新授权后的请求重新检查能力。
- 共享凭据账号各自保存开关，能力来自实际凭据账号；不会互相继承开关。
- 辅助目录读取失败时，本次推理保持原请求，不因该失败改变账号健康状态。关闭开关或客户端已经指定时不额外查询目录。
- 导出保留开关偏好。新导入账号默认关闭；导入内容要求开启时返回警告，需完成授权后在编辑页重新验证。更新已有账号且未修改开关时保留原偏好。

自动添加只发生在实际推理发送前，原始重试正文和预热正文保持未注入状态。换号后依据新账号的开关、模型映射及能力重新判断。

## 指纹观测

开启指纹观测后，展开具体请求的详情，可查看 **Daybreak 出站字段**：实际出站的 `access_programs.cyber`、字段来源（自动补充、客户端指定、未自动补充）及本次原因，例如开关关闭、Red 未开启、目录获取失败或能力不足。摘要也会显示字段值和来源。

观测覆盖 HTTP 请求及 WS 每个实际发送的推理帧；重试、换号和后续轮分别记录，不沿用上一轮结论。它证明发送了什么，不代表上游已接受或启用 Daybreak。WS 握手没有正文；旧记录显示未采集，不能回填历史注入情况。没有补充决策的传输路径只显示实际字段并标记来源未采集。

此项沿用指纹观测的内存保留、关闭清理和分页快照机制，不保存完整正文、模型回答或凭据。无效的对象／数组值只记录类型，标量值最多记录 128 个字符，且不会截断实际发送的请求。

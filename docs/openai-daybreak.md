# Daybreak 系统、分组与 OAuth 账号开关

在**系统设置 → 网关**中设置 Daybreak 总开关，默认开启。在 **OpenAI／Composite 分组的创建或编辑页**允许对应的 Blue／Red 档位，再到**账号管理 → 编辑 OpenAI OAuth 账号 → Daybreak**读取上游能力并开启账号档位。分组与账号的 Red 均依赖 Blue，关闭 Blue 会一起关闭 Red。

**升级后已有分组的 Blue／Red 均为关闭，需要管理员手动开启才能恢复自动补充。**新建和复制分组也默认关闭。账号偏好保留，不会随总开关或分组开关关闭而清空。

| 条件 | 出站行为 |
| --- | --- |
| 系统关闭 | 不自动补充，并删除 OpenAI 账号出站请求的 `access_programs.cyber`，包括客户端显式值 |
| 系统开启，分组或账号未允许所需档位 | 不自动补充，保留客户端原有字段 |
| 系统、分组、账号均允许，且真实目录支持 | 仅在客户端未指定 `access_programs.cyber` 时自动补充 |

自动补充仅适用于 OpenAI OAuth；API Key、Codex-Engine、Setup Token、PAT 和 Agent Identity 不提供账号 Blue／Red 开关，但它们作为 OpenAI 账号发送请求时仍受系统关闭的清除策略约束。其他平台不受影响。删除字段不代表授予或撤销官方权限，也不添加 `standard`；上游仍可能采用自身默认行为。

## 按模型填写请求

模型映射完成后，只有请求所属分组与所选 OAuth 账号同时开启所需档位，且当前 OAuth 授权的真实模型目录 `available_access_programs.cyber` 明确支持目标值，Sub2API 才会补充。档位和请求值不是同一个概念：

| 模型 | 分组与账号均需开启 | 自动补充值 |
| --- | --- | --- |
| GPT-5.5、5.6 Sol、6 Sol、6 Luna、`gpt-daybreak-blue-latest` | Blue | `daybreak_blue` |
| GPT-6 Astra、6.1 Sol | Blue + Red | `daybreak_blue` |
| GPT-5.5 Cyber、5.6 Cyber、`gpt-daybreak-red-latest` | Blue + Red | `daybreak_red` |

例如 Astra 获得相应授权后，发送的字段是 `"access_programs":{"cyber":"daybreak_blue"}`，而不是 `daybreak_red`。未知型号不会按名称前缀推断，也不会自动改成 Daybreak alias。规则依据 [官方档位说明](https://help.openai.com/en/articles/20001258-openai-daybreak-trusted-access-for-cyber-overview)及 [Responses 参数文档](https://developers.openai.com/api/docs/guides/daybreak)，具体可用性始终以账号上游目录为准。

HTTP Responses、WS `response.create`，以及 Chat／Messages 转换后的 Responses 使用相同规则。独立压缩、计数和 WS 预热不会自动添加；正常 Responses 中的 `compaction_trigger` 不影响补充。系统开启时，客户端显式值（包括 `null` 或其他无效值）交由上游验证。

分组来自当前请求的可信授权信息，Composite 使用父分组配置，不从账号绑定的多个分组中挑选。没有明确请求分组时（例如管理员直接测试账号）不自动补充。

系统关闭的清除策略也覆盖独立压缩、计数、图片、搜索、Engine 直转、账号诊断和 WS 预热等入口。只删除请求级 `access_programs.cyber`，保留同对象中的其他字段；删除后为空则保留 `{}`。历史、工具参数、JSON Schema 及用户内容里的同名字段不变。multipart 图片只清理非文件的 `access_programs` JSON 表单字段，图片内容和其他参数保持不变。

## 能力与配置

- 管理员接口：`GET /api/v1/admin/accounts/:id/daybreak-capabilities`，返回检查时间、可启用档位、支持模型和原因。
- 系统设置通过现有管理员设置接口保存 `openai_daybreak_enabled`，缺失时默认 `true`。省略或传入 `null` 不覆盖原值。
- 分组通过现有分组接口保存 `openai_daybreak_blue_enabled`、`openai_daybreak_red_enabled`；新增迁移为已有分组写入 `false` 默认值。部分更新省略字段时保留原值，切换到其他平台时清零。
- 账号偏好保存在 `extra.openai_daybreak_blue_enabled`、`extra.openai_daybreak_red_enabled`，默认均为 `false`。
- 静态目录、分组名及导入文件不能作为能力证明。查询失败或没有能力证据时禁止开启，始终允许关闭。
- 能力沿用账号目录缓存及单飞刷新，按凭据所有者、授权槽位和授权代际隔离。普通令牌刷新保留开关；重新授权后的请求重新检查能力。
- 共享凭据账号各自保存开关，能力来自实际凭据账号；不会互相继承开关。
- 辅助目录读取失败时，本次推理保持原请求，不因该失败改变账号健康状态。系统关闭、分组／账号未允许或客户端已经指定时不额外查询目录。
- 导出保留开关偏好。新导入账号默认关闭；导入内容要求开启时返回警告，需完成授权后在编辑页重新验证。更新已有账号且未修改开关时保留原偏好。

自动添加只发生在实际推理发送前，原始重试正文和预热正文保持未注入状态。换号后依据新账号的开关、模型映射及能力重新判断。

系统设置在本进程保存成功后立即生效，其他实例最长约 60 秒刷新。读取失败时暂用最近有效值并短期重试，首次没有有效值时默认开启。每次物理请求或 WS 帧发送重新取值，不冻结到整条连接。HTTP 使用当前鉴权分组快照；长连接 WS 在需要自动补充的新一轮按可信分组 ID 读取配置，分组失效或读取失败时跳过补充，不阻断原请求。

## 指纹观测

开启指纹观测后，展开具体请求的详情，可查看 **Daybreak 出站字段**：实际出站的 `access_programs.cyber`、字段来源及本次原因，包括系统关闭并已移除、系统关闭但无需移除、分组未允许、没有有效分组、账号开关关闭、Red 未开启、目录获取失败或能力不足。摘要也会显示字段值和来源。

观测覆盖 HTTP 请求及 WS 每个实际发送的推理帧；重试、换号和后续轮分别记录，不沿用上一轮结论。它证明发送了什么，不代表上游已接受或启用 Daybreak。WS 握手没有正文；旧记录显示未采集，不能回填历史注入情况。没有补充决策的传输路径只显示实际字段并标记来源未采集。

此项沿用指纹观测的内存保留、关闭清理和分页快照机制，不保存完整正文、模型回答或凭据。无效的对象／数组值只记录类型，标量值最多记录 128 个字符，且不会截断实际发送的请求。

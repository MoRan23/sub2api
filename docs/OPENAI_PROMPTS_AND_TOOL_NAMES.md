# OpenAI/Codex 提示词与工具名

## 业务请求

OpenAI/Codex 最终发送为 Responses 的请求恢复缺省 Codex 指令，包括原生 Responses、Responses 透传、Chat/Messages 转 Responses，以及 HTTP/WS 桥接和原生 WS。仅当 `instructions` 缺失、为 null 或空白，且没有非空 system/developer 提示词时，才按最终上游模型补入官方模板。已有提示词按原协议规则传递，不追加默认模板。原生第三方 Chat Completions 转发不扩大补入范围。

显式配置的强制模板、TODO guard、生图与 Spark 专属指令继续遵循原有规则。缺省模板在最终模型确定后选择；Messages 的内置 TODO guard 不被当成用户提供了提示词。非字符串等非法指令不会被默认模板掩盖，仍交由原字段校验处理。

Python 保留名兼容补丁已移除：不再把 `python` 自动改成 `python_exec`，也不再反向改写响应中的工具名。声明、tool choice、历史调用和返回调用中的这两个名称分别保留，可以同时使用。

## 服务端主动构造的测试与模型目录

账号连接测试、compact 测试、服务端合成模型目录与业务缺省指令共用按模型选择的官方模板。独立 turn-state 采集仍使用其专用短请求指令。

当前官方模板核对并固定自 [openai/codex 的 models.json，提交 d1092865f8ec63735006211f65ee109ce91c30b9](https://github.com/openai/codex/blob/d1092865f8ec63735006211f65ee109ce91c30b9/codex-rs/models-manager/models.json)（2026-09-22 的 main），取 `model_messages.instructions_template`。既有模板的有效正文与此次最新目录一致：

| 模型 | 本地模板 |
| --- | --- |
| GPT-6 Astra | `instructions_gpt6_astra.txt` |
| GPT-5.6 Sol / Terra / Luna | `instructions_gpt5_6.txt` |
| GPT-5.5 | `instructions_gpt5_5.txt` |
| GPT-5.4 | `instructions_gpt5_4.txt` |
| Daybreak Blue / Codex Auto Review | `instructions_daybreak_blue.txt` |
| Daybreak Red | `instructions_daybreak_red.txt` |

上述文件位于 `backend/internal/pkg/openai/`。官方目录未包含的旧模型保留既有兼容模板；未知模型保留 GPT-5.5 模板回退，这不代表上游模型映射。上游已经提供的模型目录提示词保持原样。

## 请求完整性检查

检查器先清除客户端私有消息元数据，再提升 system 消息，与实际转发顺序一致。只有客户端确实缺少提示词，且出站指令精确匹配最终模型的官方模板时，才将默认补入解释为已知转换；任意新增指令仍报告差异。Spark 等专属指令追加沿用原检查范围，不因包含默认模板前缀就自动豁免。

`input` 按内容和顺序做有界对齐，单项删除、插入或移动不再造成后续所有数组下标连续报差异。`input.before[n]` 表示入站项被删除，`input.after[n]` 表示出站新增项；`input.before[n].after[m].arguments` 等表示对齐后同一项的字段变化。移动仍会报告，工具参数等实际改写不会被忽略。对齐超过预算时保守报告整个 `input` 差异，仍保留 32 项显示上限与原脱敏限制。

转换规则表示比较时采用的规范化规则，不是逐步修改日志。旧观测摘要不重新计算。

## 验证范围

使用模拟出站与本地 WS 服务验证 OAuth/API Key、Responses 透传开关、Chat/Messages、WS 首帧与后续帧：工具名保持原样，缺失提示词按最终模型补入，已有提示词继续传递。

相关 Go 回归覆盖模板按模型选择、保留上游模型目录提示词、完整性观测以及请求/响应工具名；完整性用例覆盖带元数据的 system 提升、真实指令丢失、插入/删除/移动、重复项、移位后参数修改、预算及显示上限，并运行相关竞态测试。前端测试验证对齐位置说明；类型检查与定向 lint 验证展示改动。验证不发送真实收费请求。

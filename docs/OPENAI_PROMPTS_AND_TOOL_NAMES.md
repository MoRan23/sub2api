# OpenAI/Codex 提示词与工具名

## 业务请求

OpenAI/Codex 最终发送为 Responses 的请求恢复缺省 Codex 指令，包括原生 Responses、Responses 透传、Chat/Messages 转 Responses，以及 HTTP/WS 桥接和原生 WS。仅当 `instructions` 缺失、为 null 或空白，且没有非空 system/developer 提示词时，才按最终上游模型补入官方模板。已有提示词按原协议规则传递，不追加默认模板。原生第三方 Chat Completions 转发不扩大补入范围。

显式配置的强制模板、TODO guard、生图与 Spark 专属指令继续遵循原有规则。缺省模板在最终模型确定后选择；Messages 的内置 TODO guard 不被当成用户提供了提示词。非字符串等非法指令不会被默认模板掩盖，仍交由原字段校验处理。

Python 保留名兼容补丁已移除：不再把 `python` 自动改成 `python_exec`，也不再反向改写响应中的工具名。声明、tool choice、历史调用和返回调用中的这两个名称分别保留，可以同时使用。

## 服务端主动构造的测试与模型目录

账号连接测试、compact 测试、服务端合成模型目录与业务缺省指令共用按模型选择的官方模板。独立 turn-state 采集仍使用其专用短请求指令。

当前官方模板核对并固定自 [openai/codex 的 models.json，提交 24462234b2aeeb27373e17bbe226baf9c0e97d3b](https://github.com/openai/codex/blob/24462234b2aeeb27373e17bbe226baf9c0e97d3b/codex-rs/models-manager/models.json)（2026-09-23 的 main），逐模型复制 `model_messages.instructions_template`：

| 模型 | 本地模板 |
| --- | --- |
| GPT-6 Astra | `instructions_gpt6_astra.txt` |
| GPT-6 Sol | `instructions_gpt6_sol.txt` |
| GPT-6 Luna | `instructions_gpt6_luna.txt` |
| GPT-5.6 Sol / Terra / Luna / Codex Auto Review | `instructions_gpt5_6.txt` |
| GPT-5.5 | `instructions_gpt5_5.txt` |
| GPT-5.4 | `instructions_gpt5_4.txt` |
| Daybreak Blue | `instructions_daybreak_blue.txt` |
| Daybreak Red | `instructions_daybreak_red.txt` |

上述文件位于 `backend/internal/pkg/openai/`。官方目录未包含的旧模型保留既有兼容模板；未知模型保留 GPT-5.5 模板回退，这不代表上游模型映射。上游已经提供的模型目录提示词保持原样。

本次增加 GPT-6 Sol / Luna 的独立模板和默认目录条目，保留既有默认模型顺序。更新 Astra 模板的消息发送授权段落及 GPT-5.6 模板的标题大小写；Codex Auto Review 改为其当前官方模板，与 GPT-5.6 三个变体一致，不再借用 Daybreak Blue。新增模型仅按精确名称及既有拼写规范化规则匹配，不推测日期后缀或 `-latest` 别名。

当前快照的 `instructions_variables` 均为 null；GPT-6 模板中的 `{{connector_id}}` 属于 Apps 链接语法示例，原样保留，不作为待填入变量。模型目录附加元数据与提示词字段保存在 `backend/internal/service/openai_codex_model_defaults.json`，主指令模板仍以本表文件为准。

### 2026-09-23 目录默认值

合成目录对已知模型加载同一官方快照的元数据，包括 shell、推理档位、上下文、服务等级，以及 `model_messages` 中的权限和多代理等字段。GPT-6 Astra 默认推理为 `low`，Sol / Luna 为 `medium`；三者默认上下文为 272,000、最大上下文为 872,000，Luna 不提供 Ultra 工作流。GPT-5.6 Sol 的本地默认目录不再声明此次官方快照已移除的 `ultrafast`；上游明确提供的等级仍保留。

这份快照用于补齐目录默认值，不替换账号模型映射、分组名单或路由能力判断。官方目录的套餐可见性、退役／升级提示及 WebSocket 偏好不改变本项目的可见模型和传输选择。无账号能力证据时继续使用 text-only、关闭搜索和 Lite；官方 OAuth 的 GPT-6 系列可按账号路径声明 Lite，API Key 的 GPT-6 Sol / Luna 继续强制使用完整 Responses。已有上游模型元数据和提示词优先，配置中的自定义上下文不会被静态默认值覆盖。此次不修改价格、turn-state 生效模型名单或已保存账号配置。

更新快照时，从固定提交读取官方 `models.json`，仅移除各条目 `model_messages.instructions_template` 后保存 JSON，其余源字段保留；主模板按本表单独复制。服务端只投影自身支持的字段及本地路由策略，不执行目录中的任何指令。

## 请求完整性检查

检查器先清除客户端私有消息元数据，再提升 system 消息，与实际转发顺序一致。只有客户端确实缺少提示词，且出站指令精确匹配最终模型的官方模板时，才将默认补入解释为已知转换；任意新增指令仍报告差异。Spark 等专属指令追加沿用原检查范围，不因包含默认模板前缀就自动豁免。

`input` 按内容和顺序做有界对齐，单项删除、插入或移动不再造成后续所有数组下标连续报差异。`input.before[n]` 表示入站项被删除，`input.after[n]` 表示出站新增项；`input.before[n].after[m].arguments` 等表示对齐后同一项的字段变化。移动仍会报告，工具参数等实际改写不会被忽略。对齐超过预算时保守报告整个 `input` 差异，仍保留 32 项显示上限与原脱敏限制。

转换规则表示比较时采用的规范化规则，不是逐步修改日志。旧观测摘要不重新计算。

## 验证范围

使用模拟出站与本地 WS 服务验证 OAuth/API Key、Responses 透传开关、Chat/Messages、WS 首帧与后续帧：工具名保持原样，缺失提示词按最终模型补入，已有提示词继续传递。

相关 Go 回归覆盖模板按模型选择、保留上游模型目录提示词、完整性观测以及请求/响应工具名；完整性用例覆盖带元数据的 system 提升、真实指令丢失、插入/删除/移动、重复项、移位后参数修改、预算及显示上限，并运行相关竞态测试。前端测试验证对齐位置说明；类型检查与定向 lint 验证展示改动。验证不发送真实收费请求。

### 2026-09-23 目录更新验证

- 官方快照中的 11 个模型主模板已逐项核对；本地模板与源模板内容一致。
- `internal/pkg/openai` 全部测试通过；目录、能力、模板、别名、推理档位、完整性默认指令及相关 handler 回归通过，并通过 `-race`。
- 前端模型名单相关 22 项测试、完整类型检查和改动文件定向 lint 通过。
- 扩展目录回归中的 `TestFetchCodexModelsManifestOAuth401OnlyCoolsSelectedAuthorization`、`TestFetchCodexModelsManifestOAuth401TokenRevokedOnlyDisablesSelectedAuthorization` 缺少账号状态仓储测试配置，在修改前的 `bf78cc394` 隔离基线也出现相同失败；最终定向竞态验证显式排除这两项，未修改授权逻辑。
- 未运行全仓测试、前端构建或浏览器验收；本次前端仅更新模型候选数据。未部署，未发送真实业务、采集、授权或遥测请求。

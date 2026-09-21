# OpenAI/Codex 提示词与工具名

## 业务请求

OpenAI/Codex 的 Responses、Chat/Messages 桥接及 HTTP/WS 转发不再因为客户端未提供提示词而填入默认 Codex base instructions。客户端提供的 `instructions`、system/developer 消息仍按各入口的协议规则传递。

这次调整只移除默认提示词的缺省补入。显式配置的强制模板、TODO guard、生图与 Spark 专属指令仍遵循原有规则；原有字段校验也保持不变。例如 OAuth Responses 透传原本对显式空值或无效 `instructions` 的校验仍然有效。

Python 保留名兼容补丁已移除：不再把 `python` 自动改成 `python_exec`，也不再反向改写响应中的工具名。声明、tool choice、历史调用和返回调用中的这两个名称分别保留，可以同时使用。

## 服务端主动构造的测试与模型目录

账号连接测试、compact 测试和服务端合成的模型目录继续使用按模型选择的 Codex 提示词。普通业务转发不会读取这些模板来补入缺失提示词。独立 turn-state 采集仍使用其专用短请求指令。

当前官方模板固定自 [openai/codex 的 models.json，提交 6149914a0e59363b6777080b3e953b05d592dbac](https://github.com/openai/codex/blob/6149914a0e59363b6777080b3e953b05d592dbac/codex-rs/models-manager/models.json)，取 `model_messages.instructions_template`：

| 模型 | 本地模板 |
| --- | --- |
| GPT-6 Astra | `instructions_gpt6_astra.txt` |
| GPT-5.6 Sol / Terra / Luna | `instructions_gpt5_6.txt` |
| GPT-5.5 | `instructions_gpt5_5.txt` |
| GPT-5.4 | `instructions_gpt5_4.txt` |
| Daybreak Blue / Codex Auto Review | `instructions_daybreak_blue.txt` |
| Daybreak Red | `instructions_daybreak_red.txt` |

上述文件位于 `backend/internal/pkg/openai/`。官方目录未包含的旧模型保留既有兼容模板；未知模型保留 GPT-5.5 模板回退，这不代表上游模型映射。上游已经提供的模型目录提示词保持原样。

## 验证范围

使用模拟出站与本地 WS 服务验证 OAuth/API Key、Responses 透传开关、Chat/Messages、WS 首帧与后续帧：工具名保持原样，缺失提示词不会补入默认模板，已有提示词继续传递。

相关 Go 回归覆盖模板按模型选择、保留上游模型目录提示词、完整性观测以及请求/响应工具名；并运行相关竞态测试。验证不发送真实收费请求。

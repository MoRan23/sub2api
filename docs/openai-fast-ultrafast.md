# OpenAI Fast / Ultrafast 控制

## 设置与优先级

OpenAI 和 Composite 分组支持强制 Fast、强制 Ultrafast、免费 Fast、免费 Ultrafast。OpenAI 账号（OAuth、普通 API Key、Codex-Engine、影子账号）支持相同四项偏好。新增设置默认关闭，已有分组 Fast 设置保持原值。

- 同一层的两个强制开关互斥。界面自动关闭另一个；接口拒绝最终组合同时开启。部分更新若要切换档位，应同时提交旧档位 `false` 和新档位 `true`。
- 强制来源按账号、分组、客户端依次决定。账号开启 Ultrafast 后，即使能力不足，也不回退使用分组强制 Fast。
- 两个免费开关独立，可同时启用，无需开启强制。账号或分组任一开启对应免费标志即可生效。
- 影子账号使用自己的业务设置；凭据来源只提供认证与能力。非 OpenAI 路由不受影响。
- 设置保存后影响新请求；既有 WebSocket 会话沿用设置快照，重连后更新。每一轮仍重新检查能力观察是否有效。

## 出站策略与真实能力

Fast 写入 `service_tier="priority"`，Ultrafast 写入 `service_tier="ultrafast"`。强制 Ultrafast 需要所选账号的原始上游目录对最终映射模型明确声明 `service_tiers[].id == "ultrafast"`。OAuth 按真实授权槽位查询；API Key 和 Engine 使用各自上游。

只含模型 ID、缺失或空声明、降级目录、查询失败或过期证据均跳过自动强制，保留客户端档位。公开目录、内置描述符、别名推测和 `additional_speed_tiers` 不提供此项证据。客户端自己指定 Ultrafast 时按既有策略处理。

能力索引在目录刷新时构建，缓存命中只共享只读索引；有效观察期为 60 秒。过期缓存触发已有刷新，本轮跳过；冷缓存最多等待 2 秒，失败不改变账号健康。缓存沿用 URL、凭据、代理及 OAuth 授权隔离。无强制 Ultrafast 时不为该功能查询目录。

全局 Fast/Flex 策略保留最终过滤、拦截和改写权限。合法 OAuth Guardian 请求仍最终省略 `service_tier`。图片、模型目录、配额等独立辅助接口不新增档位。HTTP 重试和 WS 后续轮基于各自原始请求；未变化的档位直接复用正文。

## 免费含义与诊断

“免费”表示免加速溢价：用户按同一请求的 Standard 价格计费。实际发送档位、上游原始成本、账号成本和账号额度统计保持原规则；渠道、长上下文、峰时和用户倍率沿用现有计价。免费 Fast 仅覆盖 priority/Fast，免费 Ultrafast 仅覆盖 Ultrafast。

异步计费只冻结两个小型布尔标志和现有计费数据，不持有完整账号 extra 或正文。Engine 独立结算快照保存相同偏好，旧快照缺失字段按关闭处理，保持原有查价时点。

指纹详情新增 Fast / Ultrafast 策略：客户端档位、强制来源、请求强制档位、实际出站档位及能力跳过原因。最终档位来自实际物理请求或当前 WS 帧；旧记录显示未记录。

## API 与升级

- 分组列/API：`force_openai_ultrafast`、`free_openai_ultrafast`，均为布尔值。
- 账号 extra：`force_openai_fast`、`force_openai_ultrafast`、`free_openai_fast`、`free_openai_ultrafast`，只接受布尔值。省略项保持原值。
- 创建、编辑、复制、导入导出及批量设置沿用既有管理流程；批量界面仅提交勾选的项目，强制两项作为一对更新。
- 应用迁移 `282_group_openai_ultrafast.sql`，仅新增默认 false 的分组列；鉴权缓存版本升为 29。无需修改已有账号数据。
- 回退应用可保留新增列与 extra 值；旧程序不执行新增控制。回退前应关闭依赖新策略的业务设置。

本次仅代码交付，未部署、未调用真实模型、未修改 Engine 项目或线上配置。验证记录见 [验证报告](openai-fast-ultrafast-verification.md)。

# 上游 v0.2.9 合并与升级说明

## 基线与边界

- 本地父提交：`50d89cffb097ed542372ec115e99d360152654a6`。
- 上游父提交：`9a62841fd124d026cf3694fcf9b79e98addcdbdc`，包含 v0.2.9 及版本号同步。
- 共同基线：`a3eb7ef302961cba716dc78b39b93b60c467db0e`。上游新增 70 个提交、117 个变更文件。
- 在 `codex/merge-upstream-v0.2.9` 真实合并，保留双方历史；三个文本冲突逐项处理，并补充无文本冲突的行为适配。

## 本地定制与上游修复

糖果测试仍从各账号上游实时读取模型目录，直接发送管理员选择的真实模型 ID；不使用业务白名单、账号映射或旧 Codex 别名替换。测试保留一次推理发送、取消、20 分钟截止时间及账号状态隔离，401/403/429、连接错误只影响测试结果。取消错误处理先识别糖果用途，再处理普通业务客户端取消，避免把测试取消写成账号或代理故障。

OpenAI OAuth 仍以 `accounts.credentials` 为唯一凭据源，沿用账号级调度和刷新 CAS。三系统仅分配 UA、installation ID、TLS、会话根及遥测身份，不恢复分系统授权门控。保留 native HTTP 生命周期、实际出口遥测及最终模型/Lite 冻结；合入 Responses 多代理 beta 透传，只移除 legacy Responses beta。

自建 turn-state 缓存、目标票据解析、采集、Cookie 包及出票代理功能保持退役。原生 opaque 状态透传、来源保护和协议 continuation 仍然保留；历史合并说明中的票据功能不作为当前实现依据。

额度自动重置吸收上游无卡确认期避免重复查询、一分钟查询失败退避和三十秒调度通知合并，同时保留本地查询后重读、授权/尝试来源校验、周期锁与兑换幂等。查询失败也通过原状态 CAS 保存，防止迟到失败覆盖新授权或并发成功。存在明确未来重置时间时，陈旧使用量快照仍维持阈值暂停，实际重置后才失效。

WebSocket 使用已经过本地投影的有效上下文窗口，在续接推断前解除旧窗口的 previous_response_id；同窗口或缺失窗口信息保持原行为。成功后同时更新窗口记录和时区历史，保留 compact 实际交付后的 CAS 及三系统冻结身份。

协议转换吸收角色 message 类型、显式 thinking 禁用、GPT 后续代际识别、工具参数种子恢复及终态空文本恢复。Responses 文件转 Anthropic document 同步补齐来源占位，防止后续文本的完整性/时区路径错位。文本恢复不提升响应状态，失败或缺失完成终态不会变成成功。Anthropic structured-output beta、Antigravity PDF/schema/空流，以及限定官方 DeepSeek/OpenCode 主机的 reasoning 占位修复一并合入。

计费保留本地礼金余额、实际响应模型和服务层级规则，合入 Free Fast 缺价零成本日志与账号成本长上下文开关。渠道图片价留空继承目录价，显式输入/输出 `0` 均保持免费；基础与区间价一致。模型广场新增 `video_rate_independent`、`video_rate_multiplier`，图片、视频和分组倍率独立。

分组白名单支持任意位置的 `*`，普通模型发现允许透传账号补充目录；这不影响糖果题实时目录。合入空闲用量倒计时、分组弹窗资源清理、CC Switch 根端点/单一 v1 路径和 Windows Codex 目录配置路径，保留本地页面配置。

## 升级与数据兼容

本次上游没有新增数据库迁移或依赖版本更新，不修改既有迁移文件或校验和。本地缓存退役和糖果任务表迁移仍按既有规则执行；已完成升级的数据库无需重复回填，不清空 Redis 或修改账号状态。

Compose 的 Redis 启动改用参数列表，保留本地环境变量、持久化和健康检查配置。安装向导只停止为新配置生成已废弃的限流默认值，不改写现有部署配置。本次交付不部署，不执行生产数据库操作，也不发布标签。

## 验证记录

验证统一使用本地模拟、合成凭据和隔离 PostgreSQL/Redis，没有发送真实业务、目录、糖果测试、OAuth、遥测或收费请求。既有失败需在本次固定基线复现，不能直接继承 v0.2.8 的失败豁免。

- [后端基线与合并对照](UPSTREAM_MERGE_V0_2_9_VERIFICATION.md)
- [网关专项](UPSTREAM_MERGE_V0_2_9_GATEWAY_VERIFICATION.md)
- [前端与模拟浏览器](UPSTREAM_MERGE_V0_2_9_FRONTEND_VERIFICATION.md)
- [隔离存储](UPSTREAM_MERGE_V0_2_9_STORAGE_VERIFICATION.md)
- [计费与部署配置](UPSTREAM_MERGE_V0_2_9_BILLING_DEPLOY_VERIFICATION.md)

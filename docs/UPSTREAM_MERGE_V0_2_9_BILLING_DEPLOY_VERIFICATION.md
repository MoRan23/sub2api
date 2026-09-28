# v0.2.9 合并：计费、模型广场与 Compose 验证

本记录对应本地 `50d89cffb` 与固定上游 `9a62841fd124d026cf3694fcf9b79e98addcdbdc` 的合并。验证使用模拟数据、合成密码和独立容器，没有调用真实模型、授权或遥测接口。

## 计费与 API

- 渠道图片输入、输出价格为 `null` 时继承目录价格；显式 `0` 时免费。基础价与命中阶梯区间的路径一致。补齐上游仍缺失的图片输入显式零价标记，保留没有目录图片价时的文本价回退；价格克隆不会污染共享目录。
- 账号统计成本的目录长上下文费率由账号开关决定，分组开关只影响原有客户售价规则。自定义账号成本规则和“将售价应用到成本”仍按原优先级执行。
- 保留本地 `TotalBalance()` 礼金口径、响应模型计费的价格和来源守卫，以及 Free Fast 未知定价时的零成本用量记录。
- 模型广场从分组到服务层及公开 DTO 传递视频独立倍率；图片、视频显式零倍率不会被分组或用户倍率覆盖。
- 安装向导不再生成废弃 `rate_limit` 两项默认值，Redis、时区和其他默认配置保持原样。不改写已有部署配置。

Windows Go 专项命令（工作目录 `backend`；第二次增加 `-race`）：

```bash
GOMAXPROCS=2 GOEXPERIMENT=jsonv2 TYPESAFE_LIVE_TEST=0 go test -p 1 -tags unit \
  ./internal/service ./internal/handler ./internal/setup \
  -run 'Test(GetModelPricingWithChannel|ComputeTokenBreakdown|ApplyTokenOverrides|CalculateCostUnified_.*(Image|Channel)|.*AccountStats|TryModelFilePricing|ListPlazaGroups|ListGroups_.*Image|ToModelPlazaGroupDTO|WriteConfigFile|.*ResponseModel|.*FreeOpenAIFast|.*Gift)' \
  -count=1
```

| 专项 | service | handler | setup |
| --- | --- | --- | --- |
| unit | 通过 | 通过 | 通过 |
| unit + race | 通过 | 通过 | 通过 |

本专项无测试失败；全量测试、基线比较与数据库集成结果另见本次总体验证记录。

## Redis 实际运行验证

在 WSL Ubuntu 24.04 使用 Docker Compose v5.1.2 和各文件声明的 `redis:8-alpine`。对 `docker-compose.yml`、`docker-compose.local.yml`、`docker-compose.dev.yml` 分别验证空密码和包含空格、双引号、单引号、美元符号、分号、`&`、反斜杠的合成密码，共六组。

每组从原 Compose 文件渲染配置，再以专用覆盖文件重命名容器、清除数据卷和服务网络，设置 `network_mode: none`；仅启动 Redis，禁止启动依赖。没有连接或重启任何已有容器。每组完成后删除对应临时容器及其匿名卷。

六组均通过以下断言：

- 实际容器 argv 包含完整 `--save 60 1 --appendonly yes --appendfsync everysec --requirepass <值>`，密码保持一个参数且特殊字符不被 shell 执行或拆分。
- Redis `CONFIG GET` 确认 `save=60 1`、`appendonly=yes`、`appendfsync=everysec` 和精确密码值。空密码可正常启动。
- 渲染后的应用配置保留 `CODEX_TELEMETRY_ENABLED`、`CODEX_STATSIG_API_KEY`，并使用合成值验证传递。
- Compose JSON 展示时会为可再次读取的配置转义美元符号；最终容器参数通过原 Compose 启动后再次检查，确保实际密码没有重复美元符号。

本地验证脚本与日志位于 `.git/task-artifacts/merge-upstream-v0-2-9/`：`verify-redis-compose.py`、`compose-redis-verification.log`、`billing-deploy-unit.log`、`billing-deploy-race.log`。这些是本机验证产物，不属于发布包。

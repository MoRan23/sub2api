# OpenAI OAuth 原生 HTTP 指纹

## 行为与覆盖范围

内置 OAuth HTTP 发送使用三平台实采模板，默认生效，不依赖指纹采集、每日固定根或遥测开关。模板按最终实际 User-Agent 中的 Windows、macOS、Linux 平台选择。无平台时依次使用被冻结的来源请求 UA、凭据所属账号 UA、规范 UA，最后使用内置 Linux 平台；不使用运行服务器的 GOOS。

这只选择传输模板，不修改用户正文、session/thread/turn 或实际 UA。OTLP 保留 OTel UA。Agent Identity 注册显式写入此前标准 HTTP 传输自动发送的 `Go-http-client/1.1`，避免更换传输后丢失原有 UA。模板读取与线路序列化共用大小写兼容的 UA 读取规则。

覆盖内置 Responses 及兼容入口、图片、compact/memory/guardian、搜索、计数、Live HTTP、History/Notes、模型与后台刷新、账号测试与用量、额度、隐私与订阅资料、认证换码/刷新、PAT、Agent Identity、analytics/metrics。每个物理 HTTP 发送和重定向跳重新选择模板，保留原有重定向及凭据隔离规则。

WS 保持标准传输。外部插件仍有原来的路由优先级，插件接管后不进入内置传输；本功能不保证外部插件的线路指纹。API Key 与其他供应商不因目标域名或通用 OpenAI 请求策略而启用本功能。

## 模板与传输实现

模板来源为 2026-09-17、Codex CLI 0.154.0 的三平台各六份样本：Windows 11 26100、Ubuntu 24.04.3 WSL、macOS 26.4.1 arm64。保留公开 ClientHello 结构；不提交采集包、采集私钥或原始请求值。来源和能力边界详见 `backend/internal/pkg/codexnative/README.md`。

三平台均无 ALPN，使用 HTTP/1.1。Windows/macOS 使用 TLS 1.2，Linux 提供 TLS 1.3/1.2。每次新握手生成随机数和临时密钥，SNI 按目标生成。Windows 首条明文 TLS record 版本适配发生在代理隧道内，代理外层证书校验正常保留。

HTTP 仅改变头名大小写与线路顺序；不存在的样本字段不补造，额外实际字段排序放在 host/正文分帧字段之前。正文长度、流式分帧、动态 trailer、SSE 与请求取消保留。采样服务主动关闭连接不是客户端策略，因此本实现继续复用连接。

使用版本固定的本地 req/v3 副本，仅增加默认关闭的小写头名和建连阶段取消选项；native 传输启用这些选项，其他 req 用户保留默认行为。三个源码 Dockerfile 已复制本地模块依赖清单。fork 更新方法与差异见 `backend/third_party/req/SUB2API_FORK.md`。

主网关沿用有界缓存与 in-flight 跟踪，连接按账号、代理、用途、模板内容和池配置隔离；独立 HTTP/req 出口的 dispatcher 子池容量为 256，空闲 TTL 为 15 分钟，满载时不淘汰活跃流。session/thread/turn 不进入池键。HTTP/HTTPS CONNECT 和 SOCKS5/H 均在隧道建立后执行目标模板握手。

## 验证记录（2026-09-17）

- 真实本地线路测试通过：三平台 ClientHello 的记录版本、完整扩展 payload/顺序、动态 SNI、新随机/key share、无 ALPN；TLS 1.2、TLS 1.3 与 MLKEM 实际握手。
- HTTP 测试通过：全小写/顺序/额外头/多值、Content-Length、chunked、动态 trailer、100-continue、UA 不变和大小写兼容。
- 代理测试通过：HTTP/HTTPS CONNECT、SOCKS5/H、代理认证与远端 DNS、内外层证书错误、握手/响应头超时、取消、已完成请求取消后连接继续复用、SSE/并发与空闲回收。
- 接入层默认与 `unit` 定向测试、`codexnative`/共享客户端/仓储/服务定向 `-race` 通过；时序敏感代理和 SSE 场景重复十次通过。测试不请求真实 OpenAI/遥测端点。
- Wire 生成通过，无生成代码差异；Windows 后端构建、Linux amd64 交叉构建通过。
- 后端 `go test ./...` 仍失败：repository 3 项、service 21 项、middleware 1 项（测试桩 nil panic），共 25 个顶层失败。独立检出修改前 `04b239827` 复测三个失败包，25 项全部重现，无新增顶层失败。
- Docker 不可用，因此未运行容器构建和 Docker 数据库集成测试。没有数据库迁移或前端改动；未重复前端套件。不等待 CI，不部署。

## 已知边界

样本只证明首次握手和首次 Responses 请求，辅助接口采用同一平台格式规则，没有声称采集了每种接口的原生头顺序。按系统大类匹配是兼容选择，不代表不同系统小版本、Linux 分发版本或客户端版本指纹必然相同。

uTLS 可发出对应 ClientHello，并完成已测试的常见握手，但并未实现原生系统宣告的全部算法。服务端若选择未实现的算法，返回正常传输错误，不暗中更换模板或平台。会话恢复不使用捕获票据，外部插件与 WebSocket 不在本次指纹覆盖范围。

# Shadowsocks / SS2022 代理

Sub2API 可直接连接 Shadowsocks 节点，不需要额外运行本地 SOCKS 转换进程。
在管理端「代理」创建或编辑代理，选择 **Shadowsocks (SS / SS2022)**，填写
节点地址、端口、加密方式和密码／密钥，再执行连接测试并将代理分配给账号。

## 配置

支持以下加密方式：

| 类型 | 加密方式 | 密码要求 |
| --- | --- | --- |
| AEAD | `aes-128-gcm`、`aes-192-gcm`、`aes-256-gcm`、`chacha20-ietf-poly1305` | 非空密码，保留首尾空格 |
| SS2022 | `2022-blake3-aes-128-gcm` | Base64 编码的 16 字节密钥 |
| SS2022 | `2022-blake3-aes-256-gcm`、`2022-blake3-chacha20-poly1305` | Base64 编码的 32 字节密钥 |

可批量粘贴 SIP002 `ss://` 链接。明文 userinfo 中的密码必须按 URL 规则编码，
例如 `+`、`/`、`=` 等字符使用百分号编码。旧 AEAD 也支持 Base64URL 编码的
`method:password` userinfo；SS2022 使用百分号编码的明文 userinfo。
支持 IPv6 节点地址（方括号格式）和 `#名称`。不支持插件参数、SSR、无加密方法
或 SS2022 多级用户密钥链；不支持的配置会返回错误。

管理 API 沿用代理记录结构，无需迁移：

```json
{
  "name": "My SS node",
  "protocol": "ss",
  "host": "proxy.example.com",
  "port": 27869,
  "username": "2022-blake3-aes-128-gcm",
  "password": "<节点提供的 Base64 密钥>"
}
```

`username` 在 SS 下表示加密方式，`password` 表示密码／密钥。导入导出保留
这两个字段。修改方法、密码或节点地址沿用现有代理路由代际与缓存失效机制。
两字段各沿用数据库的 100 字节上限。备份代理、过期处理继续使用现有设置。

## 连接行为

- 支持 Sub2API 的 TCP 请求：HTTP、HTTPS、SSE、WebSocket，以及账号 OAuth、
  目录、归因与连通性探测使用的公共传输路径。
- 目标域名封装到 SS 请求，由节点解析。若节点地址本身是域名，Sub2API 仍需解析
  节点地址才能建立连接。原有 URL 安全校验保持生效：启用解析 IP 校验的请求
  会在转发前另外执行本地解析检查。
- 保留原有 TLS 证书校验、TLS 指纹和 OpenAI 原生客户端行为；SS 只承载连接。
- 建连与初始握手受超时和取消控制，连接池继续复用连接。错误密钥、无效配置、
  不支持的协议均返回错误，不在连接失败时自动改为直连。
- 本功能不是系统 TUN，不接管浏览器 WebRTC、系统 DNS 或任意 UDP 应用。

## 依赖与来源

使用固定版本 `github.com/sagernet/sing-shadowsocks v0.2.9` 实现加密协议，
没有自行实现 Shadowsocks 密码算法。第三方代码遵循其自身 GPL-3.0-or-later
许可证，原文与版本信息见 `backend/third_party/sing-shadowsocks/`。
发行包携带该目录，容器内放在 `/app/licenses/sing-shadowsocks/`。
发布包含该依赖的组合二进制时，应连同 GPL 许可证及对应源码提供；仓库原有
源码许可证文件保持原样。

- [SIP002 链接格式](https://shadowsocks.org/doc/sip002.html)
- [SS2022 协议](https://shadowsocks.org/doc/sip022.html)
- [sing-shadowsocks](https://github.com/SagerNet/sing-shadowsocks)

## 本地验证

测试使用回环模拟节点，不连接实际代理或模型。已验证七种方法的加密 TCP 往返、
域名／IPv4／IPv6 目标传递、错误密钥、取消与超时、并发复用、TLS 证书校验、
HTTP 原生客户端、OAuth 和 WebSocket；配置测试覆盖创建、部分更新、密码空白
保留、导入导出和旧协议。核心包在 Linux 下通过竞态检查。

后端编译、相关 Go 测试、前端 86 项相关测试、类型检查、生产构建、定向 lint
以及发行配置的 10 项回归测试通过。

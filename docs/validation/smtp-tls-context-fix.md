# SMTP implicit TLS fix / SMTP 隐式 TLS 修复

## Cause / 原因

The context cancellation change in `042e8a292` wrapped `net.Conn` before passing it to `net/smtp.NewClient`. The standard library recognizes implicit TLS using the concrete `*tls.Conn` type. Wrapping it hid that type, so `PlainAuth` refused authentication to a non-localhost SMTP host with `unencrypted connection`, even though the underlying transport had completed TLS successfully.

原有测试使用 `localhost` / `127.0.0.1`，触发标准库对本地主机的认证例外，遗漏了这项回归。线上 SMTP 465 的配置和证书校验正常；发送在 AUTH 命令发出前失败。

## Change / 修改

- Pass the original connection to `smtp.NewClient`.
- Own the cancellation callback in a small SMTP session wrapper instead of wrapping the transport.
- Keep cancellation active while greeting, authenticating, sending DATA and waiting for QUIT; retain the existing dial and I/O deadlines.
- Unregister the callback on Close, QUIT completion/failure and client-construction failure. Failed QUIT closes the connection as well.
- Preserve implicit TLS, mandatory/opportunistic STARTTLS, certificate verification, message formatting and existing mail queue behavior.

未新增常驻协程、配置、数据库迁移或邮件内容副本。该修复需要部署新版应用生效；本次只提交推送。历史上已耗尽重试次数的通知不会被自动重新入队。

## Verification / 验证

Go 1.27.4, `GOEXPERIMENT=greenteagc,jsonv2`, `GOMAXPROCS=4`. All SMTP traffic in tests uses local loopback servers and dummy credentials; no real email is sent.

New regressions cover certificate-verified `smtp.test` TLS authentication, full connection/send paths through `127.0.0.2` (outside net/smtp's plaintext-auth exceptions), both legacy `UseTLS` and explicit `TLSMode=implicit`, QUIT cancellation and callback/connection cleanup. Existing STARTTLS, plaintext refusal, greeting/auth cancellation, deadline and MIME tests remain in the selected suite.

Run from `backend`:

```bash
export GOEXPERIMENT=greenteagc,jsonv2 GOMAXPROCS=4
go test -tags unit -p 2 ./internal/service -run '^Test(SMTP|SendEmailWithConfig|BuildSMTPMessage)' -count=1 -timeout=3m
go test -race -tags unit -p 2 ./internal/service -run '^Test(SMTP|SendEmailWithConfig|BuildSMTPMessage)' -count=1 -timeout=5m
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -tags embed -p 2 -o ../.tmp-smtp-tls-fix-linux ./cmd/server
```

2026-11-18 results: unit passed (5.604 s), race passed (6.779 s), Linux embedded build passed. See [test output](smtp-tls-context-tests.log). Independent code review confirmed cleanup on every sending/test-connection exit path and preservation of STARTTLS behavior.

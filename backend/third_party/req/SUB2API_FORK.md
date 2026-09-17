# Local req/v3 transport patch

This directory contains the source of `github.com/imroc/req/v3` **v3.59.0**
from the verified Go module cache. Of the 174 upstream files, the 23 files under
`.codebuddy/` are unrelated agent configuration and are excluded from version
control; all 151 other upstream files are retained. The upstream MIT license is preserved in
`LICENSE`. The module path and dependency versions remain those of the upstream
release. The backend replaces only this pinned version with this directory.

The local patch adds `Transport.LowercaseHTTP1HeaderNames` and its chainable
`SetLowercaseHTTP1HeaderNames(bool)` setter. It is disabled by default. When
enabled, HTTP/1.1 serialization writes lowercase names for ordinary headers,
generated Host/User-Agent/framing headers, and trailers. Trailer declarations
use the same wire spelling. The option preserves request header maps, values,
ordering, framing and HTTP/2/HTTP/3 behavior, and is retained by `Clone`.
The HTTP CONNECT request establishing a proxy tunnel keeps the upstream
standard-library serializer; this option affects requests sent inside that
tunnel, not the proxy's own wire identity.

Ordered header writes also propagate their writer errors instead of dropping
them. Callers must configure the option before sharing a transport, as with
other transport configuration fields.

`Transport.CancelDialOnRequestCancel` and its chainable setter are also
default-off. The OAuth transport enables this option so a canceled request
cancels pending dial, proxy negotiation, and TLS work, instead of completing
a detached connection for a future request. Completed connections retain
normal reuse behavior. The option is retained by `Clone`.

Local-only focused tests (no public network requests):

```sh
go test . -run '^(TestLowercaseHTTP1|TestCancelDialOnRequestCancel)' -count=1
```

When updating req, rebase these small changes in `transport.go` and `transfer.go`,
retain `transport_http1_casing_test.go`, and rerun the backend's native OAuth wire
tests before changing the version-specific replacement in `backend/go.mod`.

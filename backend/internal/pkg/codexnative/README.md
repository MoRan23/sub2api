# Native Codex OAuth HTTP profiles

These three built-in profiles come from six fresh-connection observations per
platform, collected on 2026-09-17 using Codex CLI 0.154.0: Windows 11 26100,
Ubuntu 24.04.3 under WSL, and macOS 26.4.1 arm64. Capture schema 2 records the
ClientHello and the first HTTP/1.1 Responses request at a controlled endpoint.
The capture tool does not independently verify the process executable; the
source sessions provide the CLI provenance. All three platforms offered no
ALPN. These samples do not establish connection-resumption or auxiliary-endpoint
specific behavior.

Only public protocol structure is retained in `profile.go`. The original
captures, request values, keys, randomness, and local CA files are not embedded.
Every handshake builds fresh extensions and ephemeral key material. The profile
digest includes ordered parameters, first-record version, session-ID policy,
header order, and version identifier.

Windows and macOS offer TLS 1.2. Linux offers TLS 1.3 and 1.2, including fresh
X25519MLKEM768 and X25519 shares. uTLS supports the commonly negotiated AEAD
suites; it does not implement all algorithms advertised by the native OS sample
(for example every CBC/finite-field combination and Encrypt-then-MAC). This is
not a replacement for the entire platform TLS implementation. No alternate
platform fallback is attempted on a handshake error.

Explicit OAuth scopes opt requests into dispatch. Final outbound UA platform
takes precedence over frozen source, credential-owner, and canonical hints;
unrecognized hints use Linux. UA values and semantic request fields are not
rewritten. WebSocket and externally handled plugin traffic never enter this
HTTP-only transport. HTTP ordering rules apply to fields that actually exist;
unknown fields are sorted before host and body framing headers.

The req transport's proxy implementation establishes the outer HTTPS proxy
connection using ordinary verified TLS. Only the target connection inside the
CONNECT or SOCKS tunnel uses the native profile. Windows' record-version adapter
therefore changes only the first target ClientHello record.

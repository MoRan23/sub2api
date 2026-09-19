# Legacy plugin binary fixture

`pkg/pluginapi/v1/{plugin.proto,plugin.pb.go,plugin_grpc.pb.go,runtime.go}` are
unmodified files from commit `4897e8b5395f093d5a868cdf9ab2fef2fbb8c738`, under
`backend/pkg/pluginapi/v1/`. Do not regenerate them with the current schema.
They contain neither `InitHostServices` nor the new Health `status_json` field.

The integration test copies this independent module to a temporary directory
and builds an executable with `GOWORK=off`, without replacing its SDK with the
host module. Its dependency versions match the baseline SDK dependencies.
`main.go` is a synthetic transport fixture, not a production plugin. It only
permits literal loopback HTTP targets and never follows redirects or proxies.

Run from `backend`:

```
go test ./internal/service -run '^TestPluginLegacyBinaryMergeIntegration$' -count=1 -v
```

The test requires the Go toolchain and fails on build/start/compatibility
errors. It has no environment variable gate or skipped fallback.

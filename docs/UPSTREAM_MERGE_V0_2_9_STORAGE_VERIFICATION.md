# v0.2.9 storage verification

The merge storage checks use the repository integration harness with dedicated
PostgreSQL 18 and Redis 8.4 containers started by testcontainers. They do not
connect to any existing database or upstream service.

The focused run passed on 2026-09-28:

```text
GOMAXPROCS=2 GOEXPERIMENT=jsonv2 TYPESAFE_LIVE_TEST=0 \
go test -p 1 -tags integration ./internal/repository \
  -run '^(TestQuotaPreflightCAS|TestMergeV029|TestSchedulerSnapshotOutboxReplay|TestIdempotency)' \
  -count=1 -timeout=15m
```

It covered:

- atomic quota preflight failure persistence and scheduler publication;
- late failures rejected after authorization replacement, state changes,
  account disablement, pause, missing generation, or owner mismatch;
- ordinary OAuth token refresh accepted without changing authorization
  generation, and concurrent instances producing one CAS winner;
- a lock-wait race in which a committed authorization replacement is observed
  before a stale failure can be written;
- Redis scheduler snapshot propagation and multi-coordinator idempotency for
  a synthetic reset cycle;
- channel image pricing preserving `nil` inheritance, explicit `0`, and
  non-zero prices across a database round trip;
- the account long-context billing flag surviving account and scheduler cache
  updates.

The repository preflight SQL locks the account row before checking the frozen
authorization generation. It intentionally ignores normal credential revision
changes, while a generation or credential-owner replacement invalidates the
late write. No migration or existing migration checksum is changed by these
tests.

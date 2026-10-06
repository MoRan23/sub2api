# Sub2API LM Fingerpoint Detector

Private analysis adapter for [Ikaleio/lm-detector](https://github.com/Ikaleio/lm-detector).
Pinned revision: `d53d3f5b158249a87a895e4fc7aaa8a940dfd6d6`.
The vendored shared modules are unchanged; `vendor/LICENSE` retains the upstream MIT notice.
Data files are gzip-compressed copies of the exact upstream bytes. `upstream.json` records
the source revision and SHA-256 integrity values for every vendored file.

## Run and test

```sh
bun test
bun run server.ts
docker build -t sub2api-lm-detector:d53d3f5b1582 .
```

Bun 1.4.2 is the pinned container runtime. No package install, remote model calls,
runtime downloads, tokenizer probing or training is needed. The service loads the
bank, ranker and calibration once at startup. Code/data integrity, reference
sample counts and the official ranker/calibration bindings must all match before
the HTTP listener starts. There is no legacy-ranking fallback.

The service intentionally has no public authentication interface: attach it only
to the private Sub2API container network and do not publish a host port.
Requests containing Authorization or X-API-Key are rejected. Input bodies accept
only three `{text, expected_count}` outputs; each answer is limited to the existing
Sub2API 1 MiB probe output limit. No truncation, trimming or digit rewriting occurs.
Input answers are never logged or persisted. The bundled public reference samples
are static upstream training data, not user test results.

## Protocol v1

- `GET /healthz`: ready only after successful startup validation.
- `GET /api/info`: provider, protocol, upstream revision, algorithm, bank date,
  reference, ranker and calibration identities.
- `GET /api/banks`: `{"shared":{"models":[{"id":"…"}]}}`.
- `GET /api/challenges`: `{"challenges":[…]}`, three official random challenges.
- `POST /api/analyze`: `{"outputs":[{"text":"…","expected_count":300},…]}`;
  returns the official full-sample analysis plus `detector` metadata.

All responses use `Cache-Control: no-store` and `X-LM-Detector-Version`.
Except for info and health, clients must send that version header, formed by
joining revision, ranker SHA, reference SHA and calibration SHA with colons.
Version mismatch returns 409 before any analysis.

Confidence is the official calibrated closed-set probability, not an identity
certificate. `decision: not_confirmed` is normal for this algorithm. All three
answers and matching calibration are required for a scored result; null
probabilities remain null. Sub2API decides allowlist changes.

## Updating

Update vendored modules and data together from one reviewed upstream commit;
regenerate the integrity manifest, retain the license, run the adapter and
Sub2API regression tests, and release a new fixed container tag. Never point
runtime code at `main` or `latest`. Updating the container requires an administrator
to check the connection and save the new detector version in Sub2API; the
consecutive-pass baseline then starts again. Old history remains readable.

### Standalone server updater

For the standalone deployment at `/opt/lm-detector`, install `update.sh` and
`update.py` together in that directory (`chmod 755 update.sh`). Python 3 and
Docker Compose are required; no host Bun installation or Git checkout is needed.
The existing Compose file must contain **only** `lm-detector`, use an external
Sub2API network, and publish no host ports.

```sh
# Download the latest official main revision and check it without switching.
/opt/lm-detector/update.sh --check

# Upgrade to an explicitly reviewed upstream commit (recommended).
/opt/lm-detector/update.sh --ref <full-upstream-commit>

# Or resolve main once and deploy that fixed revision.
/opt/lm-detector/update.sh

# Restore the previous successfully deployed detector.
/opt/lm-detector/update.sh --rollback
```

The updater downloads only the official shared modules, three data files and
license from the same resolved commit. It regenerates the required integrity
manifest, retains the installed adapter, builds a fixed image, runs its offline
tests, and checks a temporary container from the actual `sub2api` container.
Algorithm/schema changes that break the adapter or tests abort the update; they
require an adapter update rather than skipping checks. GitHub API/download
failures also leave the current deployment intact.

Only after validation does it replace the detector with `up --no-deps`. Failed
switches restore the previous Compose file and image. An exclusive lock prevents
concurrent updates. Source releases, images, and Compose backups remain available;
there is no global Docker prune. `--check` retains its candidate source/image but
removes its temporary container. At least 2 GiB free disk space is required.

Pause automatic attribution before upgrading, then check the connection and save
the new accepted detector version in the Sub2API admin page before re-enabling it.
The updater does not change admin settings, restart Sub2API/PostgreSQL/Redis,
modify database records, or send model requests. The address stays
`http://lm-detector:8080`. Use `--directory` or `--sub2api-container` for other
standalone deployment layouts. Updater regression tests: `python3 update_test.py`.

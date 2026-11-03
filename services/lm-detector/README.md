# Sub2API LM Fingerprint Detector

Private analysis adapter for [Ikaleio/lm-detector](https://github.com/Ikaleio/lm-detector).
Pinned revision: `e9bd7c545bf75f35aa02aaf0878b36b89fe7dfa4`.
The vendored shared modules are unchanged; `vendor/LICENSE` retains the upstream MIT notice.
Data files are gzip-compressed copies of the exact upstream bytes. `upstream.json` records
the source revision and SHA-256 integrity values for every vendored file.

## Run and test

```sh
bun test
python3 update_test.py
bun run server.ts
docker build -t sub2api-lm-detector:e9bd7c545bf7 .
```

Bun 1.4.2 is the pinned container runtime. No package install, remote model calls,
runtime downloads, tokenizer probing or training is needed. The service loads the
bank, ranker and calibration once at startup. Code/data integrity, reference
sample counts and the official ranker/calibration bindings must all match before
the HTTP listener starts. There is no legacy-ranking fallback.
The current official `shared-detector-v2` ranker is stored as
`data/shared_detector.bin` and decoded by the unchanged official
`shared/detector-codec.ts`; it is not converted through JSON or reimplemented.

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
certificate. Successful v2 results omit `decision`; older v1 results may use
`decision: not_confirmed`. Both describe a reference-library ranking. The official
`not_model` result has no ranking or confidence and remains unscorable. All three
answers and matching calibration are required for a scored result; null
probabilities remain null. Sub2API decides allowlist changes.

## Updating

Update vendored modules and data together from one reviewed upstream commit;
regenerate the integrity manifest, retain the license, run the adapter and
Sub2API regression tests, and release a new fixed container tag. Never point
runtime code at `main` or `latest`. Current Sub2API reads and freezes the live
detector version for each new task; updating the container does not require saving
a newly accepted version. A version change during an existing task still stops
mixed-version analysis, and the consecutive-pass baseline starts again. Old
history remains readable.

This bundled reference library has 66 candidates. `gpt-6-astra` and `gpt-5.6-sol`
remain available. OpenAI `gpt-6.1-sol` samples are grouped into the canonical
`gpt-6-astra` candidate (`display_name: gpt-6-astra/6.1-sol`). Its 72 reference
samples include 36 from `openrouter/openai` and 36 from `openai/direct`.
`gpt-6.1-terra` is a separate candidate. Expected model matching continues to use exact canonical
candidate IDs returned by `/api/banks`.

### Standalone server updater

For the standalone deployment at `/opt/lm-detector`, install `update.sh` and
`update.py` together in that directory (`chmod 755 update.sh`). Python 3 and
Docker Compose are required; no host Bun installation or Git checkout is needed.
The existing Compose file must contain **only** `lm-detector`, use an external
Sub2API network, and publish no host ports.

The initial v1-to-v2 migration also needs the updated adapter and its tests in
the release source: the v2 artifact is binary, while the old adapter reads JSON.
The updater copies the adapter from the existing Compose build context, so
replacing only `update.sh` and `update.py` does not migrate that adapter. Prepare
and validate a complete v2 release, deploy a v2-compatible Sub2API build, then
switch the detector. Keep the previous source and image for rollback. Subsequent
updates can use the commands below with the installed v2 adapter.

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

Use a Sub2API build that supports the official `shared-detector-v2` algorithm
before deploying this version. Older Sub2API builds that still require an accepted
detector version need a connection check and saved identity after an upgrade;
the current build discovers it automatically for new tasks. Check expected model
lists when the upstream reference library changes. Waiting tasks retain their
original version and cannot mix old answers with a newer detector.
The updater does not change admin settings, restart Sub2API/PostgreSQL/Redis,
modify database records, or send model requests. The address stays
`http://lm-detector:8080`. Use `--directory` or `--sub2api-container` for other
standalone deployment layouts. Updater regression tests: `python3 update_test.py`.

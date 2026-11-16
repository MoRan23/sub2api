#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../../../backend"
export GOEXPERIMENT=greenteagc,jsonv2
export GOMAXPROCS=4
binary=${SUB2_PERF_BINARY:-/tmp/sub2api-perf-service-e6de.test}
if [[ ${SUB2_PERF_SKIP_BUILD:-false} != true ]]; then
  go test -p 1 -tags unit -c -o "$binary" ./internal/service
fi
for round in 1 2 3; do
  echo "round=$round"
  for scenario in text_1m tool_1m ws_16m; do
    for legacy in true false; do
      SUB2_PERF_SCENARIO="$scenario" SUB2_PERF_LEGACY="$legacy" "$binary" -test.run '^TestPerformanceProcessSample$' -test.v
    done
  done
  SUB2_PERF_SCENARIO=xlsx_1000 "$binary" -test.run '^TestPerformanceProcessSample$' -test.v
  SUB2_PERF_SCENARIO=xlsx_200000 "$binary" -test.run '^TestPerformanceProcessSample$' -test.v
done

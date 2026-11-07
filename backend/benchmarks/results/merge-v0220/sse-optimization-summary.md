# Engine WS multiline SSE performance follow-up

Linux/amd64 WSL Ubuntu-24.04, Go 1.27.3, GOEXPERIMENT=greenteagc,jsonv2, GOMAXPROCS=4, taskset -c 0-3. Each comparison alternates AB/BA for six independent 3s runs. All original samples and profiles remain in the artifact directory.

| Comparison | Reference ns/op | Version ns/op | Change | Reference p95 ns | Version p95 ns | Change | B/op | allocs/op |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| unoptimized | 190031 | 199221 | +4.84% | 279781 | 291668 | +4.25% | 17232 | 314 |
| optimized | 190560 | 218153 | +14.48% | 249544 | 346660 | +38.92% | 17232 | 314 |
| same-window candidate vs optimized (3 pairs) | 193822 | 187770 | -3.12% | 308275 | 265874 | -13.75% | 17232 | 314 |

The first two rows are medians across all six runs against baseline. The final row directly compares the original candidate and optimized binary in three alternating AB/BA pairs in one window; the optimized version was faster in every pair. All binary build settings match (sse-binary-build-settings.json). The p95 metric records the first up to 1024 relay calls per invocation, so the sustained ns/op metric and CPU profiles are the primary performance evidence.

The six-run optimized comparison was slower (+14.48%); the subsequent direct control was faster (-3.12%). The same optimized binary ranged from 181892 to 258419 ns/op across these windows. This variability is retained in full rather than discarded. The direct control and the reduced CPU hotspot support keeping the small change; an exact final percentage against baseline is not established by these inconsistent windows.

The original CPU profiles localized the added cost to repeated bytes.TrimRight calls: cumulative readFrame CPU 1.07s -> 1.66s and cumulative TrimRight CPU 0.25s -> 0.83s in similarly sized profiles. The optimized code first checks whether a fragment can exceed the remaining byte budget, and reuses already-trimmed Engine lines through observeTrimmed. Generic observe callers still trim their input. Event byte and line budgets, raw wire byte charging, comments, and the two-frame ownership bound are unchanged.

The optimized profile reduces TrimRight CPU share from 5.42% to 2.16% and readFrame from 10.84% to 7.43%. The slower follow-up profile spends more time in existing consumer-side JSON parsing: gjson.getBytes share 27.82% -> 28.42%, parseObject 18.55% -> 20.23%. These profiles identify where CPU time accumulated; they do not establish the cause of the broader run-to-run variation.

Added boundary cases cover CRLF empty events, exact/overflow event bytes, comment bytes and line counts, and a 4096-byte fragment whose next fragment is only CRLF. The existing unterminated 8192-byte line and frame lifecycle tests remain covered.

Verification: the focused race command passed (service test time 2.825s); the same focused Linux tests passed; the optimized Linux service test binary and backend server build succeeded. Exact commands and exit codes are in sse-optimized-verification.json.

Commands and raw sample manifests: sse-focused-manifest.json, sse-optimized-manifest.json, and sse-direct-manifest.json. Detailed per-run values: sse-optimization-summary.json and sse-direct-summary.json. Profile reports listed there are plain text; CPU binary profiles stay in the local artifact directory.

Two diagnostic WSL commands stalled before any Go output. The execution channel recovered without a distribution restart. A process snapshot then showed no residual test or compiler processes, and the final Linux focused test passed before the optimized comparison. Startup diagnostics are retained separately.

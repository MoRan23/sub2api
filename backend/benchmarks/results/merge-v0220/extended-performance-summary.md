# v0.2.20 merge performance comparison

Go 1.27.3; greenteagc,jsonv2; GOMAXPROCS=4; Linux amd64, WSL Ubuntu-24.04. Three alternating baseline/candidate rounds, identical fixtures. Values are round medians; spread is (max-min)/abs(median).

| Fixture | Metric | Baseline | Candidate | Change | Baseline spread | Candidate spread |
|---|---|---:|---:|---:|---:|---:|
| SchedulerCacheReadSharing/accounts_2000_groups_1 | B/op | 421,714.000 | 421,716.000 | 0.00% | 0.00% | 0.00% |
| SchedulerCacheReadSharing/accounts_2000_groups_1 | allocs/op | 10,369.000 | 10,369.000 | 0.00% | 0.00% | 0.00% |
| SchedulerCacheReadSharing/accounts_2000_groups_1 | decoded-retained-B | 7,084,208.000 | 7,094,864.000 | 0.15% | 0.49% | 0.34% |
| SchedulerCacheReadSharing/accounts_2000_groups_1 | ns/op | 2,184,010.000 | 2,105,183.000 | -3.61% | 5.76% | 3.79% |
| SchedulerCacheReadSharing/accounts_2000_groups_1 | p95-ns | 2,640,677.000 | 2,495,090.000 | -5.51% | 27.31% | 8.29% |
| SchedulerCacheReadSharing/accounts_2000_groups_10 | B/op | 421,713.000 | 421,713.000 | 0.00% | 0.00% | 0.00% |
| SchedulerCacheReadSharing/accounts_2000_groups_10 | allocs/op | 10,369.000 | 10,369.000 | 0.00% | 0.00% | 0.00% |
| SchedulerCacheReadSharing/accounts_2000_groups_10 | decoded-retained-B | 11,151,536.000 | 11,149,072.000 | -0.02% | 0.16% | 0.05% |
| SchedulerCacheReadSharing/accounts_2000_groups_10 | ns/op | 2,179,585.000 | 2,139,959.000 | -1.82% | 3.74% | 5.37% |
| SchedulerCacheReadSharing/accounts_2000_groups_10 | p95-ns | 2,588,414.000 | 2,595,472.000 | 0.27% | 4.37% | 6.58% |
| SchedulerCacheReadSharing/accounts_2000_groups_100 | B/op | 421,714.000 | 421,714.000 | 0.00% | 0.00% | 0.00% |
| SchedulerCacheReadSharing/accounts_2000_groups_100 | allocs/op | 10,369.000 | 10,369.000 | 0.00% | 0.00% | 0.00% |
| SchedulerCacheReadSharing/accounts_2000_groups_100 | decoded-retained-B | 46,247,912.000 | 46,272,632.000 | 0.05% | 0.05% | 0.00% |
| SchedulerCacheReadSharing/accounts_2000_groups_100 | ns/op | 2,137,983.000 | 2,141,389.000 | 0.16% | 11.63% | 19.74% |
| SchedulerCacheReadSharing/accounts_2000_groups_100 | p95-ns | 2,798,556.000 | 2,507,396.000 | -10.40% | 11.53% | 34.17% |
| CodexEngineRelay/WS/multiline_128 | B/op | 17,238.000 | 17,238.000 | 0.00% | 0.01% | 0.04% |
| CodexEngineRelay/WS/multiline_128 | allocs/op | 314.000 | 314.000 | 0.00% | 0.00% | 0.00% |
| CodexEngineRelay/WS/multiline_128 | ns/op | 205,188.000 | 250,010.000 | 21.84% | 10.06% | 35.14% |
| CodexEngineRelay/WS/multiline_128 | p95-ns | 287,006.000 | 413,345.000 | 44.02% | 15.09% | 36.68% |

## Per-process resources

| Phase | Metric | Baseline rounds | Candidate rounds |
|---|---|---|---|
| cache-extended | Maximum resident set size (kbytes) | [389960.0, 383756.0, 379896.0] | [389780.0, 389004.0, 390456.0] |
| cache-extended | User time (seconds) | [11.36, 11.49, 10.56] | [12.38, 10.93, 10.99] |
| cache-extended | System time (seconds) | [12.9, 13.02, 12.59] | [13.07, 12.43, 12.28] |
| sse-multiline-extended | Maximum resident set size (kbytes) | [133808.0, 133728.0, 133900.0] | [134020.0, 135436.0, 135212.0] |
| sse-multiline-extended | User time (seconds) | [0.25, 0.22, 0.23] | [0.28, 0.26, 0.35] |
| sse-multiline-extended | System time (seconds) | [0.14, 0.17, 0.16] | [0.18, 0.15, 0.11] |

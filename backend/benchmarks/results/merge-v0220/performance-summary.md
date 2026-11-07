# v0.2.20 merge performance comparison

Go 1.27.3; greenteagc,jsonv2; GOMAXPROCS=4; Linux amd64, WSL Ubuntu-24.04. Three alternating baseline/candidate rounds, identical fixtures. Values are round medians; spread is (max-min)/abs(median).

| Fixture | Metric | Baseline | Candidate | Change | Baseline spread | Candidate spread |
|---|---|---:|---:|---:|---:|---:|
| SchedulerCacheReadSharing/accounts_2000_groups_1 | B/op | 421,726.000 | 421,718.000 | -0.00% | 0.00% | 0.00% |
| SchedulerCacheReadSharing/accounts_2000_groups_1 | allocs/op | 10,369.000 | 10,369.000 | 0.00% | 0.00% | 0.00% |
| SchedulerCacheReadSharing/accounts_2000_groups_1 | decoded-retained-B | 7,084,176.000 | 7,084,288.000 | 0.00% | 0.50% | 0.48% |
| SchedulerCacheReadSharing/accounts_2000_groups_1 | ns/op | 2,282,642.000 | 2,315,218.000 | 1.43% | 5.05% | 3.43% |
| SchedulerCacheReadSharing/accounts_2000_groups_1 | p95-ns | 2,620,676.000 | 2,650,110.000 | 1.12% | 13.76% | 0.87% |
| SchedulerCacheReadSharing/accounts_2000_groups_10 | B/op | 421,724.000 | 421,724.000 | 0.00% | 0.00% | 0.00% |
| SchedulerCacheReadSharing/accounts_2000_groups_10 | allocs/op | 10,369.000 | 10,369.000 | 0.00% | 0.00% | 0.00% |
| SchedulerCacheReadSharing/accounts_2000_groups_10 | decoded-retained-B | 11,151,488.000 | 11,151,536.000 | 0.00% | 0.00% | 0.05% |
| SchedulerCacheReadSharing/accounts_2000_groups_10 | ns/op | 2,143,567.000 | 2,154,821.000 | 0.53% | 4.76% | 3.96% |
| SchedulerCacheReadSharing/accounts_2000_groups_10 | p95-ns | 2,390,126.000 | 2,513,453.000 | 5.16% | 14.18% | 4.09% |
| SchedulerCacheReadSharing/accounts_2000_groups_100 | B/op | 421,726.000 | 421,728.000 | 0.00% | 0.00% | 0.00% |
| SchedulerCacheReadSharing/accounts_2000_groups_100 | allocs/op | 10,369.000 | 10,369.000 | 0.00% | 0.00% | 0.00% |
| SchedulerCacheReadSharing/accounts_2000_groups_100 | decoded-retained-B | 46,260,632.000 | 46,260,344.000 | -0.00% | 0.08% | 0.03% |
| SchedulerCacheReadSharing/accounts_2000_groups_100 | ns/op | 2,177,793.000 | 2,332,151.000 | 7.09% | 6.03% | 13.72% |
| SchedulerCacheReadSharing/accounts_2000_groups_100 | p95-ns | 2,475,912.000 | 2,660,475.000 | 7.45% | 5.72% | 25.16% |
| CodexEngineRelay/HTTP/small_256 | B/op | 16,388.000 | 16,392.000 | 0.02% | 0.29% | 0.51% |
| CodexEngineRelay/HTTP/small_256 | allocs/op | 304.000 | 304.000 | 0.00% | 0.00% | 0.00% |
| CodexEngineRelay/HTTP/small_256 | ns/op | 398,491.000 | 348,727.000 | -12.49% | 43.44% | 9.10% |
| CodexEngineRelay/HTTP/small_256 | p95-ns | 542,145.000 | 410,341.000 | -24.31% | 24.02% | 10.35% |
| CodexEngineRelay/WS/small_256 | B/op | 25,080.000 | 25,023.000 | -0.23% | 0.30% | 0.21% |
| CodexEngineRelay/WS/small_256 | allocs/op | 565.000 | 565.000 | 0.00% | 0.00% | 0.00% |
| CodexEngineRelay/WS/small_256 | ns/op | 529,588.000 | 372,485.000 | -29.67% | 45.65% | 25.49% |
| CodexEngineRelay/WS/small_256 | p95-ns | 749,541.000 | 467,429.000 | -37.64% | 61.78% | 67.13% |
| CodexEngineRelay/HTTP/multiline_128 | B/op | 12,711.000 | 12,648.000 | -0.50% | 0.59% | 0.00% |
| CodexEngineRelay/HTTP/multiline_128 | allocs/op | 181.000 | 181.000 | 0.00% | 0.00% | 0.00% |
| CodexEngineRelay/HTTP/multiline_128 | ns/op | 283,240.000 | 199,675.000 | -29.50% | 3.16% | 25.76% |
| CodexEngineRelay/HTTP/multiline_128 | p95-ns | 421,696.000 | 254,412.000 | -39.67% | 49.66% | 3.98% |
| CodexEngineRelay/WS/multiline_128 | B/op | 17,243.000 | 17,232.000 | -0.06% | 0.35% | 0.30% |
| CodexEngineRelay/WS/multiline_128 | allocs/op | 314.000 | 314.000 | 0.00% | 0.00% | 0.00% |
| CodexEngineRelay/WS/multiline_128 | ns/op | 205,303.000 | 212,514.000 | 3.51% | 37.78% | 20.41% |
| CodexEngineRelay/WS/multiline_128 | p95-ns | 273,246.000 | 290,641.000 | 6.37% | 29.47% | 29.86% |
| CodexEngineRelay/HTTP/slow_write_32 | B/op | 9,288.000 | 9,393.000 | 1.13% | 0.24% | 0.73% |
| CodexEngineRelay/HTTP/slow_write_32 | allocs/op | 80.000 | 81.000 | 1.25% | 0.00% | 0.00% |
| CodexEngineRelay/HTTP/slow_write_32 | ns/op | 36,726,839.000 | 36,721,423.000 | -0.01% | 0.08% | 0.31% |
| CodexEngineRelay/HTTP/slow_write_32 | p95-ns | 37,074,891.000 | 36,921,140.000 | -0.41% | 1.12% | 0.18% |
| CodexEngineRelay/WS/slow_write_32 | B/op | 10,749.000 | 10,824.000 | 0.70% | 0.76% | 1.64% |
| CodexEngineRelay/WS/slow_write_32 | allocs/op | 117.000 | 117.000 | 0.00% | 0.00% | 0.00% |
| CodexEngineRelay/WS/slow_write_32 | ns/op | 36,710,284.000 | 36,699,255.000 | -0.03% | 0.65% | 0.29% |
| CodexEngineRelay/WS/slow_write_32 | p95-ns | 36,990,788.000 | 36,929,088.000 | -0.17% | 2.15% | 0.69% |
| CodexEngineRelay/HTTP/terminal_1MiB | B/op | 6,765,672.000 | 6,765,672.000 | 0.00% | 0.00% | 0.00% |
| CodexEngineRelay/HTTP/terminal_1MiB | allocs/op | 86.000 | 86.000 | 0.00% | 0.00% | 0.00% |
| CodexEngineRelay/HTTP/terminal_1MiB | ns/op | 8,464,905.000 | 7,010,809.000 | -17.18% | 8.50% | 21.04% |
| CodexEngineRelay/HTTP/terminal_1MiB | p95-ns | 11,145,053.000 | 9,105,988.000 | -18.30% | 20.83% | 39.36% |
| CodexEngineRelay/WS/terminal_1MiB | B/op | 6,766,288.000 | 6,766,288.000 | 0.00% | 0.00% | 0.00% |
| CodexEngineRelay/WS/terminal_1MiB | allocs/op | 95.000 | 95.000 | 0.00% | 0.00% | 0.00% |
| CodexEngineRelay/WS/terminal_1MiB | ns/op | 8,091,528.000 | 7,230,570.000 | -10.64% | 17.02% | 12.49% |
| CodexEngineRelay/WS/terminal_1MiB | p95-ns | 10,151,900.000 | 9,568,635.000 | -5.75% | 16.33% | 40.08% |
| CodexEngineRelay/HTTP/terminal_8MiB | B/op | 53,353,599.000 | 53,353,632.000 | 0.00% | 0.00% | 0.00% |
| CodexEngineRelay/HTTP/terminal_8MiB | allocs/op | 95.000 | 95.000 | 0.00% | 0.00% | 0.00% |
| CodexEngineRelay/HTTP/terminal_8MiB | ns/op | 56,073,791.000 | 49,698,400.000 | -11.37% | 3.31% | 6.75% |
| CodexEngineRelay/HTTP/terminal_8MiB | p95-ns | 61,054,050.000 | 54,094,235.000 | -11.40% | 25.26% | 13.75% |
| CodexEngineRelay/WS/terminal_8MiB | B/op | 53,354,233.000 | 53,354,220.000 | -0.00% | 0.00% | 0.00% |
| CodexEngineRelay/WS/terminal_8MiB | allocs/op | 104.000 | 104.000 | 0.00% | 0.00% | 0.00% |
| CodexEngineRelay/WS/terminal_8MiB | ns/op | 56,932,422.000 | 49,679,494.000 | -12.74% | 5.17% | 4.19% |
| CodexEngineRelay/WS/terminal_8MiB | p95-ns | 66,553,331.000 | 52,010,760.000 | -21.85% | 11.04% | 6.56% |
| CodexEngineRelay/HTTP/terminal_32MiB | B/op | 206,527,692.000 | 206,527,729.000 | 0.00% | 0.00% | 0.00% |
| CodexEngineRelay/HTTP/terminal_32MiB | allocs/op | 101.000 | 101.000 | 0.00% | 0.00% | 0.99% |
| CodexEngineRelay/HTTP/terminal_32MiB | ns/op | 228,128,353.000 | 195,283,646.000 | -14.40% | 6.94% | 22.38% |
| CodexEngineRelay/HTTP/terminal_32MiB | p95-ns | 251,465,756.000 | 221,547,063.000 | -11.90% | 16.53% | 39.10% |
| CodexEngineRelay/WS/terminal_32MiB | B/op | 206,528,282.000 | 206,528,238.000 | -0.00% | 0.00% | 0.00% |
| CodexEngineRelay/WS/terminal_32MiB | allocs/op | 110.000 | 110.000 | 0.00% | 0.00% | 0.00% |
| CodexEngineRelay/WS/terminal_32MiB | ns/op | 233,851,619.000 | 208,960,102.000 | -10.64% | 11.28% | 9.04% |
| CodexEngineRelay/WS/terminal_32MiB | p95-ns | 246,502,858.000 | 240,913,622.000 | -2.27% | 45.99% | 13.11% |
| UsageRecordQueueRetainedHeap/ws_replay/queued=32 | B/op | 33,731,288.000 | 33,731,240.000 | -0.00% | 0.00% | 0.00% |
| UsageRecordQueueRetainedHeap/ws_replay/queued=32 | allocs/op | 668.000 | 669.000 | 0.15% | 0.15% | 0.30% |
| UsageRecordQueueRetainedHeap/ws_replay/queued=32 | ns/op | 18,572,538.000 | 16,718,078.000 | -9.98% | 35.54% | 4.83% |
| UsageRecordQueueRetainedHeap/ws_replay/queued=32 | retained-B/task | 3,770.000 | 3,767.000 | -0.08% | 0.27% | 0.11% |
| UsageRecordQueueRetainedHeap/ws_replay/queued=128 | B/op | 134,854,536.000 | 134,855,400.000 | 0.00% | 0.00% | 0.00% |
| UsageRecordQueueRetainedHeap/ws_replay/queued=128 | allocs/op | 2,493.000 | 2,495.000 | 0.08% | 0.12% | 0.16% |
| UsageRecordQueueRetainedHeap/ws_replay/queued=128 | ns/op | 19,594,319.000 | 22,205,384.000 | 13.33% | 22.60% | 26.15% |
| UsageRecordQueueRetainedHeap/ws_replay/queued=128 | retained-B/task | 3,764.000 | 3,764.000 | 0.00% | 0.03% | 0.08% |
| UsageRecordQueueRetainedHeap/systemone_response/queued=32 | B/op | 33,714,936.000 | 33,714,008.000 | -0.00% | 0.00% | 0.00% |
| UsageRecordQueueRetainedHeap/systemone_response/queued=32 | allocs/op | 632.000 | 627.000 | -0.79% | 0.47% | 0.48% |
| UsageRecordQueueRetainedHeap/systemone_response/queued=32 | ns/op | 9,061,115.000 | 8,697,241.000 | -4.02% | 13.22% | 11.51% |
| UsageRecordQueueRetainedHeap/systemone_response/queued=32 | retained-B/task | 3,515.000 | 3,515.000 | 0.00% | 0.09% | 0.20% |
| UsageRecordQueueRetainedHeap/systemone_response/queued=128 | B/op | 134,790,744.000 | 134,791,064.000 | 0.00% | 0.00% | 0.00% |
| UsageRecordQueueRetainedHeap/systemone_response/queued=128 | allocs/op | 2,362.000 | 2,363.000 | 0.04% | 0.17% | 0.08% |
| UsageRecordQueueRetainedHeap/systemone_response/queued=128 | ns/op | 19,829,661.000 | 20,244,993.000 | 2.09% | 23.55% | 30.80% |
| UsageRecordQueueRetainedHeap/systemone_response/queued=128 | retained-B/task | 3,515.000 | 3,515.000 | 0.00% | 0.09% | 0.09% |
| UsageRecordQueueRetainedHeap/grok_request/queued=32 | B/op | 33,713,752.000 | 33,714,008.000 | 0.00% | 0.00% | 0.00% |
| UsageRecordQueueRetainedHeap/grok_request/queued=32 | allocs/op | 628.000 | 627.000 | -0.16% | 0.96% | 0.48% |
| UsageRecordQueueRetainedHeap/grok_request/queued=32 | ns/op | 25,894,483.000 | 25,525,426.000 | -1.43% | 9.22% | 7.86% |
| UsageRecordQueueRetainedHeap/grok_request/queued=32 | retained-B/task | 3,823.000 | 3,823.000 | 0.00% | 0.18% | 0.10% |
| UsageRecordQueueRetainedHeap/grok_request/queued=128 | B/op | 134,790,184.000 | 134,790,920.000 | 0.00% | 0.00% | 0.00% |
| UsageRecordQueueRetainedHeap/grok_request/queued=128 | allocs/op | 2,359.000 | 2,363.000 | 0.17% | 0.13% | 0.08% |
| UsageRecordQueueRetainedHeap/grok_request/queued=128 | ns/op | 75,530,656.000 | 76,554,817.000 | 1.36% | 4.81% | 3.15% |
| UsageRecordQueueRetainedHeap/grok_request/queued=128 | retained-B/task | 3,819.000 | 3,820.000 | 0.03% | 0.03% | 0.03% |

## Per-process resources

| Phase | Metric | Baseline rounds | Candidate rounds |
|---|---|---|---|
| cache | Maximum resident set size (kbytes) | [381080.0, 380732.0, 387512.0] | [389688.0, 387276.0, 388604.0] |
| cache | User time (seconds) | [8.11, 7.95, 7.96] | [8.04, 8.0, 8.52] |
| cache | System time (seconds) | [8.75, 8.7, 8.43] | [8.91, 8.61, 8.89] |
| sse | Maximum resident set size (kbytes) | [353468.0, 379712.0, 377524.0] | [350760.0, 379404.0, 379788.0] |
| sse | User time (seconds) | [12.65, 12.57, 11.94] | [10.78, 11.66, 10.87] |
| sse | System time (seconds) | [1.52, 1.27, 1.0] | [1.06, 1.37, 1.07] |
| billing | Maximum resident set size (kbytes) | [61336.0, 70132.0, 71096.0] | [64304.0, 64156.0, 64200.0] |
| billing | User time (seconds) | [0.24, 0.22, 0.26] | [0.22, 0.3, 0.27] |
| billing | System time (seconds) | [0.2, 0.16, 0.14] | [0.19, 0.13, 0.14] |

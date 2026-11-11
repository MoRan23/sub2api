# Fixed-fixture comparison / 固定场景对比

Baseline: `65e3c9f8b`. New: the final working-tree implementation of generic
combination path filtering and per-policy whitelist checks. Go 1.27.3,
`GOEXPERIMENT=greenteagc,jsonv2`, Linux amd64 under WSL Ubuntu 24.04,
AMD Ryzen 7 9700X, `GOMAXPROCS=4`, `benchtime=250ms`.

Values are medians of three runs; the last column gives the new implementation's
observed range. Baseline/new execution order alternates between rounds. The
portable `CombinationFilter` fixtures have identical inputs and final no-match
results, but the baseline has no path filtering, so it performs different work.
These timings are microbenchmark results, not production request p95 latency.

For the 8 MiB image case, the initial +8.4% timing difference was checked with
longer interleaved 1-second runs. [paired-image-8m.txt](paired-image-8m.txt) has
baseline median 5.200 ms and new median 4.966 ms (-4.5%). Both sets of raw results
are retained.

下表为三轮中位数，保留新版耗时范围。旧版没有路径过滤，因此路径场景用于展示
新增功能的成本；同语义内存优化另见验证报告。8 MiB 图片场景的短时波动已用
交错的一秒测量复核，未出现稳定回退。

| Case | Base ms/op | New ms/op | Change | Base B/op | New B/op | New allocs/op | New timing range ms |
|---|---:|---:|---:|---:|---:|---:|---|
| AuditMemory/text/65536/combination | 0.213 | 0.219 | +2.9% | 294,486 | 294,743 | 85 | 0.209–0.226 |
| AuditMemory/text/1048576/combination | 2.786 | 2.909 | +4.4% | 4,239,898 | 4,241,585 | 94 | 2.867–3.144 |
| AuditMemory/text/4194304/combination | 11.479 | 11.399 | -0.7% | 16,828,088 | 16,837,159 | 102 | 11.204–11.851 |
| AuditMemory/text/8388608/combination | 24.431 | 24.039 | -1.6% | 33,605,816 | 33,605,206 | 102 | 23.216–25.667 |
| AuditMemory/image/65536/combination | 0.051 | 0.048 | -6.0% | 78,031 | 78,284 | 71 | 0.048–0.059 |
| AuditMemory/image/1048576/combination | 0.695 | 0.665 | -4.3% | 1,061,134 | 1,061,431 | 71 | 0.654–0.774 |
| AuditMemory/image/4194304/combination | 2.513 | 2.440 | -2.9% | 4,207,021 | 4,207,260 | 71 | 2.438–3.336 |
| AuditMemory/image/8388608/combination | 5.013 | 5.432 | +8.4% | 8,401,438 | 8,401,636 | 72 | 4.874–5.609 |
| CombinationFilter/plain/65536 | 14.720 | 14.136 | -4.0% | 68,501 | 68,264 | 53 | 13.379–15.831 |
| CombinationFilter/filesystem/65536 | 15.002 | 1.020 | -93.2% | 142,006 | 152,418 | 56 | 0.992–1.097 |
| CombinationFilter/shortpath/65536 | 12.633 | 3.407 | -73.0% | 76,367 | 130,756 | 64 | 3.330–3.503 |
| CombinationFilter/url/65536 | 14.115 | 4.130 | -70.7% | 68,589 | 78,704 | 55 | 4.048–4.167 |
| CombinationFilter/plain/1048576 | 222.747 | 229.673 | +3.1% | 1,065,608 | 1,068,628 | 73 | 221.820–233.452 |
| CombinationFilter/filesystem/1048576 | 254.660 | 15.666 | -93.8% | 2,168,968 | 2,257,606 | 76 | 15.587–16.989 |
| CombinationFilter/shortpath/1048576 | 197.878 | 54.571 | -72.4% | 1,067,724 | 1,903,377 | 227 | 53.263–57.613 |
| CombinationFilter/url/1048576 | 231.304 | 66.943 | -71.1% | 1,059,776 | 1,169,822 | 82 | 64.440–77.298 |
| CombinationFilter/plain/4194304 | 923.731 | 906.092 | -1.9% | 4,217,504 | 4,217,504 | 80 | 848.733–930.036 |
| CombinationFilter/filesystem/4194304 | 932.134 | 66.537 | -92.9% | 8,632,456 | 9,000,974 | 136 | 62.591–67.209 |
| CombinationFilter/shortpath/4194304 | 860.476 | 214.690 | -75.0% | 4,217,016 | 7,566,060 | 755 | 205.342–227.314 |
| CombinationFilter/url/4194304 | 941.816 | 254.961 | -72.9% | 4,208,808 | 4,629,088 | 152 | 253.450–257.633 |
| CombinationFilter/plain/8388608 | 1728.324 | 1806.580 | +4.5% | 8,403,096 | 8,403,336 | 80 | 1720.916–1934.146 |
| CombinationFilter/filesystem/8388608 | 1909.742 | 125.672 | -93.4% | 17,242,784 | 17,985,540 | 214 | 125.282–129.590 |
| CombinationFilter/shortpath/8388608 | 1593.736 | 423.427 | -73.4% | 8,411,320 | 15,148,072 | 1448 | 416.446–428.906 |
| CombinationFilter/url/8388608 | 1722.022 | 513.460 | -70.2% | 8,403,096 | 9,207,696 | 231 | 509.590–540.127 |

FASTPATH
| Case | ns/op median | B/op | allocs/op | Range ns |
|---|---:|---:|---:|---|
| PlainPathScan/65536 | 12818.00 | 0 | 0 | 12648.00–13865.00 |
| PlainPathScan/8388608 | 1620572.00 | 0 | 0 | 1594667.00–1630334.00 |
| EscapedURLSuffix/65536 | 122824.00 | 320 | 4 | 118721.00–124224.00 |
| EscapedURLSuffix/8388608 | 14968286.00 | 320 | 4 | 14906406.00–15428622.00 |
| WhitelistLookup/keys=1/hit=false | 10.54 | 0 | 0 | 10.51–10.57 |
| WhitelistLookup/keys=1/hit=true | 12.86 | 0 | 0 | 12.77–12.95 |
| WhitelistLookup/keys=100/hit=false | 12.13 | 0 | 0 | 12.13–12.19 |
| WhitelistLookup/keys=100/hit=true | 13.59 | 0 | 0 | 13.57–13.63 |
| WhitelistLookup/keys=1000/hit=false | 13.51 | 0 | 0 | 13.48–13.54 |
| WhitelistLookup/keys=1000/hit=true | 15.24 | 0 | 0 | 15.02–15.27 |
| WhitelistBypass/global/keys=1/65536 | 59.60 | 64 | 1 | 58.72–61.02 |
| WhitelistBypass/global/keys=1000/65536 | 283.20 | 64 | 1 | 271.60–284.10 |
| WhitelistBypass/policy/keys=1/65536 | 190.20 | 64 | 1 | 189.60–191.50 |
| WhitelistBypass/policy/keys=1000/65536 | 218.70 | 64 | 1 | 217.50–225.20 |
| WhitelistBypass/global/keys=1/8388608 | 59.28 | 64 | 1 | 57.65–59.33 |
| WhitelistBypass/global/keys=1000/8388608 | 292.80 | 64 | 1 | 266.80–297.00 |
| WhitelistBypass/policy/keys=1/8388608 | 190.00 | 64 | 1 | 188.90–194.70 |
| WhitelistBypass/policy/keys=1000/8388608 | 218.90 | 64 | 1 | 211.50–222.50 |

# Prompt audit allocation comparison

Medians of three fixed-fixture runs. Windows amd64, Go 1.27.3, GOEXPERIMENT=greenteagc,jsonv2, default GOMAXPROCS 16, benchtime 100ms. Sequential timings varied with host load; use paired-redaction.txt to assess the full-text redaction CPU change. Final optimized.txt includes all 32 scenarios after the last implementation change. The apparent 1/4 MiB snapshot timing increases were checked separately with full old/new snapshots in alternating order: paired-snapshot.txt shows median improvements of 5.3% and 3.5%, with no slower optimized round. This paired fixture excludes input construction, explicit GC and heap sampling from timed intervals.

| Scenario | Baseline B/op | New B/op | Reduction | Baseline allocs/op | New allocs/op | Baseline ns/op | New ns/op |
|---|---:|---:|---:|---:|---:|---:|---:|
| text/65536/off | 73920 | 896 | 98.8% | 4 | 4 | 9610 | 298 |
| text/65536/keyword | 1943359 | 291167 | 85.0% | 101 | 67 | 9214508 | 264534 |
| text/65536/combination | 2026492 | 295217 | 85.4% | 166 | 84 | 8874431 | 250572 |
| text/65536/snapshot | 1794068 | 441216 | 75.4% | 90 | 79 | 8579229 | 7914157 |
| text/1048576/off | 1056960 | 896 | 99.9% | 4 | 4 | 150988 | 425 |
| text/1048576/keyword | 26537488 | 4225688 | 84.1% | 138 | 72 | 134219000 | 3428112 |
| text/1048576/combination | 27630944 | 4234057 | 84.7% | 224 | 88 | 135366300 | 3336807 |
| text/1048576/snapshot | 24417848 | 5365200 | 78.0% | 126 | 103 | 143951000 | 161547700 |
| text/4194304/off | 4202688 | 896 | 100.0% | 4 | 4 | 376640 | 283 |
| text/4194304/keyword | 105180920 | 16809864 | 84.0% | 146 | 73 | 731004600 | 17459400 |
| text/4194304/combination | 109426136 | 16818642 | 84.6% | 240 | 90 | 696123500 | 16193429 |
| text/4194304/snapshot | 96775736 | 21099160 | 78.2% | 141 | 119 | 541729700 | 614655500 |
| text/8388608/off | 8396992 | 896 | 100.0% | 4 | 4 | 605916 | 263 |
| text/8388608/keyword | 210032576 | 33579776 | 84.0% | 148 | 76 | 1120018600 | 25968450 |
| text/8388608/combination | 218474360 | 33606040 | 84.6% | 239 | 99 | 1066950600 | 27236975 |
| text/8388608/snapshot | 193238608 | 42062488 | 78.2% | 135 | 119 | 1127666200 | 953879900 |
| image/65536/off | 73920 | 896 | 98.8% | 4 | 4 | 15025 | 414 |
| image/65536/keyword | 150153 | 76809 | 48.8% | 68 | 53 | 92335 | 70476 |
| image/65536/combination | 226974 | 77982 | 65.6% | 127 | 70 | 159778 | 73773 |
| image/65536/snapshot | 76159 | 76809 | -0.9% | 64 | 54 | 70122 | 68761 |
| image/1048576/off | 1056965 | 896 | 99.9% | 4 | 4 | 241468 | 357 |
| image/1048576/keyword | 2117507 | 1060152 | 49.9% | 71 | 53 | 1169694 | 958724 |
| image/1048576/combination | 3177660 | 1061377 | 66.6% | 130 | 70 | 2174962 | 965664 |
| image/1048576/snapshot | 1059545 | 1060150 | -0.1% | 64 | 54 | 967041 | 944772 |
| image/4194304/off | 4202692 | 896 | 100.0% | 4 | 4 | 573834 | 321 |
| image/4194304/keyword | 8411352 | 4206803 | 50.0% | 78 | 55 | 4148971 | 4075808 |
| image/4194304/combination | 12617000 | 4207712 | 66.7% | 136 | 71 | 7773360 | 3450932 |
| image/4194304/snapshot | 4205671 | 4206474 | -0.0% | 65 | 55 | 3663944 | 2749476 |
| image/8388608/off | 8397002 | 896 | 100.0% | 4 | 4 | 1509424 | 251 |
| image/8388608/keyword | 16799351 | 8401409 | 50.0% | 75 | 56 | 6579794 | 5819945 |
| image/8388608/combination | 25202433 | 8402404 | 66.7% | 143 | 72 | 13043388 | 5776320 |
| image/8388608/snapshot | 8400277 | 8401099 | -0.0% | 65 | 56 | 5763208 | 5360196 |

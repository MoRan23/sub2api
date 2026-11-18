# Three-round comparison / 三轮对比

Go 1.27.4; greenteagc,jsonv2; Windows/amd64; GOMAXPROCS=4; CPU affinity 0x55.

Median [minimum, maximum]. Time/throughput are local microbenchmarks; retained heap uses one GC-controlled sample per round.

| Case | Metric | Baseline | Merged | Median change |
|---|---|---:|---:|---:|
| CodexTransformRetryCPU/8MiB/image_false/attempts_15/sparse | ns/op | 2.615e+08 [2.6096e+08, 2.6753e+08] | 2.5893e+08 [2.5306e+08, 2.6663e+08] | -1.0% |
| CodexTransformRetryCPU/8MiB/image_false/attempts_15/sparse | B/op | 6.2921e+08 [6.2921e+08, 6.2921e+08] | 6.2921e+08 [6.2921e+08, 6.2921e+08] | -0.0% |
| CodexTransformRetryCPU/8MiB/image_false/attempts_15/sparse | allocs/op | 1,248 [1,247, 1,253] | 1,245 [1,232, 1,255] | -0.2% |
| GatewayService_ParseSSEUsagePassthrough_MessageDelta | ns/op | 1,101 [1,052, 1,120] | 1,090 [1,082, 1,131] | -1.0% |
| GatewayService_ParseSSEUsagePassthrough_MessageDelta | B/op | 0 [0, 0] | 0 [0, 0] | 0% |
| GatewayService_ParseSSEUsagePassthrough_MessageDelta | allocs/op | 0 [0, 0] | 0 [0, 0] | 0% |
| GatewayService_ParseSSEUsagePassthrough_MessageStart | ns/op | 1,153 [1,146, 1,157] | 1,149 [1,149, 1,158] | -0.3% |
| GatewayService_ParseSSEUsagePassthrough_MessageStart | B/op | 0 [0, 0] | 0 [0, 0] | 0% |
| GatewayService_ParseSSEUsagePassthrough_MessageStart | allocs/op | 0 [0, 0] | 0 [0, 0] | 0% |
| GatewayService_ParseSSEUsage_MessageDelta | ns/op | 2,993 [2,857, 3,004] | 3,050 [3,046, 3,071] | +1.9% |
| GatewayService_ParseSSEUsage_MessageDelta | B/op | 1,640 [1,640, 1,640] | 1,656 [1,656, 1,656] | +1.0% |
| GatewayService_ParseSSEUsage_MessageDelta | allocs/op | 33 [33, 33] | 33 [33, 33] | +0.0% |
| GatewayService_ParseSSEUsage_MessageStart | ns/op | 3,191 [3,126, 3,251] | 3,285 [3,027, 3,426] | +2.9% |
| GatewayService_ParseSSEUsage_MessageStart | B/op | 2,032 [2,032, 2,032] | 2,048 [2,048, 2,048] | +0.8% |
| GatewayService_ParseSSEUsage_MessageStart | allocs/op | 38 [38, 38] | 38 [38, 38] | +0.0% |
| OpenAIReasoningRewrite300/deepseek | ns/op | 1.7261e+08 [1.6659e+08, 2.0125e+08] | 1.254e+06 [1.2151e+06, 1.3155e+06] | -99.3% |
| OpenAIReasoningRewrite300/deepseek | B/op | 5.1527e+08 [5.1527e+08, 5.1527e+08] | 2.3584e+06 [2.3584e+06, 2.3584e+06] | -99.5% |
| OpenAIReasoningRewrite300/deepseek | allocs/op | 2,422 [2,422, 2,423] | 31 [31, 31] | -98.7% |
| OpenAIReasoningRewrite300/ollama | ns/op | 1.8455e+08 [1.8121e+08, 1.8562e+08] | 2.4612e+06 [2.3071e+06, 2.6019e+06] | -98.7% |
| OpenAIReasoningRewrite300/ollama | B/op | 5.2013e+08 [5.2013e+08, 5.2013e+08] | 3.7016e+06 [3.7016e+06, 3.7016e+06] | -99.3% |
| OpenAIReasoningRewrite300/ollama | allocs/op | 2,721 [2,721, 2,721] | 30 [30, 30] | -98.9% |
| OpenAIServiceTierCachedLargeCatalog | ns/op | 539.6 [517, 569.5] | 523.7 [494.2, 526.8] | -2.9% |
| OpenAIServiceTierCachedLargeCatalog | B/op | 424 [424, 424] | 424 [424, 424] | +0.0% |
| OpenAIServiceTierCachedLargeCatalog | allocs/op | 6 [6, 6] | 6 [6, 6] | +0.0% |
| ParseClaudeUsageFromResponseBody | ns/op | 1,034 [1,026, 1,081] | 1,024 [888.6, 1,059] | -1.0% |
| ParseClaudeUsageFromResponseBody | B/op | 320 [320, 320] | 336 [336, 336] | +5.0% |
| ParseClaudeUsageFromResponseBody | allocs/op | 2 [2, 2] | 2 [2, 2] | +0.0% |
| RawChatCompletionsFlushBatch/1 | ns/op | 19,728 [18,524, 22,429] | 18,756 [17,996, 19,261] | -4.9% |
| RawChatCompletionsFlushBatch/1 | B/op | 74,788 [74,788, 74,788] | 74,931 [74,931, 74,931] | +0.2% |
| RawChatCompletionsFlushBatch/1 | allocs/op | 52 [52, 52] | 55 [55, 55] | +5.8% |
| RawChatCompletionsFlushBatch/1 | flushes/op | 4 [4, 4] | 1 [1, 1] | -75.0% |
| RawChatCompletionsFlushBatch/128 | ns/op | 3.3975e+05 [3.134e+05, 3.9503e+05] | 3.1489e+05 [3.143e+05, 3.2079e+05] | -7.3% |
| RawChatCompletionsFlushBatch/128 | B/op | 1.7538e+05 [1.7538e+05, 1.7538e+05] | 1.7552e+05 [1.7552e+05, 1.7552e+05] | +0.1% |
| RawChatCompletionsFlushBatch/128 | allocs/op | 1,195 [1,195, 1,195] | 1,198 [1,198, 1,198] | +0.3% |
| RawChatCompletionsFlushBatch/128 | flushes/op | 258 [258, 258] | 1 [1, 1] | -99.6% |
| UsageRecordLifecycleAuthenticated/ws_replay/bytes=1048576/owned_snapshot | ns/op | 6.0098e+05 [5.8414e+05, 6.2112e+05] | 5.7512e+05 [5.3381e+05, 6.4984e+05] | -4.3% |
| UsageRecordLifecycleAuthenticated/ws_replay/bytes=1048576/owned_snapshot | B/op | 1.06e+06 [1.06e+06, 1.06e+06] | 1.0602e+06 [1.0602e+06, 1.0602e+06] | +0.0% |
| UsageRecordLifecycleAuthenticated/ws_replay/bytes=1048576/owned_snapshot | allocs/op | 119 [119, 119] | 125 [125, 125] | +5.0% |
| UsageRecordQueueRetainedHeap/grok_request/queued=128 | ns/op | 7.4978e+07 [7.3843e+07, 8.74e+07] | 7.141e+07 [6.7329e+07, 7.1733e+07] | -4.8% |
| UsageRecordQueueRetainedHeap/grok_request/queued=128 | B/op | 1.3479e+08 [1.3479e+08, 1.3479e+08] | 1.3479e+08 [1.3479e+08, 1.3479e+08] | -0.0% |
| UsageRecordQueueRetainedHeap/grok_request/queued=128 | allocs/op | 2,363 [2,359, 2,366] | 2,362 [2,360, 2,364] | -0.0% |
| UsageRecordQueueRetainedHeap/grok_request/queued=128 | retained-B/task | 3,819 [3,818, 3,820] | 3,818 [3,818, 3,818] | -0.0% |
| UsageRecordQueueRetainedHeap/grok_request/queued=128 | retained/payload | 0.003642 [0.003641, 0.003643] | 0.003641 [0.003641, 0.003641] | -0.0% |
| UsageRecordQueueRetainedHeap/systemone_response/queued=128 | ns/op | 1.9395e+07 [1.8783e+07, 2.6563e+07] | 1.673e+07 [1.6297e+07, 1.8745e+07] | -13.7% |
| UsageRecordQueueRetainedHeap/systemone_response/queued=128 | B/op | 1.3479e+08 [1.3479e+08, 1.3479e+08] | 1.348e+08 [1.348e+08, 1.348e+08] | +0.0% |
| UsageRecordQueueRetainedHeap/systemone_response/queued=128 | allocs/op | 2,366 [2,364, 2,366] | 2,364 [2,363, 2,365] | -0.1% |
| UsageRecordQueueRetainedHeap/systemone_response/queued=128 | retained-B/task | 3,514 [3,514, 3,515] | 3,514 [3,514, 3,515] | +0.0% |
| UsageRecordQueueRetainedHeap/systemone_response/queued=128 | retained/payload | 0.003351 [0.003351, 0.003353] | 0.003351 [0.003351, 0.003352] | +0.0% |
| UsageRecordQueueRetainedHeap/ws_replay/queued=128 | ns/op | 2.0954e+07 [2.0351e+07, 2.3019e+07] | 2.0429e+07 [1.853e+07, 2.3871e+07] | -2.5% |
| UsageRecordQueueRetainedHeap/ws_replay/queued=128 | B/op | 1.3486e+08 [1.3486e+08, 1.3486e+08] | 1.3486e+08 [1.3486e+08, 1.3486e+08] | +0.0% |
| UsageRecordQueueRetainedHeap/ws_replay/queued=128 | allocs/op | 2,496 [2,496, 2,498] | 2,497 [2,496, 2,498] | +0.0% |
| UsageRecordQueueRetainedHeap/ws_replay/queued=128 | retained-B/task | 3,763 [3,763, 3,763] | 3,763 [3,762, 3,764] | +0.0% |
| UsageRecordQueueRetainedHeap/ws_replay/queued=128 | retained/payload | 0.003589 [0.003588, 0.003589] | 0.003589 [0.003588, 0.00359] | +0.0% |

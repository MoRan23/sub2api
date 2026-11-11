# Controlled heap measurements

Three rounds per scenario/reference count/implementation. Bytes below are medians. sampled_peak_delta is sampled Go HeapAlloc above the pre-fixture baseline; it is not RSS. Each ownership baseline, held and released measurement uses two explicit test-only GCs to expire sync.Pool victim caches. Fixture construction is included identically on both sides. References simulate repeated immutable reuse, not gateway retries.

| Scenario | References | Implementation | Allocated B | Sampled peak B | Held after GC B | Released after GC B | Elapsed ms |
|---|---:|---|---:|---:|---:|---:|---:|
| text_snapshot | 1 | baseline | 193143832 | 92445776 | 8468192 | 5448 | 1294 |
| text_snapshot | 1 | optimized | 42058384 | 33669152 | 8462888 | 144 | 1362 |
| text_snapshot | 5 | baseline | 193144520 | 92442296 | 8464456 | 592 | 1298 |
| text_snapshot | 5 | optimized | 42060008 | 33670896 | 8464504 | 640 | 1362 |
| text_snapshot | 8 | baseline | 193145224 | 92442584 | 8465224 | 464 | 1338 |
| text_snapshot | 8 | optimized | 42060592 | 33668544 | 8465112 | 352 | 1498 |
| image_async_handoff | 1 | baseline | 50385616 | 33600528 | 8397768 | 720 | 15 |
| image_async_handoff | 1 | optimized | 42001568 | 33612768 | 1376 | 240 | 21 |
| image_async_handoff | 5 | baseline | 50386544 | 41997744 | 8398472 | 496 | 13 |
| image_async_handoff | 5 | optimized | 42002496 | 33613696 | 2192 | 128 | 18 |
| image_async_handoff | 8 | baseline | 50387040 | 41998240 | 8398968 | 352 | 14 |
| image_async_handoff | 8 | optimized | 42003136 | 33614336 | 2720 | 16 | 18 |

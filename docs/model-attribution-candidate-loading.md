# 归因检测候选目录自动加载 / Automatic detector candidates

配置页载入或重新加载成功后，已配置有效 HTTP(S) 检测器地址时自动检查连接并获取期望模型候选，无需开启自动检测。全局、分组和新账号首测共用一次查询。此操作只读取检测器目录，不发送模型探针。

连接按钮保留为手动刷新／重试入口。同地址刷新失败保留已获取候选和已选模型，并显示失败状态；修改地址后清除旧地址候选，等待手动连接或重新加载配置。切换地址、重新加载和离开页面会取消旧查询，迟到响应不会覆盖新状态。探针候选继续来自账号目录，期望模型来自检测器参考库。

After loading or reloading the settings page, a configured HTTP(S) detector URL is checked automatically, regardless of the automatic-testing switch. Global, group and new-account policies share one candidate request. No model probes are sent.

The connection button remains available for refresh/retry. A failed same-URL refresh retains candidates and selected values with an error indicator. URL changes invalidate old candidates without querying on every keystroke. Cancellation and generation checks protect reload, URL-change and unmount races. Probe models still use account catalogs; expected models use the detector reference catalog.

## Validation

- Targeted frontend suite: 90 tests passed, including real dropdown rendering, disabled automatic testing, reload, URL changes, failed refresh, out-of-order responses and unmount cancellation.
- `vue-tsc` and targeted ESLint passed.
- No backend, database, detector algorithm or production configuration changes are required for this fix.

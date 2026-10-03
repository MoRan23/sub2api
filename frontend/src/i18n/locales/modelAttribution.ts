export const attributionZh = {
  title: '降智检测', column: '归因测试', description: '定期核对模型归因，按检测结果自动切换账户模型白名单。',
  global: '全局配置', enabled: '启用自动检测', service: 'ModelTrace 服务地址', model: '探针 / 期望模型', high: '高级模型白名单', low: '低级模型白名单',
  connect: '测试连接并获取候选模型', candidates: '已收录的候选模型', saved: '配置已保存，下次有效检测生效。', save: '保存配置', reload: '重新加载配置',
  groupTitle: '分组配置', addGroup: '添加分组配置', inherit: '继承全局配置', independent: '启用独立配置', remove: '移除', group: '分组', version: '配置版本',
  groupHelp: '多分组账户按账户分组优先级从小到大选择首个启用的独立配置；同优先级按分组 ID 排序。',
  notice: '每 10 分钟检测一次，每轮 3 条探针，全局并发 3 个账户。检测会消耗上游额度；执行失败或归因异常不会修改白名单。',
  mappingHelp: '仅替换同名白名单条目，保留自定义别名与通配符映射。启用前请填好服务地址及两份非空白名单。',
  invalid: '请填写有效服务地址、探针模型及两份非空白名单（具体模型 ID，不含通配符）。', conflict: '配置已被其他管理员更新，请重新加载后再保存。',
  overview: '运行概况与历史', queuedCount: '排队 {count}', runningCount: '运行 {count}', total: '保留记录 {count}', refresh: '刷新',
  never: '未检测', latest: '最近结果', run: '立即测试', bulk: '批量归因测试', history: '检测历史', details: '检测详情', account: '账户', time: '检测时间', probability: '概率', top: '最高概率模型',
  selected: '已选择 {count} 个账户', created: '已加入检测队列；已有任务的账户不会重复排队。', next: '下一页', previous: '上一页', empty: '暂无检测记录',
  duration: '耗时', usage: '用量（输入 / 输出 tokens）', action: '白名单处理', changes: '模型映射变化', before: '变更前', after: '变更后', source: '来源', scheduled: '自动', manual: '手动',
  diagnostics: '探针有效性', evidence: '实际出站 / 上游声明模型', reason: '原因', allGroups: '全局默认', close: '关闭', loading: '加载中…', error: '操作失败，请重试。',
  status: { queued: '排队中', running: '检测中', passed: '通过', mismatch: '不通过', abnormal: '异常', failed: '执行失败', skipped: '已跳过' },
  actions: { high: '已应用高级白名单', low: '已应用低级白名单', unchanged: '无需修改', stale: '结果已过期，未修改', none: '未修改' },
  reasons: {
    disabled: '检测未启用', unsupported_account: '仅支持 OpenAI OAuth 实体账户', shadow_account: '共享凭据的影子账户', passthrough_account: '自动透传账户', account_inactive: '账户已停用', scheduling_disabled: '账户关闭调度', account_expired: '账户已过期', account_missing: '账户已删除或不存在',
    model_not_enrolled: 'ModelTrace 尚未收录探针模型', modeltrace_unavailable: 'ModelTrace 服务不可用', modeltrace_failed: 'ModelTrace 请求失败', modeltrace_response_invalid: 'ModelTrace 返回格式无效', modeltrace_bank_invalid: '候选模型库无效', modeltrace_challenges_invalid: '需要三条独立且有效的探针',
    insufficient_valid_outputs: '三份回答未全部有效', modeltrace_probabilities_invalid: '归因概率无效', ambiguous_prediction: '最高概率并列', configuration_or_authorization_changed: '配置、分组、授权或模型限制已变化', configuration_unavailable: '配置读取失败',
    timeout: '检测超过 10 分钟', interrupted: '检测中断，未重放请求', probe_failed: '探针执行失败', probe_model_changed: '实际出站模型与探针不一致', internal_error: '检测内部错误', authorization_changed: '账户授权已变化', upstream_error: '上游请求失败', incomplete_response: '上游流未完成', response_too_large: '上游输出超过限制'
  }
}
export const attributionEn = {
  title: 'Model attribution', column: 'Attribution', description: 'Check model attribution periodically and switch account model allowlists.',
  global: 'Global configuration', enabled: 'Enable automatic detection', service: 'ModelTrace service URL', model: 'Probe / expected model', high: 'High-tier model allowlist', low: 'Low-tier model allowlist',
  connect: 'Test connection and fetch models', candidates: 'Enrolled candidate models', saved: 'Saved. Changes apply after the next valid detection.', save: 'Save configuration', reload: 'Reload configuration',
  groupTitle: 'Group configuration', addGroup: 'Add group configuration', inherit: 'Inherit global configuration', independent: 'Enable independent configuration', remove: 'Remove', group: 'Group', version: 'Configuration version',
  groupHelp: 'For multiple groups, the first enabled override by account-group priority wins; ties use group ID.',
  notice: 'Three probes per account every 10 minutes, up to three accounts concurrently. Probes consume upstream quota. Failed or abnormal detections leave allowlists unchanged.',
  mappingHelp: 'Replace identity allowlist entries only; preserve custom aliases and wildcard mappings. Configure the service and both nonempty allowlists before enabling.',
  invalid: 'Enter a valid service URL, probe model and two nonempty allowlists (concrete model IDs, without wildcards).', conflict: 'Another administrator updated the configuration. Reload before saving.',
  overview: 'Activity and history', queuedCount: '{count} queued', runningCount: '{count} running', total: '{count} retained records', refresh: 'Refresh',
  never: 'Not tested', latest: 'Latest result', run: 'Test now', bulk: 'Batch attribution test', history: 'Detection history', details: 'Detection details', account: 'Account', time: 'Detection time', probability: 'Probability', top: 'Top model',
  selected: '{count} accounts selected', created: 'Queued. Accounts with existing tasks are not queued again.', next: 'Next', previous: 'Previous', empty: 'No detection records',
  duration: 'Duration', usage: 'Usage (input / output tokens)', action: 'Allowlist action', changes: 'Model mapping changes', before: 'Before', after: 'After', source: 'Source', scheduled: 'Automatic', manual: 'Manual',
  diagnostics: 'Probe validity', evidence: 'Outbound / upstream model', reason: 'Reason', allGroups: 'Global default', close: 'Close', loading: 'Loading…', error: 'Operation failed. Please retry.',
  status: { queued: 'Queued', running: 'Running', passed: 'Passed', mismatch: 'Mismatch', abnormal: 'Abnormal', failed: 'Failed', skipped: 'Skipped' },
  actions: { high: 'High-tier allowlist applied', low: 'Low-tier allowlist applied', unchanged: 'No change needed', stale: 'Stale result, no changes', none: 'Unchanged' },
  reasons: {
    disabled: 'Detection disabled', unsupported_account: 'Only physical OpenAI OAuth accounts are supported', shadow_account: 'Shadow account with shared credentials', passthrough_account: 'Automatic passthrough account', account_inactive: 'Account inactive', scheduling_disabled: 'Scheduling disabled', account_expired: 'Account expired', account_missing: 'Account deleted or missing',
    model_not_enrolled: 'Probe model not enrolled in ModelTrace', modeltrace_unavailable: 'ModelTrace unavailable', modeltrace_failed: 'ModelTrace request failed', modeltrace_response_invalid: 'Invalid ModelTrace response', modeltrace_bank_invalid: 'Invalid candidate bank', modeltrace_challenges_invalid: 'Three independent valid probes required',
    insufficient_valid_outputs: 'Not all three answers were valid', modeltrace_probabilities_invalid: 'Invalid attribution probabilities', ambiguous_prediction: 'Highest probability tied', configuration_or_authorization_changed: 'Configuration, groups, authorization or model restrictions changed', configuration_unavailable: 'Configuration unavailable',
    timeout: 'Detection exceeded 10 minutes', interrupted: 'Interrupted; inference was not replayed', probe_failed: 'Probe execution failed', probe_model_changed: 'Outbound model differs from probe', internal_error: 'Internal detection error', authorization_changed: 'Account authorization changed', upstream_error: 'Upstream request failed', incomplete_response: 'Incomplete upstream stream', response_too_large: 'Upstream output limit exceeded'
  }
}

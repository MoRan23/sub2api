package service

var notificationEmailPromptAuditPlaceholders = []string{
	"categories", "rule_group_names", "new_hit_count", "violation_count", "window_hours", "ban_threshold", "account_status",
}

func isNotificationEmailPromptAuditEvent(event string) bool {
	return event == NotificationEmailEventPromptAuditWarning || event == NotificationEmailEventPromptAuditBanned
}

func addNotificationEmailPromptAuditSampleVariables(values map[string]string, event, locale string) {
	if !isNotificationEmailPromptAuditEvent(event) {
		return
	}
	values["categories"], values["rule_group_names"] = "Jailbreak; topic-intent combination", "Example rule group"
	values["new_hit_count"], values["violation_count"] = "2", "4"
	values["window_hours"], values["ban_threshold"], values["account_status"] = "24", "10", "Active"
	if event == NotificationEmailEventPromptAuditBanned {
		values["violation_count"], values["account_status"] = "10", "Disabled"
	}
	if normalizeNotificationLocale(locale) == notificationEmailLocaleChinese {
		values["categories"], values["rule_group_names"] = "破甲；主题意图组合", "示例规则组"
		values["account_status"] = "正常"
		if event == NotificationEmailEventPromptAuditBanned {
			values["account_status"] = "已停用"
		}
	}
}

func notificationEmailPromptAuditTemplates(banned bool) map[string]notificationEmailOfficialTemplate {
	enTitle, zhTitle := "Prompt audit warning", "提示词审计警告"
	enIntro := "Some of your requests were blocked by the platform's jailbreak or topic-intent rules. Please review your use of the service."
	zhIntro := "您的部分请求命中了平台破甲或主题意图组合规则，已被拦截。请检查服务使用行为。"
	enAdvice := "Repeated violations may lead to automatic account suspension."
	zhAdvice := "在统计窗口内继续触发违规，可能导致账户被自动停用。"
	color := "#d97706"
	if banned {
		enTitle, zhTitle, color = "Account disabled by prompt audit", "账户已因提示词审计停用", "#b91c1c"
		enIntro = "Your account reached the prompt audit violation threshold and was automatically disabled."
		zhIntro = "您的账户在统计窗口内达到提示词审计违规阈值，已被自动停用。"
		enAdvice = "Please contact the administrator to request a review and restore access."
		zhAdvice = "如需申诉或恢复访问，请联系管理员复核。"
	}
	return map[string]notificationEmailOfficialTemplate{
		notificationEmailDefaultLocale: {
			Subject: "[{{site_name}}] " + enTitle,
			HTML: notificationEmailCard(color, enTitle, `<p>Hello {{recipient_name}},</p><p>`+enIntro+`</p>
<table style="width:100%;border-collapse:collapse;table-layout:fixed;overflow-wrap:anywhere;">
  <tr><td>Categories</td><td>{{categories}}</td></tr>
  <tr><td>Rule groups</td><td>{{rule_group_names}}</td></tr>
  <tr><td>New blocked requests</td><td>{{new_hit_count}}</td></tr>
  <tr><td>Violations in counting window</td><td>{{violation_count}}</td></tr>
  <tr><td>Counting window (hours)</td><td>{{window_hours}}</td></tr>
  <tr><td>Automatic ban threshold</td><td>{{ban_threshold}}</td></tr>
  <tr><td>Account status</td><td>{{account_status}}</td></tr>
</table><p>`+enAdvice+`</p>`),
		},
		notificationEmailLocaleChinese: {
			Subject: "[{{site_name}}] " + zhTitle,
			HTML: notificationEmailCard(color, zhTitle, `<p>{{recipient_name}}，您好：</p><p>`+zhIntro+`</p>
<table style="width:100%;border-collapse:collapse;table-layout:fixed;overflow-wrap:anywhere;">
  <tr><td>拦截类别</td><td>{{categories}}</td></tr>
  <tr><td>规则组名称</td><td>{{rule_group_names}}</td></tr>
  <tr><td>本次新增拦截数</td><td>{{new_hit_count}}</td></tr>
  <tr><td>窗口内违规次数</td><td>{{violation_count}}</td></tr>
  <tr><td>统计窗口（小时）</td><td>{{window_hours}}</td></tr>
  <tr><td>自动封禁阈值</td><td>{{ban_threshold}}</td></tr>
  <tr><td>账户状态</td><td>{{account_status}}</td></tr>
</table><p>`+zhAdvice+`</p>`),
		},
	}
}

package securityaudit

// Retain the exact former preset to upgrade saved copies without relying on a
// policy's editable ID or name. Customized topic sets remain administrator-owned.
const (
	legacyGovernmentHostTopicRule   = `regex:(?i)(?:^|[^a-z0-9_.-])(?:(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)*gov\.cn|\.gov\.cn)(?:$|[^a-z0-9_.-]|\.(?:$|[^a-z0-9_.-]))`
	legacyGovernmentAgencyTopicRule = `regex:(?i)(?:政府|政务|政務|公安|警务|警務|法院|检察院|檢察院|税务|稅務|海关|海關|财政局|財政局|财政部|財政部|(?:^|[^a-z0-9_.])(?:government(?:al)?|public[\s-]+sector|(?:federal|state|local)[\s-]+(?:agenc(?:y|ies)|departments?)|police[\s-]+(?:departments?|agenc(?:y|ies)|stations?)|tax[\s-]+(?:authorit(?:y|ies)|agenc(?:y|ies)|offices?)|customs[\s-]+(?:authorit(?:y|ies)|agenc(?:y|ies))|(?:district|supreme|federal)[\s-]+courts?)(?:$|[^a-z0-9_.]))`
)

// Editable defaults, never an implicit activation. A topic alone is insufficient:
// a separate operational-intent rule must match the latest user input.
func defaultGovernmentTopicRules() []string {
	return []string{
		// Require a concrete gov.cn host, including the root but not a bare suffix.
		// Both boundaries reject appended domains such as gov.cn.evil.com; an
		// optional final dot is sentence punctuation, not a further host label.
		`regex:(?i)(?:^|[^a-z0-9_.-])(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)*gov\.cn(?:$|[^a-z0-9_.-]|\.(?:$|[^a-z0-9_.-]))`,
	}
}

// Called only while normalizing configuration, never on the request path.
func migrateLegacyGovernmentTopicRules(topics []string) []string {
	if len(topics) == 2 &&
		((topics[0] == legacyGovernmentHostTopicRule && topics[1] == legacyGovernmentAgencyTopicRule) ||
			(topics[1] == legacyGovernmentHostTopicRule && topics[0] == legacyGovernmentAgencyTopicRule)) {
		return defaultGovernmentTopicRules()
	}
	return topics
}

func defaultGovernmentIntentRules() []string {
	return []string{
		`regex:(?i)(?:攻击|攻擊|渗透|滲透|入侵|攻破|漏洞利用|利用漏洞|拖库|拖庫|撞库|撞庫|爆破(?:密码|密碼|口令|登录|登入)|(?:窃取|竊取|盗取|盜取|盗用|盜用)[^\x00]{0,16}(?:账号|帳號|账户|帳戶|密码|密碼|口令|凭据|憑據|令牌|数据|數據)|(?:破坏|破壞)[^\x00]{0,16}(?:可用性|服务|服務|系统|系統)|拒绝服务|拒絕服務|(?:^|[^a-z0-9_])(?:attack(?:s|ed|ing)?|hack(?:s|ed|ing)?|penetration[\s-]+test(?:s|ing)?|pentest(?:s|ing)?|intrusion|exploit(?:s|ed|ing|ation)?|breach(?:es|ed|ing)?|credential[\s-]+(?:theft|stuffing)|(?:steal|exfiltrate|dump)[\s-]+(?:(?:user|account|login)[\s-]+)?(?:credentials?|passwords?|tokens?|databases?|data)|(?:denial[\s-]+of[\s-]+service|ddos|dos)[\s-]+attack(?:s)?|disrupt[\s-]+(?:service|availability))(?:$|[^a-z0-9_]))`,
	}
}

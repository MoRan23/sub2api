package securityaudit

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

func BuildIssueSummaries(result NormalizedResult) []IssueSummary {
	resultCategories := result.Categories
	if len(resultCategories) == 0 {
		resultCategories = result.MatchedScanners
	}
	summaries := make([]IssueSummary, 0, len(resultCategories)+len(result.UnknownCategories))
	for _, category := range resultCategories {
		if category == "cbrn" && result.ScannerBackend == "local-cbrn" && result.PolicyID == "cbrn-topic-intent" {
			evidence := ""
			if match := result.cbrnMatch; match != nil {
				evidence = fmt.Sprintf("主题：%q（%s）；操作意图：%q（%s）",
					RedactPreview(match.Topic.Content, 160), cbrnEvidenceLocation(match.Topic),
					RedactPreview(match.Intent.Content, 160), cbrnEvidenceLocation(match.Intent))
			}
			digest := sha256.Sum256([]byte(evidence))
			summaries = append(summaries, IssueSummary{
				Category: category, ScannerID: category, Title: "核、生物、化学、放射性危害组合拦截",
				Description: "请求携带的主题与最新用户输入中的操作意图同时命中管理员配置规则",
				Severity:    string(RiskCritical), SeverityLabel: riskLabelZH(RiskCritical),
				Action: string(ActionBlock), ActionLabel: actionLabelZH(ActionBlock), Code: "prompt_audit_cbrn",
				Score: 1, Evidence: evidence, EvidenceHash: hex.EncodeToString(digest[:]),
			})
			continue
		}
		if category == "keyword" {
			evidence := RedactPreview(result.ScannerEvidence[category], 160)
			if result.ScannerBackend == "local-keyword" && result.PolicyID == "keyword-substring" && result.matchedKeyword != "" {
				evidence = result.ScannerEvidence[category]
			}
			evidenceHash := strings.TrimPrefix(evidence, "sha256:")
			if len(evidenceHash) != sha256.Size*2 {
				digest := sha256.Sum256([]byte(evidence))
				evidenceHash = hex.EncodeToString(digest[:])
			}
			summaries = append(summaries, IssueSummary{
				Category: category, ScannerID: category, Title: "关键字拦截",
				Description: "提示词命中管理员配置的关键字规则", Severity: string(RiskCritical),
				SeverityLabel: riskLabelZH(RiskCritical), Action: string(ActionBlock),
				ActionLabel: actionLabelZH(ActionBlock), Code: "prompt_audit_keyword",
				Score: 1, Evidence: evidence, EvidenceHash: evidenceHash,
			})
			continue
		}
		definition, ok := ScannerCatalog[category]
		if !ok {
			continue
		}
		evidence := RedactPreview(result.ScannerEvidence[category], 160)
		if evidence == "" {
			evidence = definition.Label
		}
		digest := sha256.Sum256([]byte(evidence))
		summaries = append(summaries, IssueSummary{
			Category: category, ScannerID: category, Title: definition.LabelZH,
			Description: definition.Description, Severity: string(result.RiskLevel),
			SeverityLabel: riskLabelZH(result.RiskLevel), Action: string(result.Action),
			ActionLabel: actionLabelZH(result.Action), Code: "prompt_audit_" + category,
			Score: result.ScannerScores[category], Evidence: evidence,
			EvidenceHash: hex.EncodeToString(digest[:]),
		})
	}
	for _, category := range result.UnknownCategories {
		evidence := "unknown_unsafe"
		digest := sha256.Sum256([]byte(evidence + ":" + category))
		summaries = append(summaries, IssueSummary{
			Category: category, ScannerID: "unknown_unsafe", Title: "未知高风险分类",
			Description: "审计节点返回了未知但不可忽略的高风险分类", Severity: string(RiskCritical),
			SeverityLabel: riskLabelZH(RiskCritical), Action: string(ActionBlock),
			ActionLabel: actionLabelZH(ActionBlock), Code: "prompt_audit_unknown_unsafe",
			Score: 1, Evidence: evidence, EvidenceHash: hex.EncodeToString(digest[:]),
		})
	}
	return summaries
}

func cbrnEvidenceLocation(evidence CBRNMatchEvidence) string {
	source := "携带历史"
	if evidence.Source == "latest_user" {
		source = "最新用户输入"
	}
	return fmt.Sprintf("%s，文本段 %d，字符区间 [%d, %d)", source, evidence.SegmentIndex, evidence.Start, evidence.End)
}

func riskLabelZH(risk RiskLevel) string {
	switch risk {
	case RiskCritical:
		return "严重"
	case RiskHigh:
		return "高"
	case RiskMedium:
		return "中"
	default:
		return "低"
	}
}

func actionLabelZH(action Action) string {
	switch action {
	case ActionBlock:
		return "阻止"
	case ActionWarn:
		return "警告"
	default:
		return "允许"
	}
}

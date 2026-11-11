package securityaudit

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
)

type LegacyEngine interface {
	Check(ctx context.Context, req Request) (*LegacyDecision, error)
}

type PromptEngine interface {
	EffectiveMode() Mode
	CheckKeyword(ctx context.Context, req Request) (*PromptDecision, error)
	Enqueue(ctx context.Context, req Request) error
	Evaluate(ctx context.Context, req Request) (*PromptDecision, error)
}

type Coordinator struct {
	legacy LegacyEngine
	prompt PromptEngine
}

func NewCoordinator(legacy LegacyEngine, prompt PromptEngine) *Coordinator {
	return &Coordinator{legacy: legacy, prompt: prompt}
}

func (c *Coordinator) Check(ctx context.Context, req Request) Decision {
	if c == nil {
		return allowDecision(nil, nil)
	}
	mode := ModeOff
	if c.prompt != nil {
		if preparer, ok := c.prompt.(interface{ prepareRequest(Request) Request }); ok {
			req = preparer.prepareRequest(req)
		}
		mode = c.prompt.EffectiveMode()
		keyword, err := c.prompt.CheckKeyword(ctx, promptEngineRequest(req))
		if err != nil {
			var guardErr *GuardError
			if errors.As(err, &guardErr) && guardErr.Code == ErrorCodeInvalidResponse {
				return prioritize(nil, unavailablePromptDecision(ErrorCodeInvalidResponse))
			}
			return prioritize(nil, unavailablePromptDecision(ErrorCodeUnavailable))
		}
		if keyword == nil {
			return prioritize(nil, unavailablePromptDecision(ErrorCodeUnavailable))
		}
		if keyword.Kind != DecisionAllow {
			return prioritize(nil, keyword)
		}
	}
	switch mode {
	case ModeAsync:
		// Enqueue is deliberately best-effort. The implementation owns a bounded
		// context and copies request memory before it can outlive the Handler.
		_ = c.prompt.Enqueue(ctx, promptEngineRequest(req))
		legacy, _ := c.checkLegacy(ctx, req)
		return prioritize(legacy, nil)
	case ModeBlocking:
		return c.checkBlocking(ctx, req)
	default:
		legacy, _ := c.checkLegacy(ctx, req)
		return prioritize(legacy, nil)
	}
}

func (c *Coordinator) checkBlocking(ctx context.Context, req Request) Decision {
	var wg sync.WaitGroup
	wg.Add(2)
	var legacy *LegacyDecision
	var prompt *PromptDecision
	go func() {
		defer wg.Done()
		legacy, _ = c.checkLegacy(ctx, req)
	}()
	go func() {
		defer wg.Done()
		if c.prompt == nil {
			prompt = unavailablePromptDecision(ErrorCodeUnavailable)
			return
		}
		result, err := c.prompt.Evaluate(ctx, promptEngineRequest(req))
		if err != nil {
			var guardErr *GuardError
			if errors.As(err, &guardErr) && guardErr.Code == ErrorCodeInvalidResponse {
				prompt = unavailablePromptDecision(ErrorCodeInvalidResponse)
				return
			}
			prompt = unavailablePromptDecision(ErrorCodeUnavailable)
			return
		}
		if result == nil {
			prompt = unavailablePromptDecision(ErrorCodeUnavailable)
			return
		}
		prompt = result
	}()
	wg.Wait()
	return prioritize(legacy, prompt)
}

func (c *Coordinator) checkLegacy(ctx context.Context, req Request) (*LegacyDecision, error) {
	if c.legacy == nil {
		return nil, nil
	}
	return c.legacy.Check(ctx, req)
}

func prioritize(legacy *LegacyDecision, prompt *PromptDecision) Decision {
	if legacy != nil && legacy.Blocked {
		status := legacy.StatusCode
		if status < 400 || status > 599 {
			status = http.StatusForbidden
		}
		code := legacy.ErrorCode
		if code == "" {
			code = "content_policy_violation"
		}
		return Decision{
			Kind: DecisionBlock, HTTPStatus: status, ErrorCode: code, ClientMessage: legacy.Message,
			Legacy: legacy, Prompt: prompt, AllowNextStage: false,
		}
	}
	if prompt == nil {
		return allowDecision(legacy, nil)
	}
	switch prompt.Kind {
	case DecisionBlock:
		code := prompt.ErrorCode
		message := "提示词安全审计拒绝了该请求，请调整输入后重试"
		if code == "" {
			code = ErrorCodeBlocked
		}
		if code == ErrorCodeKeywordBlocked {
			message = "提示词包含禁止内容，请调整输入后重试"
			if result := prompt.Result; result != nil && result.ScannerBackend == "local-keyword" && result.PolicyID == "keyword-substring" {
				if result.keywordKind == "jailbreak" {
					message = "禁止破甲，请立即停止使用破甲词，否则将封号" + enforcementCountMessage(prompt.Enforcement)
				} else if result.keywordKind == "other" && result.matchedKeyword != "" && result.matchedContent != "" {
					message += fmt.Sprintf("（命中关键词：%q）", TrimRunes(result.matchedContent, 160))
				}
			}
		}
		if code == ErrorCodeCBRNBlocked {
			message = "请求同时命中危险主题与操作意图，请调整输入后重试"
			if result := prompt.Result; result != nil && result.ScannerBackend == "local-cbrn" && result.PolicyID == "cbrn-topic-intent" && result.cbrnMatch != nil {
				message = "请求命中规则组\"核、生物、化学、放射性危害\"，请停止相关违规请求" + enforcementCountMessage(prompt.Enforcement)
			}
		}
		if code == ErrorCodeCombinationBlocked {
			message = "请求命中组合拦截规则，请停止相关违规请求" + enforcementCountMessage(prompt.Enforcement)
			if result := prompt.Result; result != nil && result.ScannerBackend == "local-combination" && result.PolicyID == "combination-topic-intent" && result.combinationMatch != nil {
				message = fmt.Sprintf("请求命中规则组%q，请停止相关违规请求", TrimRunes(result.combinationMatch.PolicyName, 80)) + enforcementCountMessage(prompt.Enforcement)
			}
		}
		return Decision{Kind: DecisionBlock, HTTPStatus: http.StatusForbidden, ErrorCode: code,
			ClientMessage: message, Legacy: legacy, Prompt: prompt}
	case DecisionInvalid:
		return Decision{Kind: DecisionInvalid, HTTPStatus: http.StatusServiceUnavailable, ErrorCode: ErrorCodeInvalidResponse,
			ClientMessage: "提示词安全审计暂时不可用，请稍后重试", Legacy: legacy, Prompt: prompt}
	case DecisionUnavailable:
		return Decision{Kind: DecisionUnavailable, HTTPStatus: http.StatusServiceUnavailable, ErrorCode: ErrorCodeUnavailable,
			ClientMessage: "提示词安全审计暂时不可用，请稍后重试", Legacy: legacy, Prompt: prompt}
	case DecisionFlag:
		return Decision{Kind: DecisionFlag, HTTPStatus: http.StatusOK, Legacy: legacy, Prompt: prompt, AllowNextStage: true}
	default:
		return allowDecision(legacy, prompt)
	}
}

func enforcementCountMessage(outcome *EnforcementOutcome) string {
	if outcome == nil || !outcome.Recorded {
		return "；记录次数暂不可用"
	}
	if !outcome.Counted {
		return "；本次已记录，未计入当前统计周期"
	}
	message := fmt.Sprintf("；最近 %d 小时已记录 %d 次", outcome.WindowHours, outcome.ViolationCount)
	if outcome.AutoBanned && outcome.UserStatus == "disabled" {
		message += "，账号已自动停用"
	}
	return message
}

func allowDecision(legacy *LegacyDecision, prompt *PromptDecision) Decision {
	return Decision{Kind: DecisionAllow, HTTPStatus: http.StatusOK, Legacy: legacy, Prompt: prompt, AllowNextStage: true}
}

func unavailablePromptDecision(code string) *PromptDecision {
	kind := DecisionUnavailable
	if code == ErrorCodeInvalidResponse {
		kind = DecisionInvalid
	}
	return &PromptDecision{Kind: kind, ErrorCode: code, AllowNextStage: false}
}

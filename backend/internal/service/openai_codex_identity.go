package service

import (
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/google/uuid"
)

// codexUpstreamMinVersion 上游 /backend-api/codex 接受的最低 version 头：
// 若请求携带 version 且低于该值，上游直接 404（issue #3901，2026-07 实测）。
const codexUpstreamMinVersion = "0.144.0"

// codexClientVersionMaxLen 官方版本号均为短 ASCII 串，远低于此上限。
const codexClientVersionMaxLen = 64

// codexClientVersionPattern 允许 0.146.0 与 0.147.0-alpha.4 两类官方形态。
var codexClientVersionPattern = regexp.MustCompile(`^[0-9]+(\.[0-9]+){1,3}(-[0-9A-Za-z.]+)?$`)

// Legacy adapter state is retained for narrow non-plan callers and tests.
// Production OAuth transports never consult it; their immutable fingerprint
// policy snapshot selects Normalize or SafePair explicitly.
var codexIdentityEnforcement = func() *atomic.Bool {
	v := &atomic.Bool{}
	v.Store(true)
	return v
}()

func SetCodexIdentityEnforcementEnabled(enabled bool) {
	codexIdentityEnforcement.Store(enabled)
}

// NormalizeCodexClientVersion 校验并归一化 Codex 客户端版本号，非法值返回空串。
// 该值会被拼进出站 User-Agent 与 version 头，必须拒绝任意字节，避免管理员误填或
// 自动同步拿到异常值时把不可控内容透给上游。
func NormalizeCodexClientVersion(version string) string {
	version = strings.TrimSpace(version)
	if version == "" || len(version) > codexClientVersionMaxLen || !codexClientVersionPattern.MatchString(version) {
		return ""
	}
	return version
}

// buildCodexCLIUserAgent 按版本号拼出规范 Codex TUI User-Agent。
// 完整形态由 openai.EnsureCodexTUIUserAgent 唯一构造。
func buildCodexCLIUserAgent(version string) string {
	if version = NormalizeCodexClientVersion(version); version == "" {
		version = codexCLIVersion
	}
	ua, ok := openai.EnsureCodexTUIUserAgent(codexCLIEnvironmentFingerprint, version)
	if !ok {
		panic("invalid built-in Codex TUI identity")
	}
	return ua
}

// BuiltinOpenAICodexClientVersion exposes the single compile-time fallback to
// read-only settings views without duplicating the version in handlers or UI.
func BuiltinOpenAICodexClientVersion() string {
	return codexCLIVersion
}

// codexCanonicalUserAgentResolver 返回当前生效的规范 Codex User-Agent（后台设置 / 自动同步版本号）。
// 由 SettingService 在装配时注入；解析器内部自带 TTL 缓存，热路径不触库。
type codexCanonicalUserAgentResolver func() string

var (
	codexCanonicalUAMu       sync.RWMutex
	codexCanonicalUAResolver codexCanonicalUserAgentResolver
)

// SetCodexCanonicalUserAgentResolver 注入规范 User-Agent 解析器。
// 未注入或解析结果非法时回退到编译期常量 codexCLIUserAgent。
func SetCodexCanonicalUserAgentResolver(resolver func() string) {
	codexCanonicalUAMu.Lock()
	defer codexCanonicalUAMu.Unlock()
	codexCanonicalUAResolver = resolver
}

// codexCanonicalUserAgent 返回出站规范 User-Agent。
func codexCanonicalUserAgent() string {
	codexCanonicalUAMu.RLock()
	resolver := codexCanonicalUAResolver
	codexCanonicalUAMu.RUnlock()
	if resolver != nil {
		if ua := strings.TrimSpace(resolver()); ua != "" {
			return ua
		}
	}
	return codexCLIUserAgent
}

// codexOutboundIdentity 出站身份三元组，三者必须同源自洽：
// originator 与 User-Agent 首段配套（否则上游 404，issue #3901），
// version 等于 User-Agent 的版本段且不低于上游门槛。
type codexOutboundIdentity struct {
	userAgent  string
	originator string
	version    string
}

// CodexClientIdentityMode selects how the immutable OAuth outbound plan
// projects the Codex client identity triplet at the final wire boundary.
// Normalize uses the gateway/account canonical identity. SafePair preserves a
// recognized client identity while keeping originator and version coherent.
type CodexClientIdentityMode string

const (
	CodexClientIdentityNormalize CodexClientIdentityMode = "normalize"
	CodexClientIdentitySafePair  CodexClientIdentityMode = "safe_pair"
)

// CodexClientIdentityPlan is resolved after credential selection. Apply is a
// pure operation and never needs to look up an account or settings again.
type CodexClientIdentityPlan struct {
	Mode       CodexClientIdentityMode
	UserAgent  string
	Originator string
	Version    string
}

// resolveCodexClientIdentityPlan snapshots the complete client identity while
// the credential account and request policy are being resolved. The final
// projector must not consult the process-wide canonical UA resolver because a
// settings refresh between Resolve and Apply would otherwise split one HTTP
// request or WebSocket connection across two client identities.
func resolveCodexClientIdentityPlan(mode CodexClientIdentityMode, overrideUA string) CodexClientIdentityPlan {
	return resolveCodexClientIdentityPlanFromSnapshot(mode, overrideUA, resolveCodexOutboundIdentity(""))
}

func resolveCodexClientIdentityPlanFromSnapshot(
	mode CodexClientIdentityMode,
	overrideUA string,
	canonical codexOutboundIdentity,
) CodexClientIdentityPlan {
	identity := resolveCodexOutboundIdentityFromSnapshot(overrideUA, canonical)
	if mode == CodexClientIdentityNormalize {
		identity = resolveCodexTUIOutboundIdentityFromSnapshot(overrideUA, canonical)
	}
	return CodexClientIdentityPlan{
		Mode:       mode,
		UserAgent:  identity.userAgent,
		Originator: identity.originator,
		Version:    identity.version,
	}
}

// resolveCodexOutboundIdentity 由候选 User-Agent 推导自洽的出站身份。
// candidateUA 为空时使用规范 User-Agent；推导不出官方身份时整体回退为规范 TUI 身份。
//
// 候选 UA（面板 / 账号级的管理员显式配置）只贡献客户端名与 OS / 架构 / 终端指纹，
// 其自带的版本段一律用当前生效版本重建：一条填写于某个历史版本的 UA 否则会把出站身份
// 永久钉死在陈旧版本上并绕过版本自动同步。
// 需要固定版本请填「Codex 客户端版本号」并关闭自动同步。
func resolveCodexOutboundIdentity(candidateUA string) codexOutboundIdentity {
	canonical := codexCanonicalUserAgent()
	version := codexClientVersionFromUA(canonical)
	canonicalUA, ok := openai.EnsureCodexTUIUserAgent(canonical, version)
	if !ok {
		canonicalUA = buildCodexCLIUserAgent(version)
	}
	canonicalIdentity := codexOutboundIdentity{
		userAgent:  canonicalUA,
		originator: openai.CodexDefaultOriginator,
		version:    version,
	}
	return resolveCodexOutboundIdentityFromSnapshot(candidateUA, canonicalIdentity)
}

func resolveCodexTUIOutboundIdentityFromSnapshot(candidateUA string, canonical codexOutboundIdentity) codexOutboundIdentity {
	version := NormalizeCodexClientVersion(canonical.version)
	if version == "" {
		version = codexCLIVersion
	}
	ua := strings.TrimSpace(candidateUA)
	if ua == "" {
		ua = canonical.userAgent
	}
	ua, ok := openai.EnsureCodexTUIUserAgent(ua, version)
	if !ok {
		ua = buildCodexCLIUserAgent(version)
	}
	return codexOutboundIdentity{
		userAgent:  ua,
		originator: openai.CodexDefaultOriginator,
		version:    version,
	}
}

func resolveCodexOutboundIdentityFromSnapshot(candidateUA string, canonical codexOutboundIdentity) codexOutboundIdentity {
	ua := strings.TrimSpace(candidateUA)
	if ua == "" {
		ua = canonical.userAgent
	}
	originator, pairedUA, ok := openai.PairCodexClientIdentity(ua)
	if !ok {
		originator, pairedUA = canonical.originator, canonical.userAgent
	}
	// 生效版本只有一个来源：规范身份（面板版本号 → 自动同步值 → 内置常量，见
	// SettingService.GetOpenAICodexClientVersion）。UA 与 version 头由此同源派生。
	version := canonical.version
	if version == "" {
		version = codexCLIVersion
	}
	if rebuilt := openai.SetCodexUserAgentVersion(pairedUA, version); rebuilt != "" {
		pairedUA = rebuilt
	}
	return codexOutboundIdentity{userAgent: pairedUA, originator: originator, version: version}
}

// codexClientVersionFromUA 取 UA 的版本段作为生效版本；
// 非法或低于上游门槛（低于则上游 404，issue #3901）时回退编译期常量。
func codexClientVersionFromUA(ua string) string {
	version := NormalizeCodexClientVersion(openai.CodexUserAgentVersion(ua))
	if version == "" || CompareVersions(version, codexUpstreamMinVersion) < 0 {
		return codexCLIVersion
	}
	return version
}

// ensureCodexIdentityHeaders 补齐 OAuth（ChatGPT 内部接口）出站请求所需的 Codex 身份头。
// 已有 User-Agent 与 version 保持不变，交给紧随其后的 enforceCodexIdentityHeaders 收口。
func ensureCodexIdentityHeaders(h http.Header) {
	if h == nil {
		return
	}
	identity := resolveCodexOutboundIdentity("")
	ensureCodexIdentityHeadersFromPlan(h, CodexClientIdentityPlan{
		UserAgent:  identity.userAgent,
		Originator: identity.originator,
		Version:    identity.version,
	})
}

// ensureCodexIdentityHeadersFromPlan fills compatibility headers from the
// immutable request/connection snapshot. In particular, Messages must not
// consult the hot global UA resolver again after its OAuth plan was resolved.
func ensureCodexIdentityHeadersFromPlan(h http.Header, plan CodexClientIdentityPlan) {
	if h == nil {
		return
	}
	if strings.TrimSpace(h.Get("user-agent")) == "" {
		h.Set("user-agent", plan.UserAgent)
	}
	if strings.TrimSpace(h.Get("originator")) == "" {
		h.Set("originator", plan.Originator)
	}
	if strings.TrimSpace(h.Get("version")) == "" {
		h.Set("version", plan.Version)
	}
	h.Set("OpenAI-Beta", "responses=experimental")
}

// applyOpenAICodexProbeHeaders 为合成探测请求补齐 Codex 身份和引擎指纹。
func applyOpenAICodexProbeHeaders(h http.Header) {
	if h == nil {
		return
	}
	ensureCodexIdentityHeaders(h)
	h.Set("X-Codex-Window-ID", uuid.NewString())
}

// enforceCodexIdentityHeaders 收口 OAuth（ChatGPT 内部接口）出站请求的客户端身份头。
// 见 enforceCodexIdentityHeadersWithUA；无账号级自定义 User-Agent 时使用本函数。
func enforceCodexIdentityHeaders(h http.Header) {
	enforceCodexIdentityHeadersWithUA(h, "")
}

// enforceCodexIdentityHeadersWithUA 强制统一 OAuth 出站身份：User-Agent / originator / version
// 一律改写为网关的规范身份，客户端自报身份不参与构造，并保证 originator 与 UA 首段配套。
//
// overrideUA 是账号级自定义 User-Agent：管理员的显式配置仍然生效，但只贡献客户端名与
// OS / 架构 / 终端指纹——版本段与 originator 都由规范身份重建，不允许出现自相矛盾或陈旧的身份。
//
// 强制统一被 gateway.disable_codex_identity_enforcement 关闭时，退回「按最终 User-Agent 配对
// originator + version 门槛校正」的收口语义，供上游策略变动时回滚。
//
// 仅对携带 originator 的请求生效：compat 桥接等非 ChatGPT 内部接口路径会显式删除 originator，
// 不应被补回。需要从缺失身份头恢复的调用方应先调用 ensureCodexIdentityHeaders。
// 必须在所有 User-Agent 改写之后调用。
func enforceCodexIdentityHeadersWithUA(h http.Header, overrideUA string) {
	if h == nil || h.Get("originator") == "" {
		return
	}
	if !codexIdentityEnforcement.Load() {
		pairCodexIdentityHeaders(h)
		return
	}
	plan := resolveCodexClientIdentityPlan(CodexClientIdentityNormalize, overrideUA)
	identity := codexOutboundIdentity{
		userAgent:  plan.UserAgent,
		originator: plan.Originator,
		version:    plan.Version,
	}
	h.Set("user-agent", identity.userAgent)
	h.Set("originator", identity.originator)
	h.Set("version", identity.version)
}

// pairCodexIdentityHeaders 是关闭强制统一后的兜底收口：保留客户端真实身份，
// 仅保证 originator 与最终 User-Agent 首段配套、version 不低于上游门槛（issue #3901）。
func pairCodexIdentityHeaders(h http.Header) {
	pairCodexIdentityHeadersWithFallback(h, resolveCodexOutboundIdentity(""))
}

// pairCodexIdentityHeadersWithFallback retains SafePair's client-preserving
// behavior while making its fallback immutable for an OAuth outbound plan.
// Recognized client identities still pass through exactly as before; only an
// invalid or missing client UA consumes the frozen fallback triplet.
func pairCodexIdentityHeadersWithFallback(h http.Header, fallback codexOutboundIdentity) {
	originator, pairedUA, ok := openai.PairCodexClientIdentity(h.Get("user-agent"))
	if !ok {
		originator, pairedUA = fallback.originator, fallback.userAgent
		h.Set("version", fallback.version)
	}
	h.Set("user-agent", pairedUA)
	h.Set("originator", originator)
	if v := strings.TrimSpace(h.Get("version")); v != "" && CompareVersions(v, codexUpstreamMinVersion) < 0 {
		h.Set("version", codexCLIVersion)
	}
}

// applyCodexClientIdentityPlan is the single final projector used by OAuth
// outbound plans. An intentionally absent originator (for example while a
// compat builder is still transforming the request) remains absent; the
// compatibility layer applies the same immutable plan after restoring the
// required Codex headers.
func applyCodexClientIdentityPlan(h http.Header, plan CodexClientIdentityPlan) {
	if h == nil || strings.TrimSpace(h.Get("originator")) == "" {
		return
	}
	identity := codexOutboundIdentity{
		userAgent:  plan.UserAgent,
		originator: plan.Originator,
		version:    plan.Version,
	}
	if plan.Mode == CodexClientIdentitySafePair {
		pairCodexIdentityHeadersWithFallback(h, identity)
		return
	}
	h.Set("user-agent", identity.userAgent)
	h.Set("originator", identity.originator)
	h.Set("version", identity.version)
}

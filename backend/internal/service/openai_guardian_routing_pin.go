package service

import (
	"container/list"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"golang.org/x/net/publicsuffix"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openaicookies"
)

const (
	openAIGuardianPinLimit = 4096
	openAIGuardianPinBytes = 8 << 20
)

// Only scalars live beyond the request. The registry owns the sole retained
// cookie copy; connections and response references retain opaque IDs.
type openAIGuardianRoutingRequest struct {
	scope       [32]byte
	parentPinID string
	ttl         time.Duration
}

type openAIGuardianRoutingContextKey struct{}

func withOpenAIGuardianRouting(ctx context.Context, routing *openAIGuardianRoutingRequest) context.Context {
	if routing == nil {
		return ctx
	}
	return context.WithValue(ctx, openAIGuardianRoutingContextKey{}, routing)
}

func (s *OpenAIGatewayService) prepareOpenAIGuardianRouting(c *gin.Context, account *Account, plan OpenAIOAuthIdentityPlan, target, proxy string, headers http.Header, body []byte) *openAIGuardianRoutingRequest {
	if c == nil || account == nil || !account.UsesOpenAICodexProtocol() || plan.AuthorizationGeneration == "" || plan.CredentialOwnerNamespace == "" {
		return nil
	}
	value, ok := c.Get("api_key")
	key, valid := value.(*APIKey)
	if !ok || !valid || key == nil || key.UserID <= 0 {
		return nil
	}
	u := openAIGuardianPinURL(target)
	if u == nil {
		return nil
	}
	scope := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%d\x00%d\x00%s\x00%s\x00%s\x00%s\x00%s", key.UserID, getOpenAIGroupIDFromContext(c), account.ID, plan.CredentialOwnerNamespace, plan.AuthorizationGeneration, plan.CredentialOS, u.String(), proxy)))
	routing := &openAIGuardianRoutingRequest{scope: scope, ttl: s.openAIWSResponseStickyTTL()}
	if values := headerValuesCaseInsensitive(headers, openAIGuardianHeader); len(values) == 1 && values[0] == "reviewer" {
		parent := parseRawJSONView(body).Get("client_metadata.parent_response_id")
		if parent.Type == gjson.String && validOpenAIGuardianResponseID(parent.String()) {
			routing.parentPinID = openAIGuardianPins.lookup(scope, parent.String(), time.Now())
		}
	}
	return routing
}

func openAIGuardianRoutingPinID(routing *openAIGuardianRoutingRequest) string {
	if routing == nil {
		return ""
	}
	return routing.parentPinID
}

type openAIGuardianRoutingCookie struct {
	value   string
	expires time.Time
}
type openAIGuardianRoutingPin struct {
	id      string
	scope   [32]byte
	target  string
	cookies []openAIGuardianRoutingCookie
	expires time.Time
	bytes   int
}
type openAIGuardianResponseKey struct {
	scope [32]byte
	id    string
}
type openAIGuardianResponsePin struct {
	key     openAIGuardianResponseKey
	pinID   string
	expires time.Time
	bytes   int
}
type openAIGuardianPinRegistry struct {
	mu                          sync.Mutex
	pins                        map[string]*openAIGuardianRoutingPin
	responses                   map[openAIGuardianResponseKey]*openAIGuardianResponsePin
	order                       list.List
	sequence                    uint64
	bytes, maxEntries, maxBytes int
}

func newOpenAIGuardianPinRegistry() *openAIGuardianPinRegistry {
	return &openAIGuardianPinRegistry{pins: make(map[string]*openAIGuardianRoutingPin), responses: make(map[openAIGuardianResponseKey]*openAIGuardianResponsePin), maxEntries: openAIGuardianPinLimit, maxBytes: openAIGuardianPinBytes}
}

var openAIGuardianPins = newOpenAIGuardianPinRegistry()

func openAIGuardianPinURL(target string) *url.URL {
	u, err := url.Parse(target)
	if err != nil || u.User != nil {
		return nil
	}
	if u.Scheme == "wss" {
		u.Scheme = "https"
	}
	if !openaicookies.AllowedURL(u) {
		return nil
	}
	u.Host = strings.ToLower(u.Host)
	u.RawQuery, u.Fragment = "", ""
	if u.Path == "" {
		u.Path = "/"
	}
	return u
}

func openAIGuardianRoutingRedirectGuard(client *http.Client, target string) *http.Client {
	if client == nil {
		return nil
	}
	expected := openAIGuardianPinURL(target)
	clone := *client
	prior := client.CheckRedirect
	clone.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		strip := func() {
			actual := openAIGuardianPinURL(request.URL.String())
			if expected != nil && actual != nil && expected.String() == actual.String() {
				return
			}
			var kept []string
			for name, values := range request.Header {
				if !strings.EqualFold(name, "Cookie") {
					continue
				}
				for _, line := range values {
					for _, value := range strings.Split(line, ";") {
						cookieName, _, ok := strings.Cut(strings.TrimSpace(value), "=")
						if ok && cookieName != "__oailb" && cookieName != "__cflb" {
							kept = append(kept, strings.TrimSpace(value))
						}
					}
				}
				delete(request.Header, name)
			}
			if len(kept) > 0 {
				request.Header.Set("Cookie", strings.Join(kept, "; "))
			}
		}
		strip()
		if prior != nil {
			err := prior(request, via)
			strip()
			return err
		}
		return nil
	}
	return &clone
}

func (r *openAIGuardianPinRegistry) capture(routing *openAIGuardianRoutingRequest, target string, headers http.Header, now time.Time) string {
	if routing == nil {
		return ""
	}
	u := openAIGuardianPinURL(target)
	if u == nil {
		return ""
	}
	var candidates []string
	seen := make(map[string]bool, 2)
	for _, line := range headers.Values("Set-Cookie") {
		name, _, ok := strings.Cut(line, "=")
		name = strings.TrimSpace(name)
		if !ok || (name != "__oailb" && name != "__cflb") {
			continue
		}
		if seen[name] || len(line) > 8192 {
			return ""
		}
		seen[name] = true
		candidates = append(candidates, line)
	}
	ttl := routing.ttl
	if ttl <= 0 {
		ttl = time.Hour
	}
	pin := &openAIGuardianRoutingPin{scope: routing.scope, target: u.String(), expires: now.Add(ttl), bytes: 512 + len(u.String())}
	for _, line := range candidates {
		cookie, err := http.ParseSetCookie(line)
		if err != nil || cookie.MaxAge < 0 || cookie.Value == "" {
			continue
		}
		expires := cookie.Expires
		if cookie.MaxAge > 0 {
			// Retention never exceeds sticky TTL, even for extreme Max-Age values.
			expires = pin.expires
			if int64(cookie.MaxAge) < int64(ttl/time.Second) {
				expires = now.Add(time.Duration(cookie.MaxAge) * time.Second)
			}
		}
		if !expires.IsZero() && !expires.After(now) {
			continue
		}
		jar, _ := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
		jar.SetCookies(u, []*http.Cookie{cookie})
		accepted := jar.Cookies(u)
		if len(accepted) != 1 {
			continue
		}
		value := accepted[0].String()
		pin.cookies = append(pin.cookies, openAIGuardianRoutingCookie{value: value, expires: expires})
		pin.bytes += len(value) + 128
	}
	if len(pin.cookies) == 0 {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sequence++
	pin.id = strconv.FormatUint(r.sequence, 36)
	r.order.PushBack(pin)
	r.pins[pin.id] = pin
	r.bytes += pin.bytes
	r.trimLocked()
	if r.pins[pin.id] == nil {
		return ""
	}
	return pin.id
}

func validOpenAIGuardianResponseID(value string) bool {
	return len(value) > 0 && len(value) <= 1024 && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\r\n\x00")
}

func (r *openAIGuardianPinRegistry) observe(scope [32]byte, pinID string, payload []byte, now time.Time) {
	if pinID == "" {
		return
	}
	// The transport is already decoding one complete message. Scalar extraction
	// borrows it; only the response identifier is copied into the bounded index.
	view := parseRawJSONView(payload)
	event := view.Get("type").String()
	if event != "response.created" && event != "response.in_progress" && event != "response.completed" {
		return
	}
	if !gjson.ValidBytes(payload) {
		return
	}
	id := view.Get("response.id")
	if id.Type != gjson.String || !validOpenAIGuardianResponseID(id.String()) {
		return
	}
	r.bind(scope, pinID, id.String(), now)
}

func (r *openAIGuardianPinRegistry) bind(scope [32]byte, pinID, responseID string, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	pin := r.pins[pinID]
	if pin == nil || pin.scope != scope || !pin.expires.After(now) || !validOpenAIGuardianResponseID(responseID) {
		return
	}
	key := openAIGuardianResponseKey{scope: scope, id: responseID}
	if _, exists := r.responses[key]; exists {
		return
	}
	key.id = strings.Clone(responseID)
	ref := &openAIGuardianResponsePin{key: key, pinID: pinID, expires: pin.expires, bytes: len(key.id) + 256}
	r.order.PushBack(ref)
	r.responses[key] = ref
	r.bytes += ref.bytes
	r.trimLocked()
}

func (r *openAIGuardianPinRegistry) lookup(scope [32]byte, responseID string, now time.Time) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	ref := r.responses[openAIGuardianResponseKey{scope: scope, id: responseID}]
	if ref == nil || !ref.expires.After(now) {
		return ""
	}
	pin := r.pins[ref.pinID]
	if pin == nil || !pin.expires.After(now) {
		return ""
	}
	return pin.id
}

func (r *openAIGuardianPinRegistry) apply(routing *openAIGuardianRoutingRequest, target string, headers http.Header, now time.Time) {
	if routing == nil || routing.parentPinID == "" {
		return
	}
	u := openAIGuardianPinURL(target)
	if u == nil {
		return
	}
	r.mu.Lock()
	pin := r.pins[routing.parentPinID]
	var values []string
	if pin != nil && pin.scope == routing.scope && pin.target == u.String() && pin.expires.After(now) {
		for _, cookie := range pin.cookies {
			if cookie.expires.IsZero() || cookie.expires.After(now) {
				values = append(values, cookie.value)
			}
		}
	}
	r.mu.Unlock()
	if len(values) == 0 {
		return
	}
	for _, header := range headers.Values("Cookie") {
		for _, value := range strings.Split(header, ";") {
			name, _, ok := strings.Cut(strings.TrimSpace(value), "=")
			if ok && name != "__oailb" && name != "__cflb" {
				values = append(values, strings.TrimSpace(value))
			}
		}
	}
	headers.Set("Cookie", strings.Join(values, "; "))
}

func (r *openAIGuardianPinRegistry) trimLocked() {
	for len(r.pins)+len(r.responses) > r.maxEntries || r.bytes > r.maxBytes {
		front := r.order.Front()
		if front == nil {
			break
		}
		switch entry := front.Value.(type) {
		case *openAIGuardianRoutingPin:
			r.bytes -= entry.bytes
			delete(r.pins, entry.id)
		case *openAIGuardianResponsePin:
			r.bytes -= entry.bytes
			delete(r.responses, entry.key)
		}
		r.order.Remove(front)
	}
}

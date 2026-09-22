package openaicookies

import (
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/publicsuffix"
)

func AllowedName(name string) bool {
	switch name {
	case "__cf_bm", "__cflb", "__cfruid", "__cfseq", "__cfwaitingroom", "__oailb", "_cfuvid", "cf_clearance", "cf_ob_info", "cf_use_ob":
		return true
	default:
		return strings.HasPrefix(name, "cf_chl_")
	}
}

func AllowedURL(target *url.URL) bool {
	if target == nil || target.Scheme != "https" {
		return false
	}
	host := strings.ToLower(target.Hostname())
	return host == "chatgpt.com" || host == "chat.openai.com" || host == "chatgpt-staging.com" || strings.HasSuffix(host, ".chatgpt.com") || strings.HasSuffix(host, ".chatgpt-staging.com")
}

func newJar() *cookiejar.Jar {
	jar, _ := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	return jar
}

func defaultPath(path string) string {
	if !strings.HasPrefix(path, "/") {
		return "/"
	}
	if index := strings.LastIndex(path, "/"); index > 0 {
		return path[:index]
	}
	return "/"
}

func normalize(target *url.URL, cookie *http.Cookie, now time.Time) (Entry, bool, bool) {
	if !AllowedURL(target) || cookie == nil || !AllowedName(cookie.Name) || len(cookie.Value) > 8192 {
		return Entry{}, false, false
	}
	path := cookie.Path
	if !strings.HasPrefix(path, "/") {
		path = defaultPath(target.Path)
	}
	// Let the standard jar validate Domain/public suffix and cookie acceptance.
	// Give the candidate a temporary lifetime so deletion cookies are validated too.
	check := *cookie
	check.MaxAge, check.Expires, check.Path = 0, time.Now().Add(time.Hour), path
	check.Value = "validation"
	probe := *target
	probe.Path, probe.RawPath = path, ""
	jar := newJar()
	jar.SetCookies(target, []*http.Cookie{&check})
	if len(jar.Cookies(&probe)) != 1 {
		return Entry{}, false, false
	}
	domain := strings.ToLower(strings.TrimPrefix(cookie.Domain, "."))
	hostOnly := domain == ""
	if hostOnly {
		domain = strings.ToLower(target.Hostname())
	}
	entry := Entry{Name: cookie.Name, Value: cookie.Value, Domain: domain, Path: path, HostOnly: hostOnly, Secure: cookie.Secure, HTTPOnly: cookie.HttpOnly, Quoted: cookie.Quoted, SameSite: cookie.SameSite, CreatedAt: now, UpdatedAt: now}
	entry.Key = CookieKey(entry.Name, entry.Domain, entry.Path)
	if cookie.MaxAge < 0 {
		return entry, true, true
	}
	if cookie.MaxAge > 0 {
		// Cap overflow at the latest ordinary HTTP-date, not a duration wraparound.
		limit := time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
		if int64(cookie.MaxAge) > limit.Unix()-now.Unix() {
			entry.ExpiresAt = limit
		} else {
			entry.ExpiresAt = time.Unix(now.Unix()+int64(cookie.MaxAge), int64(now.Nanosecond())).UTC()
		}
	} else {
		entry.ExpiresAt = cookie.Expires
	}
	if !entry.ExpiresAt.IsZero() && !entry.ExpiresAt.After(now) {
		return entry, true, true
	}
	return entry, false, entry.Valid()
}

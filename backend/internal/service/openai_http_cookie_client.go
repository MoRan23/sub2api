package service

import "net/http"

// Model and usage auxiliary requests keep their own timeout/redirect policies
// while sharing the same account-scoped HTTP cookie manager as gateway sends.
func openAIHTTPCookieClient(upstream HTTPUpstream, client *http.Client, request *http.Request) *http.Client {
	if provider, ok := upstream.(interface {
		OpenAICookieClient(*http.Client, *http.Request) *http.Client
	}); ok {
		return provider.OpenAICookieClient(client, request)
	}
	return client
}

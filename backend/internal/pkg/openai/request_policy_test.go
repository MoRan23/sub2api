package openai

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/imroc/req/v3"
)

func TestApplyCodexResidencyHeader(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[enabled], func(t *testing.T) {
			header := http.Header{
				"x-openai-internal-codex-residency": {"eu", "us"},
				"X-OPENAI-INTERNAL-CODEX-RESIDENCY": {""},
				"Accept":                            {"application/json"},
			}
			before := header.Clone()
			ApplyCodexResidencyHeader(header, enabled)
			if !enabled && !reflect.DeepEqual(header, before) {
				t.Fatalf("disabled policy modified headers: %v", header)
			}
			if enabled {
				want := http.Header{"Accept": {"application/json"}}
				want.Set(CodexResidencyHeaderName, "us")
				if !reflect.DeepEqual(header, want) {
					t.Fatalf("headers = %v, want %v", header, want)
				}
			}
		})
	}
}

func TestCodexResidencyRedirectGuardActualRequests(t *testing.T) {
	var destinationHeaders http.Header
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		destinationHeaders = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer destination.Close()
	var initialHeaders http.Header
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/same" {
			initialHeaders = r.Header.Clone()
			http.Redirect(w, r, "/cross", http.StatusFound)
			return
		}
		if r.Header.Get(CodexResidencyHeaderName) != "us" {
			t.Error("same-origin redirect lost residency")
		}
		http.Redirect(w, r, destination.URL, http.StatusFound)
	}))
	defer source.Close()
	request, _ := http.NewRequest(http.MethodGet, source.URL+"/same", nil)
	request.Header.Set("X-Existing-Request", "retained")
	ApplyCodexResidencyHeader(request.Header, true)
	request = MarkCodexResidencyRequest(request)
	base := &http.Client{}
	response, err := HTTPClientWithCodexResidencyRedirectGuard(base).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if initialHeaders.Get(CodexResidencyHeaderName) != "us" || destinationHeaders.Get(CodexResidencyHeaderName) != "" {
		t.Fatalf("unexpected residency: initial=%v cross-origin=%v", initialHeaders, destinationHeaders)
	}
	if initialHeaders.Get("X-Existing-Request") != "retained" || destinationHeaders.Get("X-Existing-Request") != "retained" {
		t.Fatal("residency redirect guard changed an unrelated header")
	}
	if base.CheckRedirect != nil {
		t.Fatal("shared client's redirect policy was mutated")
	}
	// Ordinary non-OpenAI traffic is unaffected, even if it declares this header.
	ordinary, _ := http.NewRequest(http.MethodGet, source.URL+"/same", nil)
	ordinary.Header.Set(CodexResidencyHeaderName, "us")
	response, err = HTTPClientWithCodexResidencyRedirectGuard(base).Do(ordinary)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if destinationHeaders.Get(CodexResidencyHeaderName) != "us" {
		t.Fatal("unmarked request was modified")
	}
}

func TestCodexResidencyRedirectGuardPreservesExistingDecision(t *testing.T) {
	expected := errors.New("existing redirect policy")
	called := false
	base := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		called = true
		return expected
	}}
	request, _ := http.NewRequest(http.MethodGet, "https://example.invalid", nil)
	if err := HTTPClientWithCodexResidencyRedirectGuard(base).CheckRedirect(request, nil); !errors.Is(err, expected) || !called {
		t.Fatalf("existing redirect decision not preserved: %v", err)
	}
}

func TestReqClientRequestPolicyActualRequests(t *testing.T) {
	var received http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = r.Header.Clone()
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()
	client := req.C().SetCommonHeader(CodexResidencyHeaderName, "common").
		SetCommonHeader("User-Agent", "local-test-client/1.0").
		SetCommonHeader("X-Existing-Common", "retained")
	originalTransport := client.GetClient().Transport
	for _, enabled := range []bool{false, true} {
		policy := DefaultRequestPolicy()
		policy.CodexResidencyUS = enabled
		ctx := WithRequestPolicy(context.Background(), policy)
		derived := ReqClientWithRequestPolicy(client, ctx)
		_, err := derived.R().SetHeader(CodexResidencyHeaderName, "eu").
			SetHeader("Authorization", "Bearer local-test-token").
			SetHeader("X-Existing-Request", "retained").Get(server.URL)
		if err != nil {
			t.Fatal(err)
		}
		want := "eu"
		if enabled {
			want = "us"
		}
		if got := received.Values(CodexResidencyHeaderName); len(got) != 1 || got[0] != want {
			t.Fatalf("enabled=%v received %v, want %s", enabled, got, want)
		}
		for name, value := range map[string]string{
			"User-Agent": "local-test-client/1.0", "X-Existing-Common": "retained",
			"Authorization": "Bearer local-test-token", "X-Existing-Request": "retained",
		} {
			if received.Get(name) != value {
				t.Errorf("enabled=%v changed unrelated header %s", enabled, name)
			}
		}
		if derived.GetClient().Transport != originalTransport {
			t.Fatal("derived client must retain shared connection pool")
		}
	}
	if client.Headers.Get(CodexResidencyHeaderName) != "common" || client.GetClient().Transport != originalTransport {
		t.Fatal("shared client was modified")
	}
}

func TestReqClientRequestPolicyStripsCrossOrigin(t *testing.T) {
	var received string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = r.Header.Get(CodexResidencyHeaderName)
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(CodexResidencyHeaderName) != "us" {
			t.Error("initial request omitted forced residency")
		}
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer source.Close()
	_, err := ReqClientWithRequestPolicy(req.C(), context.Background()).R().Get(source.URL)
	if err != nil {
		t.Fatal(err)
	}
	if received != "" {
		t.Fatalf("cross-origin residency = %q", received)
	}
}

func TestReqClientRequestPolicyPreservesCookieState(t *testing.T) {
	seen := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Get("Cookie")
		http.SetCookie(w, &http.Cookie{Name: "existing-session", Value: "cookie-value", Path: "/"})
	}))
	defer server.Close()
	client := req.C()
	for range 2 {
		derived := ReqClientWithRequestPolicy(client, context.Background())
		if derived.GetClient().Jar != client.GetClient().Jar {
			t.Fatal("derived client must retain existing cookie jar")
		}
		if _, err := derived.R().Get(server.URL); err != nil {
			t.Fatal(err)
		}
	}
	if first := <-seen; first != "" {
		t.Fatalf("initial request unexpectedly has cookies: %q", first)
	}
	if second := <-seen; second != "existing-session=cookie-value" {
		t.Fatalf("second request lost server cookie: %q", second)
	}
}

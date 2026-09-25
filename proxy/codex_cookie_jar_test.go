package proxy

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/codex2api/auth"
)

func TestCodexCookieJarIsOptInAndIsolatedPerAccount(t *testing.T) {
	ResetAllCodexCookieJars()
	t.Cleanup(ResetAllCodexCookieJars)
	a := &auth.Account{DBID: 101, AccountID: "workspace-a"}
	b := &auth.Account{DBID: 102, AccountID: "workspace-b"}

	setCookieJarTestSetting(false)
	if jar := CodexCookieJarForAccount(a); jar != nil {
		t.Fatal("cookie jar should be disabled by default/false")
	}

	setCookieJarTestSetting(true)
	jarA := CodexCookieJarForAccount(a)
	if jarA == nil {
		t.Fatal("enabled cookie jar is nil")
	}
	if jarA != CodexCookieJarForAccount(a) {
		t.Fatal("same account did not reuse the same jar")
	}
	if jarA == CodexCookieJarForAccount(b) {
		t.Fatal("different accounts share a cookie jar")
	}
}

func TestCodexCookieJarUpdatesReplacementAndExpiry(t *testing.T) {
	ResetAllCodexCookieJars()
	t.Cleanup(ResetAllCodexCookieJars)
	setCookieJarTestSetting(true)
	account := &auth.Account{DBID: 103, AccountID: "workspace"}
	wsURL, _ := url.Parse("wss://chatgpt.com/backend-api/codex/responses")
	jar := CodexCookieJarForAccount(account)

	UpdateCodexCookieJarFromResponse(account, wsURL, &http.Response{
		StatusCode: http.StatusSwitchingProtocols,
		Header: http.Header{
			"Set-Cookie": []string{
				"session=old; Path=/; Secure; Max-Age=3600",
			},
		},
	})
	if got := jar.Cookies(mustURL(t, "https://chatgpt.com/backend-api/codex/responses")); len(got) != 1 || got[0].Value != "old" {
		t.Fatalf("initial cookie = %#v", got)
	}

	// A later Set-Cookie with the same name replaces the old value.
	UpdateCodexCookieJarFromResponse(account, wsURL, &http.Response{
		StatusCode: http.StatusSwitchingProtocols,
		Header: http.Header{
			"Set-Cookie": []string{"session=new; Path=/; Secure; Max-Age=3600"},
		},
	})
	if got := jar.Cookies(mustURL(t, "https://chatgpt.com/backend-api/codex/responses")); len(got) != 1 || got[0].Value != "new" {
		t.Fatalf("replaced cookie = %#v", got)
	}

	// Max-Age=0 is a deletion cookie and must remove the previous value.
	UpdateCodexCookieJarFromResponse(account, wsURL, &http.Response{
		StatusCode: http.StatusSwitchingProtocols,
		Header: http.Header{
			"Set-Cookie": []string{"session=gone; Path=/; Secure; Max-Age=0"},
		},
	})
	if got := jar.Cookies(mustURL(t, "https://chatgpt.com/backend-api/codex/responses")); len(got) != 0 {
		t.Fatalf("deleted cookie still present = %#v", got)
	}

	// Expires in the past is also removed, which covers upstreams that do not
	// send Max-Age on refresh/deletion responses.
	jar.SetCookies(mustURL(t, "https://chatgpt.com/backend-api/codex/responses"), []*http.Cookie{{
		Name: "expires", Value: "gone", Path: "/", Secure: true,
		Expires: time.Now().Add(-time.Minute),
	}})
	if got := jar.Cookies(mustURL(t, "https://chatgpt.com/backend-api/codex/responses")); len(got) != 0 {
		t.Fatalf("expired cookie still present = %#v", got)
	}
}

func TestApplyCodexCookieJarToHeadersPreservesSecureWSCookies(t *testing.T) {
	ResetAllCodexCookieJars()
	t.Cleanup(ResetAllCodexCookieJars)
	setCookieJarTestSetting(true)
	account := &auth.Account{DBID: 104, AccountID: "workspace"}
	jar := CodexCookieJarForAccount(account)
	jar.SetCookies(mustURL(t, "https://chatgpt.com/backend-api/codex/responses"), []*http.Cookie{
		{Name: "secure_session", Value: "secret", Path: "/", Secure: true},
		{Name: "regular", Value: "value", Path: "/", Secure: false},
	})
	headers := http.Header{"Cookie": []string{"caller=kept"}}
	ApplyCodexCookieJarToHeaders(account, "wss://chatgpt.com/backend-api/codex/responses", headers)
	value := headers.Get("Cookie")
	for _, expected := range []string{"caller=kept", "secure_session=secret", "regular=value"} {
		if !strings.Contains(value, expected) {
			t.Fatalf("Cookie header %q does not contain %q", value, expected)
		}
	}
}

func TestPooledCodexClientReusesAndRefreshesCookies(t *testing.T) {
	ResetAllCodexCookieJars()
	t.Cleanup(ResetAllCodexCookieJars)
	setCookieJarTestSetting(true)
	account := &auth.Account{DBID: 105, AccountID: "workspace"}
	var seenCookie string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenCookie = r.Header.Get("Cookie")
		if r.URL.Path == "/first" {
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "refreshed", Path: "/", MaxAge: 3600})
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	client := getPooledClient(account, "")
	defer recyclePooledClient(account, "")
	if response, err := client.Get(server.URL + "/first"); err != nil {
		t.Fatalf("first request: %v", err)
	} else {
		response.Body.Close()
	}
	if response, err := client.Get(server.URL + "/second"); err != nil {
		t.Fatalf("second request: %v", err)
	} else {
		response.Body.Close()
	}
	if !strings.Contains(seenCookie, "session=refreshed") {
		t.Fatalf("second request Cookie = %q", seenCookie)
	}
}

func TestCodexEdgeRotationStartsFromCookieAndAdvances(t *testing.T) {
	ResetAllCodexCookieJars()
	t.Cleanup(ResetAllCodexCookieJars)
	s := DefaultRuntimeSettings()
	s.CodexCookieJarEnabled = true
	s.CodexEdgeRotationEnabled = true
	s.CodexEdgeRotationIntervalSec = 1
	s.CodexEdgeRotationMax = 3
	ApplyRuntimeSettings(s)
	account := &auth.Account{DBID: 106, AccountID: "workspace"}
	payload, _ := json.Marshal(map[string]any{"host": "chat.gateway.unified-2.api.openai.com"})
	value := "e30." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
	u := mustURL(t, "https://chatgpt.com/backend-api/codex/responses")
	UpdateCodexCookieJarFromResponse(account, u, &http.Response{Header: http.Header{"Set-Cookie": []string{"__oailb=" + value + "; Path=/; Max-Age=3600"}}})
	if got := CodexBaseURLForAccount(account); got != CodexBaseURL {
		t.Fatalf("edge routing must keep public API URL, got %q", got)
	}
	headers := http.Header{}
	ApplyCodexCookieJarToHeaders(account, "https://chatgpt.com/backend-api/codex/responses", headers)
	if !strings.Contains(decodeOailbPayloadForTest(headers.Get("Cookie")), "unified-2") {
		t.Fatalf("starting edge was not wrapped in __oailb: %q", headers.Get("Cookie"))
	}
	// A response may refresh __oailb with a different host, but that must not
	// change the active rotation sequence. The next request still injects the
	// selected node before sending.
	otherPayload, _ := json.Marshal(map[string]any{"host": "chat.gateway.unified-1.api.openai.com"})
	otherValue := "e30." + base64.RawURLEncoding.EncodeToString(otherPayload) + ".sig"
	UpdateCodexCookieJarFromResponse(account, u, &http.Response{Header: http.Header{
		"Set-Cookie": []string{"__oailb=" + otherValue + "; Path=/; Max-Age=3600"},
	}})
	headers = http.Header{}
	ApplyCodexCookieJarToHeaders(account, "https://chatgpt.com/backend-api/codex/responses", headers)
	if !strings.Contains(decodeOailbPayloadForTest(headers.Get("Cookie")), "unified-2") {
		t.Fatalf("upstream Set-Cookie changed active edge: %q", headers.Get("Cookie"))
	}
	time.Sleep(1100 * time.Millisecond)
	headers = http.Header{}
	ApplyCodexCookieJarToHeaders(account, "https://chatgpt.com/backend-api/codex/responses", headers)
	if !strings.Contains(decodeOailbPayloadForTest(headers.Get("Cookie")), "unified-3") {
		t.Fatalf("rotated edge was not wrapped in __oailb: %q", headers.Get("Cookie"))
	}
}

func TestCodexCookieJarMapsResinResponseToPublicURL(t *testing.T) {
	ResetAllCodexCookieJars()
	t.Cleanup(ResetAllCodexCookieJars)
	setCookieJarTestSetting(true)
	account := &auth.Account{DBID: 107, AccountID: "workspace"}
	SetResinConfig(&ResinConfig{BaseURL: "http://127.0.0.1:2260/test-token", PlatformName: "codex2api"})
	t.Cleanup(func() { SetResinConfig(nil) })

	publicURL := mustURL(t, CodexBaseURL+"/responses")
	resinURL, err := url.Parse(BuildReverseProxyURL(publicURL.String()))
	if err != nil {
		t.Fatalf("parse Resin URL: %v", err)
	}
	UpdateCodexCookieJarFromResponse(account, resinURL, &http.Response{
		Header: http.Header{"Set-Cookie": []string{"session=from-resin; Path=/; Secure; Max-Age=3600"}},
	})
	header := http.Header{}
	ApplyCodexCookieJarToHeaders(account, publicURL.String(), header)
	if got := header.Get("Cookie"); !strings.Contains(got, "session=from-resin") {
		t.Fatalf("public request did not receive Resin response cookie: %q", got)
	}
}

func decodeOailbPayloadForTest(header string) string {
	value := strings.TrimPrefix(strings.TrimSpace(header), "__oailb=")
	parts := strings.Split(value, ".")
	if len(parts) < 2 {
		return ""
	}
	payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
	return string(payload)
}

func setCookieJarTestSetting(enabled bool) {
	s := DefaultRuntimeSettings()
	s.CodexCookieJarEnabled = enabled
	ApplyRuntimeSettings(s)
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse URL %q: %v", raw, err)
	}
	return u
}

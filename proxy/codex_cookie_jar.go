package proxy

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/codex2api/auth"
)

var codexCookieJars sync.Map // map[string]http.CookieJar

func codexCookieJarKey(account *auth.Account) string {
	if account == nil {
		return ""
	}
	// EffectiveAccountID includes a Chatgpt-Account-Id override. Keeping it in
	// the key prevents cookies from one upstream workspace being reused after
	// an account is pointed at another workspace.
	return fmt.Sprintf("%d|%s", account.ID(), account.EffectiveAccountID())
}

// CodexCookieJarForAccount returns the persistent cookie jar for an official
// Codex account when the global switch is enabled. Relay-style providers keep
// their existing authentication behavior. Jars are isolated by database
// account and effective upstream workspace. The standard net/http cookiejar
// applies Expires and Max-Age, including deletion cookies, when SetCookies is
// called.
func CodexCookieJarForAccount(account *auth.Account) http.CookieJar {
	if account == nil || account.IsRelayStyle() || !CurrentRuntimeSettings().CodexCookieJarEnabled {
		return nil
	}
	key := codexCookieJarKey(account)
	if value, ok := codexCookieJars.Load(key); ok {
		return value.(http.CookieJar)
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil
	}
	actual, _ := codexCookieJars.LoadOrStore(key, http.CookieJar(jar))
	return actual.(http.CookieJar)
}

// ResetCodexCookieJarForAccount discards all cookies currently associated with
// an account. It is used when credentials or workspace identity are replaced,
// and is also useful for tests. A subsequent request creates a fresh jar.
func ResetCodexCookieJarForAccount(account *auth.Account) {
	if account == nil {
		return
	}
	// The effective workspace can change while the database account stays the
	// same. Remove every generation for that DB account so an old workspace's
	// cookies cannot remain reachable in memory.
	prefix := fmt.Sprintf("%d|", account.ID())
	codexCookieJars.Range(func(key, _ any) bool {
		if strings.HasPrefix(key.(string), prefix) {
			codexCookieJars.Delete(key)
		}
		return true
	})
	ResetCodexEdgeStateForAccount(account)
}

// ResetAllCodexCookieJars drops every in-memory jar. Cookie jars are process
// memory only; this helper makes that lifecycle explicit and keeps tests
// independent.
func ResetAllCodexCookieJars() {
	codexCookieJars.Range(func(key, _ any) bool {
		codexCookieJars.Delete(key)
		return true
	})
	ResetAllCodexEdgeStates()
}

// codexCookieURL maps a WebSocket URL to the equivalent HTTP URL. The
// standard cookiejar treats Secure cookies as eligible for https, while a
// wss URL otherwise looks like an unknown scheme. HTTP callers are returned
// unchanged.
func codexCookieURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(u.Scheme) {
	case "ws":
		u.Scheme = "http"
	case "wss":
		u.Scheme = "https"
	}
	if u.Host == "" {
		return nil, fmt.Errorf("cookie URL has no host")
	}
	return u, nil
}

func codexCookieURLFromParsed(input *url.URL) *url.URL {
	if input == nil {
		return nil
	}
	u := *input
	switch strings.ToLower(u.Scheme) {
	case "ws":
		u.Scheme = "http"
	case "wss":
		u.Scheme = "https"
	}
	return &u
}

// ApplyCodexCookieJarToHeaders adds eligible jar cookies to a WebSocket
// handshake. Gorilla's Dialer consults its jar using the original wss URL;
// doing this explicitly with https preserves Secure cookies as well. Existing
// cookie names supplied by the caller win, so custom authentication headers
// are not silently overwritten.
func ApplyCodexCookieJarToHeaders(account *auth.Account, rawURL string, headers http.Header) {
	if headers == nil {
		return
	}
	jar := CodexCookieJarForAccount(account)
	if jar == nil {
		return
	}
	u, err := codexCookieURL(rawURL)
	if err != nil {
		return
	}
	cookies := jar.Cookies(u)
	if len(cookies) == 0 {
		return
	}
	seen := make(map[string]struct{})
	parts := make([]string, 0, len(cookies))
	for _, value := range headers.Values("Cookie") {
		for _, part := range strings.Split(value, ";") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			name := part
			if index := strings.IndexByte(part, '='); index >= 0 {
				name = strings.TrimSpace(part[:index])
			}
			if name != "" {
				seen[name] = struct{}{}
			}
			parts = append(parts, part)
		}
	}
	for _, cookie := range cookies {
		if cookie == nil || cookie.Name == "" {
			continue
		}
		if _, exists := seen[cookie.Name]; exists {
			continue
		}
		parts = append(parts, cookie.Name+"="+cookie.Value)
		seen[cookie.Name] = struct{}{}
	}
	if len(parts) > 0 {
		headers.Set("Cookie", strings.Join(parts, "; "))
	}
}

// UpdateCodexCookieJarFromResponse explicitly stores handshake cookies. The
// HTTP client already does this automatically, but keeping the operation here
// makes WebSocket handshakes and any custom RoundTripper follow the same
// expiry/update rules.
func UpdateCodexCookieJarFromResponse(account *auth.Account, responseURL *url.URL, response *http.Response) {
	if response == nil {
		return
	}
	// The edge JWT is useful even when the Cookie Jar switch is disabled; the
	// rotation switch itself decides whether it may affect routing.
	if cookies := response.Cookies(); len(cookies) > 0 {
		for _, cookie := range cookies {
			if cookie != nil && cookie.Name == "__oailb" {
				observeCodexEdgeCookie(account, cookie, time.Now())
			}
		}
	}
	jar := CodexCookieJarForAccount(account)
	if jar == nil {
		return
	}
	u := codexCookieURLFromParsed(responseURL)
	if u == nil && response.Request != nil {
		u = codexCookieURLFromParsed(response.Request.URL)
	}
	if u == nil {
		return
	}
	if cookies := response.Cookies(); len(cookies) > 0 {
		jar.SetCookies(u, cookies)
		// __oailb is host scoped by upstream. Also index it under the selected
		// edge host so the next request can send it after routing changes.
		for _, cookie := range cookies {
			if cookie == nil || cookie.Name != "__oailb" {
				continue
			}
			if edge := codexEdgeDomainIndexFromCookie(cookie, CurrentRuntimeSettings().CodexEdgeRotationMax); edge != "" {
				edgeURL := *u
				edgeURL.Host = edge
				jar.SetCookies(&edgeURL, []*http.Cookie{cookie})
			}
		}
	}
}

func codexEdgeDomainIndexFromCookie(cookie *http.Cookie, max int) string {
	if cookie == nil {
		return ""
	}
	parts := strings.Split(cookie.Value, ".")
	if len(parts) < 2 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		payload, err = base64.URLEncoding.DecodeString(parts[1])
	}
	if err != nil {
		return ""
	}
	var data struct {
		Host string `json:"host"`
	}
	if json.Unmarshal(payload, &data) != nil {
		return ""
	}
	if codexEdgeDomainIndex(data.Host, max) == 0 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(data.Host))
}

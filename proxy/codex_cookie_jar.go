package proxy

import (
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"

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

// ApplyCodexCookieJarToHeaders adds eligible jar cookies to an outgoing
// request. Gorilla's Dialer consults its jar using the original wss URL;
// doing this explicitly with https preserves Secure cookies as well. Existing
// cookie names supplied by the caller win, so custom authentication headers
// are not silently overwritten. When edge rotation is active, __oailb is
// rewritten in place while the request URL remains the public chatgpt.com URL.
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
	if len(cookies) == 0 && len(headers.Values("Cookie")) == 0 {
		return
	}
	seen := make(map[string]struct{})
	parts := make([]string, 0, len(cookies))
	edgeDomain := codexCurrentEdgeDomain(account)
	for _, value := range headers.Values("Cookie") {
		for _, part := range strings.Split(value, ";") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			name := part
			cookieValue := ""
			if index := strings.IndexByte(part, '='); index >= 0 {
				name = strings.TrimSpace(part[:index])
				cookieValue = part[index+1:]
			}
			if name == "__oailb" && edgeDomain != "" {
				if rewritten := rewriteCodexEdgeCookieValue(cookieValue, edgeDomain); rewritten != cookieValue {
					part = name + "=" + rewritten
				}
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
		value := cookie.Value
		if cookie.Name == "__oailb" && edgeDomain != "" {
			value = rewriteCodexEdgeCookieValue(value, edgeDomain)
		}
		parts = append(parts, cookie.Name+"="+value)
		seen[cookie.Name] = struct{}{}
	}
	if len(parts) > 0 {
		headers.Set("Cookie", strings.Join(parts, "; "))
	}
}

// UpdateCodexCookieJarFromResponse explicitly stores handshake cookies. The
// HTTP client already does this automatically, but keeping the operation here
// makes WebSocket handshakes and any custom RoundTripper follow the same
// expiry/update rules. It never changes the active edge rotation state;
// rotation is applied by ApplyCodexCookieJarToHeaders immediately before send.
func UpdateCodexCookieJarFromResponse(account *auth.Account, responseURL *url.URL, response *http.Response) {
	if response == nil {
		return
	}
	jar := CodexCookieJarForAccount(account)
	if jar == nil {
		return
	}
	u := codexCookieResponseURL(account, responseURL)
	if u == nil && response.Request != nil {
		u = codexCookieResponseURL(account, response.Request.URL)
	}
	if u == nil {
		return
	}
	if cookies := response.Cookies(); len(cookies) > 0 {
		jar.SetCookies(u, cookies)
	}
}

// codexCookieResponseURL maps a Resin reverse-proxy response back to the
// public Codex URL before storing Set-Cookie values. Otherwise a cookie
// received from a Resin host would be scoped to that internal host and would
// never be returned for the next chatgpt.com request.
func codexCookieResponseURL(account *auth.Account, responseURL *url.URL) *url.URL {
	u := codexCookieURLFromParsed(responseURL)
	if u == nil || account == nil || !resinCarriesEgress(account) {
		return u
	}
	cfg := GetResinConfig()
	if cfg == nil {
		return u
	}
	base, err := url.Parse(cfg.BaseURL)
	if err != nil || base.Host == "" {
		return u
	}
	basePath := strings.TrimRight(base.Path, "/")
	prefix := basePath + "/" + strings.Trim(cfg.PlatformName, "/") + "/"
	if !strings.HasPrefix(u.Path, prefix) {
		return u
	}
	encoded := strings.TrimPrefix(u.Path, prefix)
	parts := strings.SplitN(encoded, "/", 3)
	if len(parts) < 2 || !strings.EqualFold(parts[0], "https") || !strings.EqualFold(parts[1], "chatgpt.com") {
		return u
	}
	public, err := url.Parse(CodexBaseURL)
	if err != nil {
		return u
	}
	if len(parts) == 3 && parts[2] != "" {
		public.Path = "/" + parts[2]
	}
	public.RawQuery = u.RawQuery
	return public
}

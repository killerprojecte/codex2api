package proxy

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/codex2api/auth"
)

// CodexEdgeConfig is persisted as one JSON value in system_settings so the
// web settings page can atomically control the Cookie Jar and edge rotation.
type CodexEdgeConfig struct {
	CookieJarEnabled        bool `json:"cookie_jar_enabled"`
	EdgeRotationEnabled     bool `json:"edge_rotation_enabled"`
	EdgeRotationIntervalSec int  `json:"edge_rotation_interval_sec"`
	EdgeRotationMax         int  `json:"edge_rotation_max"`
}

func ParseCodexEdgeConfigJSON(raw string) CodexEdgeConfig {
	cfg := CodexEdgeConfig{EdgeRotationIntervalSec: defaultCodexEdgeRotationIntervalSec, EdgeRotationMax: defaultCodexEdgeRotationMax}
	if strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &cfg)
	}
	if cfg.EdgeRotationIntervalSec <= 0 {
		cfg.EdgeRotationIntervalSec = defaultCodexEdgeRotationIntervalSec
	}
	if cfg.EdgeRotationIntervalSec > 86400 {
		cfg.EdgeRotationIntervalSec = 86400
	}
	if cfg.EdgeRotationMax <= 0 {
		cfg.EdgeRotationMax = defaultCodexEdgeRotationMax
	}
	if cfg.EdgeRotationMax > defaultCodexEdgeRotationMax {
		cfg.EdgeRotationMax = defaultCodexEdgeRotationMax
	}
	return cfg
}

func EncodeCodexEdgeConfigJSON(cfg CodexEdgeConfig) string {
	cfg = ParseCodexEdgeConfigJSON(mustJSONCodexEdge(cfg))
	b, _ := json.Marshal(cfg)
	return string(b)
}

func mustJSONCodexEdge(v any) string { b, _ := json.Marshal(v); return string(b) }

var codexUnifiedHostPattern = regexp.MustCompile(`^chat\.gateway\.unified-([0-9]+)\.api\.openai\.com$`)

type codexEdgeState struct {
	mu           sync.Mutex
	domain       string
	index        int
	nextSwitchAt time.Time
}

var codexEdgeStates sync.Map // map[string]*codexEdgeState

func codexEdgeStateForAccount(account *auth.Account) *codexEdgeState {
	key := codexCookieJarKey(account)
	if key == "" {
		return nil
	}
	if v, ok := codexEdgeStates.Load(key); ok {
		return v.(*codexEdgeState)
	}
	v, _ := codexEdgeStates.LoadOrStore(key, &codexEdgeState{})
	return v.(*codexEdgeState)
}

func ResetCodexEdgeStateForAccount(account *auth.Account) {
	if account == nil {
		return
	}
	prefix := fmt.Sprintf("%d|", account.ID())
	codexEdgeStates.Range(func(key, _ any) bool {
		if strings.HasPrefix(key.(string), prefix) {
			codexEdgeStates.Delete(key)
		}
		return true
	})
}

func ResetAllCodexEdgeStates() {
	codexEdgeStates.Range(func(key, _ any) bool { codexEdgeStates.Delete(key); return true })
}

func codexEdgeDomainIndex(domain string, max int) int {
	m := codexUnifiedHostPattern.FindStringSubmatch(strings.ToLower(strings.TrimSpace(domain)))
	if len(m) != 2 {
		return 0
	}
	i, _ := strconv.Atoi(m[1])
	if i < 1 || i > max {
		return 0
	}
	return i
}

func codexEdgeDomain(index int) string {
	return fmt.Sprintf("chat.gateway.unified-%d.api.openai.com", index)
}

func observeCodexEdgeCookie(account *auth.Account, cookie *http.Cookie, now time.Time) {
	if account == nil || cookie == nil || !CurrentRuntimeSettings().CodexEdgeRotationEnabled {
		return
	}
	cfg := CurrentRuntimeSettings()
	parts := strings.Split(cookie.Value, ".")
	if len(parts) < 2 {
		return
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		payload, err = base64.URLEncoding.DecodeString(parts[1])
	}
	if err != nil {
		return
	}
	var data struct {
		Host string `json:"host"`
	}
	if json.Unmarshal(payload, &data) != nil {
		return
	}
	idx := codexEdgeDomainIndex(data.Host, cfg.CodexEdgeRotationMax)
	if idx == 0 {
		return
	}
	state := codexEdgeStateForAccount(account)
	if state == nil {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.index != idx || state.domain == "" {
		state.index, state.domain = idx, codexEdgeDomain(idx)
		state.nextSwitchAt = now.Add(time.Duration(cfg.CodexEdgeRotationIntervalSec) * time.Second)
	}
}

// CodexEdgeStateForAccount returns the currently selected domain and next
// rotation deadline for the account. It is safe for admin polling.
func CodexEdgeStateForAccount(account *auth.Account) (string, time.Time, int) {
	cfg := CurrentRuntimeSettings()
	if account == nil || !cfg.CodexCookieJarEnabled || !cfg.CodexEdgeRotationEnabled {
		return "", time.Time{}, 0
	}
	state := codexEdgeStateForAccount(account)
	if state == nil {
		return "", time.Time{}, 0
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.domain == "" {
		state.nextSwitchAt = time.Now().Add(time.Duration(cfg.CodexEdgeRotationIntervalSec) * time.Second)
		return "chatgpt.com", state.nextSwitchAt, 0
	}
	return state.domain, state.nextSwitchAt, state.index
}

func codexCurrentEdgeDomain(account *auth.Account) string {
	cfg := CurrentRuntimeSettings()
	if account == nil || !cfg.CodexCookieJarEnabled || !cfg.CodexEdgeRotationEnabled {
		return ""
	}
	state := codexEdgeStateForAccount(account)
	if state == nil {
		return ""
	}
	// On process/runtime setting reload, recover the starting node from the
	// account jar before creating a new sequential rotation state.
	state.mu.Lock()
	needsCookieSeed := state.domain == ""
	state.mu.Unlock()
	if needsCookieSeed {
		if jar := CodexCookieJarForAccount(account); jar != nil {
			if u, err := url.Parse("https://chatgpt.com/backend-api/codex/responses"); err == nil {
				for _, cookie := range jar.Cookies(u) {
					if cookie != nil && cookie.Name == "__oailb" {
						observeCodexEdgeCookie(account, cookie, time.Now())
						break
					}
				}
			}
		}
	}
	now := time.Now()
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.domain == "" {
		state.nextSwitchAt = now.Add(time.Duration(cfg.CodexEdgeRotationIntervalSec) * time.Second)
		return ""
	}
	if !state.nextSwitchAt.IsZero() && !now.Before(state.nextSwitchAt) {
		next := state.index + 1
		if next > cfg.CodexEdgeRotationMax {
			next = 1
		}
		state.index, state.domain = next, codexEdgeDomain(next)
		state.nextSwitchAt = now.Add(time.Duration(cfg.CodexEdgeRotationIntervalSec) * time.Second)
	}
	return state.domain
}

// CodexURLForAccount rewrites official Codex URLs to the selected edge while
// leaving test servers, relay URLs, and unrelated OpenAI hosts untouched.
func CodexURLForAccount(account *auth.Account, raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return raw
	}
	domain := codexCurrentEdgeDomain(account)
	if domain == "" {
		return raw
	}
	host := strings.ToLower(u.Hostname())
	if host != "chatgpt.com" && codexEdgeDomainIndex(host, CurrentRuntimeSettings().CodexEdgeRotationMax) == 0 {
		return raw
	}
	u.Host = domain
	if port := u.Port(); port != "" {
		u.Host = domain + ":" + port
	}
	return u.String()
}

func CodexBaseURLForAccount(account *auth.Account) string {
	return CodexURLForAccount(account, CodexBaseURL)
}

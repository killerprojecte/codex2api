package proxy

import (
	"strings"
	"sync"
	"time"

	"github.com/codex2api/auth"
)

// CodexAccountWebsocketSessionTTL is the lifetime of a manually pre-warmed
// account session. The upstream response id and the session id are kept
// together because a previous_response_id is only useful with the account and
// connection identity that produced it.
const CodexAccountWebsocketSessionTTL = time.Hour

type CodexAccountWebsocketSession struct {
	PreviousResponseID string    `json:"previous_id"`
	SessionID          string    `json:"session_id"`
	ExpiresAt          time.Time `json:"expires_at"`
}

var codexAccountWebsocketSessions = struct {
	sync.Mutex
	items   map[int64]CodexAccountWebsocketSession
	enabled map[int64]bool
}{items: make(map[int64]CodexAccountWebsocketSession), enabled: make(map[int64]bool)}

// CodexAccountWebsocketSessionEnabled reports the account's explicit opt-in.
// The default is off, including after a process restart.
func CodexAccountWebsocketSessionEnabled(accountID int64) bool {
	codexAccountWebsocketSessions.Lock()
	defer codexAccountWebsocketSessions.Unlock()
	return codexAccountWebsocketSessions.enabled[accountID]
}

func SetCodexAccountWebsocketSessionEnabled(accountID int64, enabled bool) {
	if accountID <= 0 {
		return
	}
	codexAccountWebsocketSessions.Lock()
	if enabled {
		codexAccountWebsocketSessions.enabled[accountID] = true
	} else {
		delete(codexAccountWebsocketSessions.enabled, accountID)
	}
	codexAccountWebsocketSessions.Unlock()
	if !enabled {
		ResetCodexAccountWebsocketSession(accountID)
	}
}

// GetCodexAccountWebsocketSession returns a copy of the live account session.
// Expiry is checked lazily so the map cannot retain stale response ids.
func GetCodexAccountWebsocketSession(accountID int64) (CodexAccountWebsocketSession, bool) {
	if accountID <= 0 {
		return CodexAccountWebsocketSession{}, false
	}
	now := time.Now()
	codexAccountWebsocketSessions.Lock()
	defer codexAccountWebsocketSessions.Unlock()
	item, ok := codexAccountWebsocketSessions.items[accountID]
	if !ok || item.ExpiresAt.IsZero() || !item.ExpiresAt.After(now) || strings.TrimSpace(item.SessionID) == "" {
		if ok {
			delete(codexAccountWebsocketSessions.items, accountID)
		}
		return CodexAccountWebsocketSession{}, false
	}
	return item, true
}

// SetCodexAccountWebsocketSession publishes a new account-scoped session.
// Callers must provide both ids returned by the same upstream turn.
func SetCodexAccountWebsocketSession(accountID int64, previousResponseID, sessionID string, expiresAt time.Time) bool {
	previousResponseID = strings.TrimSpace(previousResponseID)
	sessionID = strings.TrimSpace(sessionID)
	if accountID <= 0 || previousResponseID == "" || sessionID == "" {
		return false
	}
	if expiresAt.IsZero() {
		expiresAt = time.Now().Add(CodexAccountWebsocketSessionTTL)
	}
	if !expiresAt.After(time.Now()) {
		return false
	}
	codexAccountWebsocketSessions.Lock()
	codexAccountWebsocketSessions.items[accountID] = CodexAccountWebsocketSession{
		PreviousResponseID: previousResponseID,
		SessionID:          sessionID,
		ExpiresAt:          expiresAt,
	}
	codexAccountWebsocketSessions.Unlock()
	return true
}

// ResetCodexAccountWebsocketSession removes the account's cached session.
func ResetCodexAccountWebsocketSession(accountID int64) {
	if accountID <= 0 {
		return
	}
	codexAccountWebsocketSessions.Lock()
	delete(codexAccountWebsocketSessions.items, accountID)
	codexAccountWebsocketSessions.Unlock()
	resetCodexAccountWebsocketConnections(accountID)
}

// ResetCodexWebsocketConnectionsForAccount is installed by main.go after the
// wsrelay package initializes. Keeping this hook in proxy avoids an import
// cycle while allowing the admin refresh action to actually close the old
// account connections before creating a replacement session.
var ResetCodexWebsocketConnectionsForAccount func(accountID int64)

func resetCodexAccountWebsocketConnections(accountID int64) {
	if ResetCodexWebsocketConnectionsForAccount != nil {
		ResetCodexWebsocketConnectionsForAccount(accountID)
	}
}

func accountWebsocketSessionID(account *auth.Account) string {
	if account == nil || !CodexAccountWebsocketSessionEnabled(account.ID()) {
		return ""
	}
	session, ok := GetCodexAccountWebsocketSession(account.ID())
	if !ok {
		return ""
	}
	return session.SessionID
}

package proxy

import (
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// CODEX_LOG_RESPONSE_COOKIES is an opt-in diagnostic switch. When enabled,
// cookies returned by the official Codex upstream are written to the process
// log exactly as received in Set-Cookie. This is intentionally off by default
// because cookie values are credentials.
const CodexLogResponseCookiesEnv = "CODEX_LOG_RESPONSE_COOKIES"

func codexResponseCookieLoggingEnabled() bool {
	value := strings.TrimSpace(os.Getenv(CodexLogResponseCookiesEnv))
	if value == "" {
		return false
	}
	enabled, err := strconv.ParseBool(value)
	return err == nil && enabled
}

// LogCodexResponseCookies logs upstream Set-Cookie values for one official
// Codex account. It is exported so both HTTP responses and WebSocket handshake
// responses use the same opt-in behavior.
func LogCodexResponseCookies(accountID int64, response *http.Response) {
	if response == nil || !codexResponseCookieLoggingEnabled() {
		return
	}
	for _, value := range response.Header.Values("Set-Cookie") {
		log.Printf("[CodexCookies] account=%d status=%d Set-Cookie=%s", accountID, response.StatusCode, value)
	}
}

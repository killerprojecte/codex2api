package proxy

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// CODEX_RESPONSES_DEBUG is an opt-in diagnostic switch for the final
// Responses payloads sent to and received from an upstream. It is disabled by
// default because payloads can contain prompts, tool arguments and model
// output.
const CodexResponsesDebugEnv = "CODEX_RESPONSES_DEBUG"

func codexResponsesDebugEnabled() bool {
	value := strings.TrimSpace(os.Getenv(CodexResponsesDebugEnv))
	if value == "" {
		return false
	}
	enabled, err := strconv.ParseBool(value)
	return err == nil && enabled
}

// LogCodexResponsesPayload writes one complete logical payload. The helper is
// exported so the native Codex WS relay and the OpenAI Responses WS relay use
// the same switch and log format.
func LogCodexResponsesPayload(accountID int64, transport, direction string, payload []byte) {
	if !codexResponsesDebugEnabled() || len(payload) == 0 {
		return
	}
	log.Printf("[CodexResponsesDebug] account=%d transport=%s direction=%s bytes=%d data=%q",
		accountID, strings.TrimSpace(transport), strings.TrimSpace(direction), len(payload), string(payload))
}

// WrapCodexResponsesResponseBody preserves the original stream while logging
// every chunk as it is read. SSE responses are intentionally logged at the
// read boundary because a stream can contain many independent response events.
func WrapCodexResponsesResponseBody(accountID int64, transport string, status int, body io.ReadCloser) io.ReadCloser {
	if body == nil || !codexResponsesDebugEnabled() {
		return body
	}
	return &codexResponsesDebugBody{
		ReadCloser: body,
		accountID:  accountID,
		transport:  transport,
		status:     status,
	}
}

type codexResponsesDebugBody struct {
	io.ReadCloser
	accountID int64
	transport string
	status    int
}

func (b *codexResponsesDebugBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		direction := "receive"
		if b.status > 0 {
			direction = fmt.Sprintf("receive status=%d", b.status)
		}
		LogCodexResponsesPayload(b.accountID, b.transport, direction, p[:n])
	}
	return n, err
}

func isCodexResponsesRequest(req *http.Request) bool {
	if req == nil || req.URL == nil {
		return false
	}
	return strings.Contains(strings.ToLower(req.URL.Path), "/responses")
}

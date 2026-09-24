package proxy

import (
	"bytes"
	"log"
	"net/http"
	"strings"
	"testing"
)

func TestLogCodexResponseCookiesIsOptIn(t *testing.T) {
	previousWriter := log.Writer()
	var output bytes.Buffer
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(previousWriter) })
	response := &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Set-Cookie": []string{"session=secret"}}}

	t.Setenv(CodexLogResponseCookiesEnv, "false")
	LogCodexResponseCookies(11, response)
	if output.Len() != 0 {
		t.Fatalf("cookies logged while disabled: %q", output.String())
	}

	t.Setenv(CodexLogResponseCookiesEnv, "true")
	LogCodexResponseCookies(11, response)
	if !strings.Contains(output.String(), "account=11") || !strings.Contains(output.String(), "session=secret") {
		t.Fatalf("enabled cookie log = %q", output.String())
	}
}

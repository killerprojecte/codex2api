package proxy

import (
	"bytes"
	"io"
	"log"
	"strings"
	"testing"
)

func TestLogCodexResponsesPayloadIsOptIn(t *testing.T) {
	previousOutput := log.Writer()
	defer log.SetOutput(previousOutput)
	var output bytes.Buffer
	log.SetOutput(&output)

	t.Setenv(CodexResponsesDebugEnv, "false")
	LogCodexResponsesPayload(7, "websocket", "send", []byte(`{"type":"response.create"}`))
	if output.Len() != 0 {
		t.Fatalf("debug output emitted while disabled: %q", output.String())
	}

	t.Setenv(CodexResponsesDebugEnv, "true")
	LogCodexResponsesPayload(7, "websocket", "send", []byte(`{"type":"response.create"}`))
	if !strings.Contains(output.String(), "account=7") || !strings.Contains(output.String(), "response.create") {
		t.Fatalf("debug output missing account or payload: %q", output.String())
	}
}

func TestWrapCodexResponsesResponseBodyPreservesData(t *testing.T) {
	t.Setenv(CodexResponsesDebugEnv, "true")
	body := WrapCodexResponsesResponseBody(8, "http", 200, io.NopCloser(strings.NewReader("data: {}\n\n")))
	got, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if string(got) != "data: {}\n\n" {
		t.Fatalf("body = %q", got)
	}
	if err := body.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

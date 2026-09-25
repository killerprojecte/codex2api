package proxy

import (
	"testing"
	"time"
)

func TestCodexAccountWebsocketSessionIsPerAccountAndExpires(t *testing.T) {
	ResetCodexAccountWebsocketSession(901)
	ResetCodexAccountWebsocketSession(902)
	SetCodexAccountWebsocketSessionEnabled(901, false)
	SetCodexAccountWebsocketSessionEnabled(902, false)
	t.Cleanup(func() {
		ResetCodexAccountWebsocketSession(901)
		ResetCodexAccountWebsocketSession(902)
		SetCodexAccountWebsocketSessionEnabled(901, false)
		SetCodexAccountWebsocketSessionEnabled(902, false)
	})

	expires := time.Now().Add(time.Minute)
	if !SetCodexAccountWebsocketSession(901, "resp_a", "session_a", expires) {
		t.Fatal("SetCodexAccountWebsocketSession returned false")
	}
	if !SetCodexAccountWebsocketSession(902, "resp_b", "session_b", expires) {
		t.Fatal("SetCodexAccountWebsocketSession returned false for second account")
	}
	got, ok := GetCodexAccountWebsocketSession(901)
	if !ok || got.PreviousResponseID != "resp_a" || got.SessionID != "session_a" {
		t.Fatalf("account 901 session = %#v, ok=%v", got, ok)
	}
	if other, ok := GetCodexAccountWebsocketSession(902); !ok || other.SessionID != "session_b" {
		t.Fatalf("account 902 session = %#v, ok=%v", other, ok)
	}

	if !SetCodexAccountWebsocketSession(901, "resp_expired", "session_expired", time.Now().Add(-time.Second)) {
		t.Log("expired values are rejected at write time as expected")
	} else {
		t.Fatal("expired session should not be stored")
	}
	ResetCodexAccountWebsocketSession(901)
	if _, ok := GetCodexAccountWebsocketSession(901); ok {
		t.Fatal("ResetCodexAccountWebsocketSession left stale state")
	}
}

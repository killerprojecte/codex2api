package admin

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/codex2api/auth"
	"github.com/codex2api/proxy"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

func TestRefreshCodexWebsocketSessionStoresResponseAndSessionPair(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := auth.NewStore(nil, nil, nil)
	account := &auth.Account{DBID: 701, AccessToken: "access-token", Status: auth.StatusReady}
	store.AddAccount(account)
	handler := &Handler{store: store}

	previousExecutor := proxy.WebsocketExecuteFunc
	previousResetHook := proxy.ResetCodexWebsocketConnectionsForAccount
	t.Cleanup(func() {
		proxy.WebsocketExecuteFunc = previousExecutor
		proxy.ResetCodexWebsocketConnectionsForAccount = previousResetHook
		proxy.ResetCodexAccountWebsocketSession(account.ID())
	})
	proxy.ResetCodexWebsocketConnectionsForAccount = nil
	var gotSessionID string
	proxy.WebsocketExecuteFunc = func(_ context.Context, _ *auth.Account, body []byte, sessionID string, _ string, _ string, _ *proxy.DeviceProfileConfig, _ http.Header, _ string) (*http.Response, error) {
		gotSessionID = sessionID
		if gjson.GetBytes(body, "store").Bool() != true {
			t.Fatalf("refresh payload must request store=true: %s", body)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_refresh_701\"}}\n\n" +
					"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_refresh_701\",\"status\":\"completed\"}}\n\n",
			)),
		}, nil
	}

	router := gin.New()
	router.POST("/api/admin/accounts/:id/codex-websocket-session/refresh", handler.RefreshCodexWebsocketSession)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/admin/accounts/701/codex-websocket-session/refresh", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if gotSessionID == "" {
		t.Fatal("refresh did not send an explicit session id")
	}
	if got := gjson.Get(recorder.Body.String(), "previous_id").String(); got != "resp_refresh_701" {
		t.Fatalf("previous_id = %q", got)
	}
	session, ok := proxy.GetCodexAccountWebsocketSession(account.ID())
	if !ok || session.PreviousResponseID != "resp_refresh_701" || session.SessionID != gotSessionID {
		t.Fatalf("cached session = %#v, ok=%v", session, ok)
	}
}

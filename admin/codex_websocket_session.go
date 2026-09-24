package admin

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/codex2api/proxy"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// RefreshCodexWebsocketSession creates a fresh native Codex WebSocket session
// for one account, sends a small stored response to obtain its response id,
// and keeps the response id/session pair for one hour.
func (h *Handler) RefreshCodexWebsocketSession(c *gin.Context) {
	id, err := strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的账号 ID"})
		return
	}
	if h == nil || h.store == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "账号存储未初始化"})
		return
	}
	account := h.store.FindByID(id)
	if account == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "账号不在运行时池中"})
		return
	}
	if account.IsRelayStyle() || account.IsCodexAgentIdentity() {
		c.JSON(http.StatusBadRequest, gin.H{"error": "只有官方 Codex 账号支持 WebSocket 会话刷新"})
		return
	}
	if proxy.WebsocketExecuteFunc == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Codex WebSocket 执行器未初始化"})
		return
	}

	model, err := h.connectionTestModelForAccount(c.Request.Context(), account, strings.TrimSpace(c.Query("model")))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	payload := buildConnectionTestPayload(h.store, model)
	payload, _ = sjson.SetBytes(payload, "store", true)
	sessionID := proxy.NewUpstreamSessionUUID()

	// Rotating the pair must also discard old pooled connections and response-id
	// bindings, otherwise a later previous_response_id can land on stale state.
	proxy.ResetCodexAccountWebsocketSession(account.ID())
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Minute)
	defer cancel()
	resp, requestErr := proxy.ExecuteRequest(ctx, account, payload, sessionID, h.store.ResolveProxyForAccount(account), "", nil, nil, true)
	if requestErr != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("创建 Codex WebSocket 会话失败: %s", requestErr.Error())})
		return
	}
	if resp == nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "上游未返回响应"})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
		detail := strings.TrimSpace(sanitizeCodexTestText(string(body), codexTestSecrets(account)))
		if detail == "" {
			detail = http.StatusText(resp.StatusCode)
		}
		c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("上游返回 HTTP %d: %s", resp.StatusCode, detail)})
		return
	}

	var previousID string
	var streamErr string
	readErr := proxy.ReadSSEStream(resp.Body, func(data []byte) bool {
		if id := strings.TrimSpace(gjson.GetBytes(data, "response.id").String()); id != "" {
			previousID = id
		}
		if id := strings.TrimSpace(gjson.GetBytes(data, "id").String()); previousID == "" && id != "" && strings.HasPrefix(id, "resp_") {
			previousID = id
		}
		switch gjson.GetBytes(data, "type").String() {
		case "response.failed", "error":
			streamErr = strings.TrimSpace(gjson.GetBytes(data, "error.message").String())
			if streamErr == "" {
				streamErr = strings.TrimSpace(gjson.GetBytes(data, "message").String())
			}
			return false
		case "response.completed":
			status := strings.ToLower(strings.TrimSpace(gjson.GetBytes(data, "response.status").String()))
			if status == "failed" || status == "incomplete" {
				streamErr = "response " + status
				return false
			}
			return false
		default:
			return true
		}
	})
	if readErr != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("读取 Codex WebSocket 会话响应失败: %s", readErr.Error())})
		return
	}
	if streamErr != "" {
		c.JSON(http.StatusBadGateway, gin.H{"error": "Codex 会话初始化失败: " + streamErr})
		return
	}
	if previousID == "" {
		c.JSON(http.StatusBadGateway, gin.H{"error": "Codex 会话响应缺少 previous_id"})
		return
	}

	expiresAt := time.Now().UTC().Add(proxy.CodexAccountWebsocketSessionTTL)
	if !proxy.SetCodexAccountWebsocketSession(account.ID(), previousID, sessionID, expiresAt) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "保存 Codex WebSocket 会话失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"previous_id": previousID,
		"session_id":  sessionID,
		"expires_at":  expiresAt.Format(time.RFC3339),
		"model":       model,
	})
}

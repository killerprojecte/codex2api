package admin

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/codex2api/proxy"
	"github.com/gin-gonic/gin"
)

// RotateCodexEdge immediately advances one official Codex account to the
// next unified node. The following request carries that node in __oailb.
func (h *Handler) RotateCodexEdge(c *gin.Context) {
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
		c.JSON(http.StatusBadRequest, gin.H{"error": "只有官方 Codex 账号支持边缘节点轮换"})
		return
	}
	domain, next, index, err := proxy.RotateCodexEdgeForAccount(account)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"domain":         domain,
		"index":          index,
		"next_switch_at": next.Format("2006-01-02T15:04:05Z07:00"),
	})
}

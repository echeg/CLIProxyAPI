package management

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// GetRoutingActivity reports recent account selections, not current in-flight work.
func (h *Handler) GetRoutingActivity(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}
	h.mu.Lock()
	manager := h.authManager
	h.mu.Unlock()
	if manager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"accounts": manager.RoutingActivity()})
}

package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

type SystemHandler struct {
	restart func()
}

func NewSystemHandler(restart func()) *SystemHandler {
	return &SystemHandler{restart: restart}
}

func (h *SystemHandler) Restart(c *gin.Context) {
	c.JSON(http.StatusAccepted, gin.H{"message": "重启请求已接受"})
	if h.restart != nil {
		go h.restart()
	}
}

package handler

import (
	"encoding/base64"
	"net/http"

	"emby-media-portal/internal/config"

	"github.com/gin-gonic/gin"
)

type AuthHandler struct{}

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func NewAuthHandler() *AuthHandler {
	return &AuthHandler{}
}

func (h *AuthHandler) Login(c *gin.Context) {
	cfg := config.Get()
	if cfg == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "config not loaded"})
		return
	}

	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	if req.Username != cfg.Server.AdminUsername || req.Password != cfg.Server.AdminPassword {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "用户名或密码错误"})
		return
	}

	authHeader := "Basic " + base64.StdEncoding.EncodeToString([]byte(req.Username+":"+req.Password))
	c.JSON(http.StatusOK, gin.H{
		"message":       "登录成功",
		"authorization": authHeader,
		"username":      req.Username,
	})
}

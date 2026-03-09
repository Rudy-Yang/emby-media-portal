package handler

import (
	"net/http"
	"time"

	"emby-media-portal/internal/config"
	"emby-media-portal/internal/session"

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

	if req.Username != cfg.Server.AdminUsername || !config.VerifyAdminPassword(cfg, req.Password) {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "用户名或密码错误"})
		return
	}

	token, expiresAt, err := session.DefaultManager.Create(req.Username)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "创建会话失败"})
		return
	}

	maxAge := int(time.Until(expiresAt).Seconds())
	if maxAge <= 0 {
		maxAge = 24 * 60 * 60
	}
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(session.CookieName, token, maxAge, "/", "", false, true)
	c.JSON(http.StatusOK, gin.H{
		"message":  "登录成功",
		"username": req.Username,
	})
}

func (h *AuthHandler) Logout(c *gin.Context) {
	if token, err := c.Cookie(session.CookieName); err == nil {
		session.DefaultManager.Revoke(token)
	}
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(session.CookieName, "", -1, "/", "", false, true)
	c.JSON(http.StatusOK, gin.H{"message": "已退出登录"})
}

func (h *AuthHandler) Status(c *gin.Context) {
	username, ok := c.Get("admin_username")
	c.JSON(http.StatusOK, gin.H{
		"authenticated": ok,
		"username":      username,
	})
}

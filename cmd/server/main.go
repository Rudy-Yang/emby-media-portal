package main

import (
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"emby-media-portal/api/handler"
	"emby-media-portal/api/middleware"
	"emby-media-portal/internal/auth"
	"emby-media-portal/internal/config"
	"emby-media-portal/internal/database"
	"emby-media-portal/internal/proxy"
	"emby-media-portal/internal/ratelimit"
	"emby-media-portal/internal/stats"
	"emby-media-portal/web/static"

	"github.com/gin-gonic/gin"
)

func main() {
	// Load config
	configPath := "config.yaml"
	if len(os.Args) > 1 {
		configPath = os.Args[1]
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Initialize database
	db, err := database.Init(cfg.Database.Path)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer db.Close()

	// Initialize components
	identifier := auth.NewIdentifier()
	limiterManager := ratelimit.NewManager(
		cfg.RateLimits.DefaultUpload,
		cfg.RateLimits.DefaultDownload,
		cfg.RateLimits.GlobalLimit,
	)
	rulesManager := ratelimit.NewRulesManager(limiterManager)
	statsTracker := stats.NewTracker(10 * time.Second)
	defer statsTracker.Stop()

	// Load existing rules from database
	if err := rulesManager.LoadRulesFromDB(); err != nil {
		log.Printf("Warning: Failed to load rules from database: %v", err)
	}

	// Create proxy
	prxy := proxy.NewProxy(identifier, limiterManager, rulesManager, statsTracker)
	adminPath := config.NormalizeAdminPath(cfg.Server.AdminPath)
	var server *http.Server
	systemHandler := handler.NewSystemHandler(func() {
		log.Println("Restart requested from admin panel")
		time.Sleep(300 * time.Millisecond)
		if server != nil {
			_ = server.Close()
		}
	})

	// Setup Gin router
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(gin.Logger())
	router.Use(middleware.CORS())

	// API routes
	authHandler := handler.NewAuthHandler()
	router.POST("/api/auth/login", authHandler.Login)
	router.POST("/api/auth/logout", middleware.OptionalAuth(), authHandler.Logout)
	router.GET("/api/auth/status", middleware.OptionalAuth(), authHandler.Status)

	api := router.Group("/api")
	api.Use(middleware.AuthRequired())
	{
		// User management
		userHandler := handler.NewUserHandler(identifier, rulesManager, limiterManager)
		api.GET("/users", userHandler.ListUsers)
		api.GET("/users/active", userHandler.ListActiveUsers)
		api.POST("/users/sync", userHandler.SyncUsers)
		api.GET("/users/:id", userHandler.GetUserRule)
		api.PUT("/users/:id", userHandler.UpdateUserRule)
		api.DELETE("/users/:id", userHandler.DeleteUserRule)
		api.GET("/stats", userHandler.GetServerStats)

		// Rules management
		rulesHandler := handler.NewRulesHandler(rulesManager, limiterManager)
		api.GET("/rules/defaults", rulesHandler.GetDefaultLimits)
		api.PUT("/rules/defaults", rulesHandler.UpdateDefaultLimits)
		api.GET("/rules/servers", rulesHandler.ListServers)
		api.POST("/rules/servers", rulesHandler.CreateServer)
		api.GET("/rules/servers/:id", rulesHandler.GetServerRule)
		api.DELETE("/rules/servers/:id", rulesHandler.DeleteServer)

		clientHandler := handler.NewClientHandler(rulesManager, limiterManager)
		api.GET("/clients", clientHandler.ListClients)
		api.GET("/clients/discovered", clientHandler.ListDiscoveredClients)
		api.POST("/clients", clientHandler.SaveClientRule)
		api.GET("/clients/:id", clientHandler.GetClientRule)
		api.PUT("/clients/:id", clientHandler.SaveClientRule)
		api.DELETE("/clients/:id", clientHandler.DeleteClientRule)

		// Stats
		statsHandler := handler.NewStatsHandler()
		api.GET("/traffic/summary", statsHandler.GetTrafficSummary)
		api.GET("/traffic/users", statsHandler.GetAllStats)
		api.GET("/traffic/users/:id", statsHandler.GetUserStats)
		api.GET("/traffic/clients", statsHandler.GetAllClientStats)
		api.GET("/traffic/clients/:id", statsHandler.GetClientStats)
		api.GET("/traffic/records", statsHandler.ListTrafficEntries)
		api.DELETE("/traffic/records", statsHandler.DeleteTrafficEntries)
		api.DELETE("/traffic/records/:id", statsHandler.DeleteTrafficEntry)
		api.DELETE("/traffic/reset", statsHandler.ResetTrafficStats)
		api.GET("/traffic/servers/:id", statsHandler.GetServerStats)
		api.DELETE("/traffic/clean", statsHandler.CleanStats)
		api.POST("/system/restart", systemHandler.Restart)
	}

	// Static files for admin panel
	router.GET(adminPath, func(c *gin.Context) {
		c.Redirect(302, adminPath+"/")
	})
	router.GET("/favicon.ico", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	router.GET(adminPath+"/*filepath", func(c *gin.Context) {
		filepath := c.Param("filepath")
		if filepath == "/" || filepath == "" {
			filepath = "/index.html"
		}
		// Remove leading slash
		filepath = filepath[1:]

		data, err := static.Files.ReadFile(filepath)
		if err != nil {
			c.String(http.StatusNotFound, "File not found")
			return
		}

		// Set content type based on file extension
		contentType := "text/html"
		if len(filepath) > 4 {
			ext := filepath[len(filepath)-4:]
			switch ext {
			case ".css":
				contentType = "text/css"
			case ".js":
				contentType = "application/javascript"
			}
		} else if len(filepath) > 5 && filepath[len(filepath)-5:] == ".html" {
			contentType = "text/html"
		}

		c.Data(http.StatusOK, contentType, data)
	})

	// Proxy all other requests
	router.NoRoute(func(c *gin.Context) {
		prxy.ServeHTTP(c.Writer, c.Request)
	})

	// Start server
	addr := cfg.Server.Listen
	log.Printf("Starting emby-media-portal server on %s", addr)
	log.Printf("Admin panel available at http://localhost%s%s/", addr, adminPath)

	server = &http.Server{
		Addr:    addr,
		Handler: router,
	}

	// Graceful shutdown
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		log.Println("Shutting down...")
		server.Close()
	}()

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("Server error: %v", err)
	}
}

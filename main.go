package main

import (
	"log"
	"net/http"
	"os"
	"strings"
	"truerp/controllers"
	"truerp/routes"
	"truerp/services"
	"truerp/transport"
	"truerp/utils"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
)

func main() {
	if secret := strings.TrimSpace(os.Getenv("JWT_SECRET")); secret != "" {
		utils.SetJWTSecret(secret)
	} else {
		log.Println("WARNING: JWT_SECRET is not set; using insecure default. Set JWT_SECRET in production.")
	}

	utils.InitDatabase()
	controllers.EnsureDefaultSuperAdmin()
	controllers.EnsureStoresMigrated()
	_ = services.GetDefaultStorageService()

	gin.SetMode(gin.ReleaseMode)
	r := gin.Default()

	config := cors.DefaultConfig()
	// Desktop Tauri WebView (local UI) and any web clients call this cloud API cross-origin.
	config.AllowOrigins = []string{"*"}
	config.AllowHeaders = []string{"Origin", "Content-Type", "Accept", "Authorization", "X-Store-ID"}
	config.ExposeHeaders = []string{"X-Active-Store-ID"}
	r.Use(cors.New(config))

	storageConfig := services.GetStorageConfig()
	if storageConfig.Type == services.StorageTypeLocal {
		r.Static("/uploads", storageConfig.LocalPath)
	}

	routes.SetupRoutes(r)

	log.Printf("Storage type: %s", storageConfig.Type)

	mode := transport.Mode()
	switch mode {
	case transport.ModeREST:
		addr := transport.RESTAddr()
		log.Printf("Server starting (REST) on %s", addr)
		if err := r.Run(addr); err != nil {
			log.Fatal("Failed to start REST server:", err)
		}
	case transport.ModeIPC:
		socketPath := transport.SocketPath()
		ln, err := transport.ListenIPC(socketPath)
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("Server starting (IPC) on unix:%s", socketPath)
		server := &http.Server{Handler: r}
		if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Fatal("Failed to start IPC server:", err)
		}
	default:
		log.Fatalf("Unknown API_TRANSPORT %q (use %q or %q)", mode, transport.ModeIPC, transport.ModeREST)
	}
}

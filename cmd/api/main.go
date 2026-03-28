package main

import (
	"context"
	"embed"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/datumbridge/mcp-ws-hub/internal/hub"
	"github.com/gorilla/mux"
	"github.com/joho/godotenv"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

//go:embed web
var webFS embed.FS

func init() {
	_ = godotenv.Load()
	logLevel := strings.ToUpper(os.Getenv("LOG_LEVEL"))
	switch logLevel {
	case "DEBUG":
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	case "INFO":
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	case "WARN":
		zerolog.SetGlobalLevel(zerolog.WarnLevel)
	case "ERROR":
		zerolog.SetGlobalLevel(zerolog.ErrorLevel)
	default:
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	}
	zerolog.TimeFieldFormat = time.RFC3339
	log.Logger = zerolog.New(os.Stdout).With().Timestamp().Caller().Logger()
}

func main() {
	h := hub.New()

	webRoot, _ := fs.Sub(webFS, "web")

	r := mux.NewRouter()
	r.HandleFunc("/health", hub.HandleHealth).Methods("GET")
	r.HandleFunc("/mcp", h.HandleMCPStreamableHTTP).Methods("POST", "OPTIONS")
	r.HandleFunc("/mcp/", h.HandleMCPStreamableHTTP).Methods("POST", "OPTIONS")
	r.HandleFunc("/ws", h.HandleWS)
	r.HandleFunc("/api/v1/devices", h.HandleListDevices).Methods("GET")
	r.HandleFunc("/api/v1/devices/register", h.HandleRegisterDevice).Methods("POST")
	r.HandleFunc("/api/v1/devices/register/confirm", h.HandleConfirmPairing).Methods("POST")
	r.HandleFunc("/api/v1/devices/{device_id}/mcp", h.HandleDeviceMCP).Methods("POST")
	r.HandleFunc("/api/v1/devices/{device_id}", h.HandleRevokeDevice).Methods("DELETE")
	r.HandleFunc("/api/v1/pairing/pending", h.HandleListPendingPairings).Methods("GET")
	r.PathPrefix("/").Handler(http.FileServer(http.FS(webRoot)))

	// Apply middleware: CORS (outermost) -> Logging -> optional /api/ws-hub strip -> Router
	handler := hub.CORSMiddleware(hub.LoggingMiddleware(hub.OptionalStudioProxyStripMiddleware(r)))

	port := os.Getenv("HUB_PORT")
	if port == "" {
		port = "8000"
	}
	addr := ":" + port

	srv := &http.Server{
		Addr:         addr,
		Handler:      handler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 120 * time.Second, // long for proxied MCP requests
		IdleTimeout:  120 * time.Second,
	}

	// Graceful shutdown on SIGINT / SIGTERM
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		log.Info().Str("addr", addr).Msg("MCP WebSocket hub listening")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal().Err(err).Msg("server error")
		}
	}()

	regBase := strings.TrimSpace(os.Getenv("TOOL_REGISTRY_BASE_URL"))
	if regBase != "" {
		regKey := strings.TrimSpace(os.Getenv("TOOL_REGISTRY_API_KEY"))
		mcpName := strings.TrimSpace(os.Getenv("WS_HUB_REGISTRY_MCPSERVER_NAME"))
		if mcpName == "" {
			mcpName = hub.DefaultRegistryMcpServerID
		}
		go func() {
			ctx := context.Background()
			n, err := hub.SyncEdgeCatalogToRegistry(ctx, regBase, regKey, mcpName)
			if err != nil {
				log.Error().Err(err).Str("registry", regBase).Msg("tool registry sync failed (catalog from this repo)")
				return
			}
			log.Info().Int("tools_synced", n).Str("registry", regBase).Str("mcpServer", mcpName).Msg("tool registry sync completed")
		}()
	}

	<-quit
	log.Info().Msg("shutting down gracefully…")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Error().Err(err).Msg("forced shutdown")
	}
	log.Info().Msg("server stopped")
}

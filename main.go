package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	_ "time/tzdata"
)

//go:embed web/index.html VERSION
var webFS embed.FS
var appVersion = func() string { data, _ := webFS.ReadFile("VERSION"); return strings.TrimSpace(string(data)) }()

var errBusy = errors.New("another scan is running")

type App struct {
	logPath, mapPath, statePath, eventsPath, storePath string
	logger                                             *log.Logger
	mu                                                 sync.RWMutex
	store                                              graveStore
	scanMu                                             sync.Mutex
	jobMu                                              sync.Mutex
	job                                                *scanJob
	cancel                                             context.CancelFunc
	logLocation                                        *time.Location
}

type config struct {
	addr, logPath, mapPath, statePath, eventsPath string
	location                                      *time.Location
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
func loadConfig() (config, error) {
	dir := envOrDefault("DATA_DIR", "./data")
	c := config{addr: envOrDefault("HTTP_ADDR", ":8080"), logPath: os.Getenv("LOG_FILE_PATH"), mapPath: os.Getenv("MAP_FILE_PATH"), statePath: filepath.Join(dir, "scanner-state.json"), eventsPath: filepath.Join(dir, "deaths.json"), location: time.Local}
	if c.logPath == "" {
		return c, errors.New("LOG_FILE_PATH is required")
	}
	if zone := os.Getenv("LOG_TIMEZONE"); zone != "" {
		var err error
		c.location, err = time.LoadLocation(zone)
		if err != nil {
			return c, fmt.Errorf("LOG_TIMEZONE: %w", err)
		}
	}
	return c, nil
}

func newApp(logPath, statePath, eventsPath string, logger *log.Logger) (*App, error) {
	path := filepath.Join(filepath.Dir(eventsPath), "grave-store-v3.json")
	s, err := loadStore(path, eventsPath)
	if err != nil {
		return nil, err
	}
	return &App{logPath: logPath, statePath: statePath, eventsPath: eventsPath, storePath: path, store: s, logger: logger, logLocation: time.Local}, nil
}
func (a *App) snapshot() graveStore { a.mu.RLock(); defer a.mu.RUnlock(); return cloneStore(a.store) }
func (a *App) commit(s graveStore) error {
	if err := persistStore(a.storePath, s); err != nil {
		return err
	}
	a.mu.Lock()
	a.store = s
	a.mu.Unlock()
	return nil
}

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/deaths", a.handleDeaths)
	mux.HandleFunc("GET /api/status", a.handleStatus)
	mux.HandleFunc("POST /api/refresh/incremental", a.handleRefreshIncremental)
	mux.HandleFunc("POST /api/refresh/full", a.handleRefreshFull)
	mux.HandleFunc("POST /api/map/verify", func(w http.ResponseWriter, r *http.Request) { a.handleMapStart(w, r, "verify") })
	mux.HandleFunc("POST /api/map/full", func(w http.ResponseWriter, r *http.Request) { a.handleMapStart(w, r, "full") })
	mux.HandleFunc("POST /api/scan/cancel", a.handleCancel)
	mux.HandleFunc("GET /api/version", a.handleVersion)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("GET /", a.handleIndex)
	return mux
}

func main() {
	logger := log.New(os.Stdout, "", log.LstdFlags)
	cfg, err := loadConfig()
	if err != nil {
		logger.Fatal(err)
	}
	app, err := newApp(cfg.logPath, cfg.statePath, cfg.eventsPath, logger)
	if err != nil {
		logger.Fatal(err)
	}
	app.mapPath = cfg.mapPath
	app.logLocation = cfg.location
	server := &http.Server{Addr: cfg.addr, Handler: app.routes(), ReadHeaderTimeout: 10 * time.Second}
	logger.Printf("Luanti Grave Scanner %s listening at %s", appVersion, cfg.addr)
	logger.Fatal(server.ListenAndServe())
}

func writeJSON(w http.ResponseWriter, code int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	if errors.Is(err, errBusy) {
		code = http.StatusConflict
	}
	writeJSON(w, code, map[string]string{"error": err.Error()})
}
func (a *App) handleDeaths(w http.ResponseWriter, r *http.Request) {
	events := a.snapshot().Graves
	sort.SliceStable(events, func(i, j int) bool { return events[i].Timestamp.After(events[j].Timestamp) })
	writeJSON(w, http.StatusOK, events)
}
func (a *App) handleRefreshIncremental(w http.ResponseWriter, r *http.Request) {
	resp, err := a.refreshLogs(r.Context(), false)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}
func (a *App) handleRefreshFull(w http.ResponseWriter, r *http.Request) {
	resp, err := a.refreshLogs(r.Context(), true)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}
func (a *App) refreshIncremental() (refreshResponse, error) {
	return a.refreshLogs(context.Background(), false)
}
func (a *App) refreshFull() (refreshResponse, error) {
	return a.refreshLogs(context.Background(), true)
}
func (a *App) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"version": appVersion})
}
func (a *App) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, err := webFS.ReadFile("web/index.html")
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(data)
}

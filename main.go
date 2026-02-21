package main

import (
	"bufio"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultAddr = ":8080"
	appVersion  = "v0.2"
)

var deathLinePattern = regexp.MustCompile(`^([0-9]{4}-[0-9]{2}-[0-9]{2} [0-9]{2}:[0-9]{2}:[0-9]{2}): ACTION\[Server\]: ([^ ]+) dies at \((-?[0-9]+),(-?[0-9]+),(-?[0-9]+)\)\. Bones placed$`)

//go:embed web/index.html
var webFS embed.FS

type DeathEvent struct {
	Timestamp  time.Time `json:"timestamp"`
	Player     string    `json:"player"`
	X          int       `json:"x"`
	Y          int       `json:"y"`
	Z          int       `json:"z"`
	RawLine    string    `json:"raw_line"`
	Discovered time.Time `json:"discovered_at"`
}

type scannerState struct {
	Offset int64 `json:"offset"`
}

type refreshResponse struct {
	Mode  string `json:"mode"`
	Added int    `json:"added"`
	Total int    `json:"total"`
}

type App struct {
	logPath    string
	statePath  string
	eventsPath string
	stateMu    sync.Mutex
	eventsMu   sync.RWMutex
	scanMu     sync.Mutex
	state      scannerState
	events     []DeathEvent
	logger     *log.Logger
}

func main() {
	logger := log.New(os.Stdout, "", log.LstdFlags)

	cfg, err := loadConfig()
	if err != nil {
		logger.Fatalf("invalid configuration: %v", err)
	}

	app, err := newApp(cfg.logPath, cfg.statePath, cfg.eventsPath, logger)
	if err != nil {
		logger.Fatalf("cannot initialize app: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/deaths", app.handleDeaths)
	mux.HandleFunc("POST /api/refresh/incremental", app.handleRefreshIncremental)
	mux.HandleFunc("POST /api/refresh/full", app.handleRefreshFull)
	mux.HandleFunc("GET /api/version", app.handleVersion)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /", app.handleIndex)

	logger.Printf("starting server at %s", cfg.addr)
	if err := http.ListenAndServe(cfg.addr, mux); err != nil {
		logger.Fatalf("http server failed: %v", err)
	}
}

type config struct {
	addr       string
	logPath    string
	statePath  string
	eventsPath string
}

func loadConfig() (config, error) {
	dataDir := envOrDefault("DATA_DIR", "./data")
	logPath := os.Getenv("LOG_FILE_PATH")
	if logPath == "" {
		return config{}, errors.New("LOG_FILE_PATH is required")
	}

	return config{
		addr:       envOrDefault("HTTP_ADDR", defaultAddr),
		logPath:    logPath,
		statePath:  filepath.Join(dataDir, "scanner-state.json"),
		eventsPath: filepath.Join(dataDir, "deaths.json"),
	}, nil
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func newApp(logPath, statePath, eventsPath string, logger *log.Logger) (*App, error) {
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		return nil, fmt.Errorf("cannot create state directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(eventsPath), 0o755); err != nil {
		return nil, fmt.Errorf("cannot create events directory: %w", err)
	}

	state, err := loadState(statePath)
	if err != nil {
		return nil, fmt.Errorf("load state failed: %w", err)
	}
	events, err := loadEvents(eventsPath)
	if err != nil {
		return nil, fmt.Errorf("load events failed: %w", err)
	}

	return &App{
		logPath:    logPath,
		statePath:  statePath,
		eventsPath: eventsPath,
		state:      state,
		events:     events,
		logger:     logger,
	}, nil
}

func loadState(path string) (scannerState, error) {
	buf, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return scannerState{}, nil
		}
		return scannerState{}, err
	}

	var state scannerState
	if err := json.Unmarshal(buf, &state); err != nil {
		return scannerState{}, err
	}
	if state.Offset < 0 {
		state.Offset = 0
	}
	return state, nil
}

func loadEvents(path string) ([]DeathEvent, error) {
	buf, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []DeathEvent{}, nil
		}
		return nil, err
	}
	if strings.TrimSpace(string(buf)) == "" {
		return []DeathEvent{}, nil
	}
	var events []DeathEvent
	if err := json.Unmarshal(buf, &events); err != nil {
		return nil, err
	}
	sort.Slice(events, func(i, j int) bool {
		return events[i].Timestamp.Before(events[j].Timestamp)
	})
	return events, nil
}

func (a *App) refreshIncremental() (refreshResponse, error) {
	a.scanMu.Lock()
	defer a.scanMu.Unlock()

	file, err := os.Open(a.logPath)
	if err != nil {
		return refreshResponse{}, fmt.Errorf("cannot open log file: %w", err)
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return refreshResponse{}, fmt.Errorf("cannot stat log file: %w", err)
	}

	a.stateMu.Lock()
	offset := a.state.Offset
	if stat.Size() < offset {
		a.logger.Printf("log truncation detected (size=%d < offset=%d), resetting offset to 0", stat.Size(), offset)
		offset = 0
	}
	a.stateMu.Unlock()

	found, newOffset, err := scanFromOffset(file, offset)
	if err != nil {
		return refreshResponse{}, err
	}

	a.stateMu.Lock()
	a.state.Offset = newOffset
	stateSnapshot := a.state
	a.stateMu.Unlock()

	if err := persistState(a.statePath, stateSnapshot); err != nil {
		return refreshResponse{}, fmt.Errorf("persist state failed: %w", err)
	}

	total, added, err := a.appendEvents(found)
	if err != nil {
		return refreshResponse{}, err
	}

	return refreshResponse{Mode: "incremental", Added: added, Total: total}, nil
}

func (a *App) refreshFull() (refreshResponse, error) {
	a.scanMu.Lock()
	defer a.scanMu.Unlock()

	file, err := os.Open(a.logPath)
	if err != nil {
		return refreshResponse{}, fmt.Errorf("cannot open log file: %w", err)
	}
	defer file.Close()

	found, newOffset, err := scanFromOffset(file, 0)
	if err != nil {
		return refreshResponse{}, err
	}

	a.stateMu.Lock()
	a.state.Offset = newOffset
	stateSnapshot := a.state
	a.stateMu.Unlock()
	if err := persistState(a.statePath, stateSnapshot); err != nil {
		return refreshResponse{}, fmt.Errorf("persist state failed: %w", err)
	}

	total, err := a.replaceEvents(found)
	if err != nil {
		return refreshResponse{}, err
	}

	return refreshResponse{Mode: "full", Added: total, Total: total}, nil
}

func scanFromOffset(file *os.File, offset int64) ([]DeathEvent, int64, error) {
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return nil, 0, fmt.Errorf("seek failed: %w", err)
	}

	reader := bufio.NewReader(file)
	var found []DeathEvent
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			line = strings.TrimRight(line, "\r\n")
			if event, ok := parseDeathEvent(line); ok {
				found = append(found, event)
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, 0, fmt.Errorf("read log failed: %w", err)
		}
	}

	newOffset, err := file.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, 0, fmt.Errorf("cannot get current offset: %w", err)
	}
	return found, newOffset, nil
}

func (a *App) appendEvents(found []DeathEvent) (total int, added int, err error) {
	if len(found) == 0 {
		a.eventsMu.RLock()
		total = len(a.events)
		a.eventsMu.RUnlock()
		return total, 0, nil
	}

	a.eventsMu.Lock()
	a.events = append(a.events, found...)
	sort.Slice(a.events, func(i, j int) bool {
		return a.events[i].Timestamp.Before(a.events[j].Timestamp)
	})
	snapshot := append([]DeathEvent(nil), a.events...)
	total = len(a.events)
	a.eventsMu.Unlock()

	if err := persistEvents(a.eventsPath, snapshot); err != nil {
		return 0, 0, fmt.Errorf("persist events failed: %w", err)
	}
	return total, len(found), nil
}

func (a *App) replaceEvents(all []DeathEvent) (total int, err error) {
	sort.Slice(all, func(i, j int) bool {
		return all[i].Timestamp.Before(all[j].Timestamp)
	})

	a.eventsMu.Lock()
	a.events = append([]DeathEvent(nil), all...)
	snapshot := append([]DeathEvent(nil), a.events...)
	total = len(a.events)
	a.eventsMu.Unlock()

	if err := persistEvents(a.eventsPath, snapshot); err != nil {
		return 0, fmt.Errorf("persist events failed: %w", err)
	}
	return total, nil
}

func persistState(path string, state scannerState) error {
	buf, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, buf, 0o644)
}

func persistEvents(path string, events []DeathEvent) error {
	buf, err := json.MarshalIndent(events, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, buf, 0o644)
}

func parseDeathEvent(line string) (DeathEvent, bool) {
	match := deathLinePattern.FindStringSubmatch(line)
	if len(match) != 6 {
		return DeathEvent{}, false
	}

	timestamp, err := time.ParseInLocation("2006-01-02 15:04:05", match[1], time.Local)
	if err != nil {
		return DeathEvent{}, false
	}

	x, err := strconv.Atoi(match[3])
	if err != nil {
		return DeathEvent{}, false
	}
	y, err := strconv.Atoi(match[4])
	if err != nil {
		return DeathEvent{}, false
	}
	z, err := strconv.Atoi(match[5])
	if err != nil {
		return DeathEvent{}, false
	}

	return DeathEvent{
		Timestamp:  timestamp,
		Player:     match[2],
		X:          x,
		Y:          y,
		Z:          z,
		RawLine:    line,
		Discovered: time.Now(),
	}, true
}

func (a *App) handleDeaths(w http.ResponseWriter, _ *http.Request) {
	a.eventsMu.RLock()
	resp := append([]DeathEvent(nil), a.events...)
	a.eventsMu.RUnlock()

	sort.Slice(resp, func(i, j int) bool {
		return resp[i].Timestamp.After(resp[j].Timestamp)
	})

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (a *App) handleRefreshIncremental(w http.ResponseWriter, _ *http.Request) {
	resp, err := a.refreshIncremental()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (a *App) handleRefreshFull(w http.ResponseWriter, _ *http.Request) {
	resp, err := a.refreshFull()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (a *App) handleVersion(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"version": appVersion})
}

func (a *App) handleIndex(w http.ResponseWriter, _ *http.Request) {
	buf, err := webFS.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, "cannot load html", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(buf)
}

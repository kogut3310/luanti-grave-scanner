package main

import (
	"bufio"
	"context"
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
	defaultScanInterval = 5 * time.Minute
	defaultAddr         = ":8080"
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

type App struct {
	logPath     string
	statePath   string
	eventsPath  string
	scanEvery   time.Duration
	stateMu     sync.Mutex
	eventsMu    sync.RWMutex
	state       scannerState
	deathEvents []DeathEvent
}

func main() {
	logger := log.New(os.Stdout, "", log.LstdFlags)

	cfg, err := loadConfig()
	if err != nil {
		logger.Fatalf("invalid configuration: %v", err)
	}

	app, err := newApp(cfg.logPath, cfg.statePath, cfg.eventsPath, cfg.scanEvery)
	if err != nil {
		logger.Fatalf("cannot initialize app: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go app.startScanner(ctx, logger)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/deaths", app.handleDeaths)
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
	scanEvery  time.Duration
}

func loadConfig() (config, error) {
	dataDir := envOrDefault("DATA_DIR", "./data")
	logPath := os.Getenv("LOG_FILE_PATH")
	if logPath == "" {
		return config{}, errors.New("LOG_FILE_PATH is required")
	}

	scanEvery := defaultScanInterval
	if intervalRaw := os.Getenv("SCAN_INTERVAL"); intervalRaw != "" {
		parsed, err := time.ParseDuration(intervalRaw)
		if err != nil {
			return config{}, fmt.Errorf("SCAN_INTERVAL parse error: %w", err)
		}
		scanEvery = parsed
	}

	return config{
		addr:       envOrDefault("HTTP_ADDR", defaultAddr),
		logPath:    logPath,
		statePath:  filepath.Join(dataDir, "scanner-state.json"),
		eventsPath: filepath.Join(dataDir, "deaths.json"),
		scanEvery:  scanEvery,
	}, nil
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func newApp(logPath, statePath, eventsPath string, scanEvery time.Duration) (*App, error) {
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
		logPath:     logPath,
		statePath:   statePath,
		eventsPath:  eventsPath,
		scanEvery:   scanEvery,
		state:       state,
		deathEvents: events,
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

func (a *App) startScanner(ctx context.Context, logger *log.Logger) {
	if err := a.scanOnce(logger); err != nil {
		logger.Printf("initial scan failed: %v", err)
	}

	ticker := time.NewTicker(a.scanEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := a.scanOnce(logger); err != nil {
				logger.Printf("scan failed: %v", err)
			}
		}
	}
}

func (a *App) scanOnce(logger *log.Logger) error {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()

	file, err := os.Open(a.logPath)
	if err != nil {
		return fmt.Errorf("cannot open log file: %w", err)
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return fmt.Errorf("cannot stat log file: %w", err)
	}

	if stat.Size() < a.state.Offset {
		logger.Printf("log truncation detected (size=%d < offset=%d), resetting offset to 0", stat.Size(), a.state.Offset)
		a.state.Offset = 0
	}

	if _, err := file.Seek(a.state.Offset, io.SeekStart); err != nil {
		return fmt.Errorf("seek failed: %w", err)
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
			return fmt.Errorf("read log failed: %w", err)
		}
	}

	offset, err := file.Seek(0, io.SeekCurrent)
	if err != nil {
		return fmt.Errorf("cannot get current offset: %w", err)
	}
	a.state.Offset = offset

	if err := persistState(a.statePath, a.state); err != nil {
		return fmt.Errorf("persist state failed: %w", err)
	}

	if len(found) == 0 {
		return nil
	}

	a.eventsMu.Lock()
	a.deathEvents = append(a.deathEvents, found...)
	sort.Slice(a.deathEvents, func(i, j int) bool {
		return a.deathEvents[i].Timestamp.Before(a.deathEvents[j].Timestamp)
	})
	eventsSnapshot := append([]DeathEvent(nil), a.deathEvents...)
	a.eventsMu.Unlock()

	if err := persistEvents(a.eventsPath, eventsSnapshot); err != nil {
		return fmt.Errorf("persist events failed: %w", err)
	}

	logger.Printf("scan finished: found %d new deaths", len(found))
	return nil
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
	resp := append([]DeathEvent(nil), a.deathEvents...)
	a.eventsMu.RUnlock()

	sort.Slice(resp, func(i, j int) bool {
		return resp[i].Timestamp.After(resp[j].Timestamp)
	})

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
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

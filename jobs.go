package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

type scanJob struct {
	ID         string           `json:"id"`
	Mode       string           `json:"mode"`
	State      string           `json:"state"`
	Stage      string           `json:"stage"`
	StartedAt  time.Time        `json:"started_at"`
	FinishedAt *time.Time       `json:"finished_at,omitempty"`
	Blocks     int              `json:"blocks"`
	Found      int              `json:"found"`
	Checked    int              `json:"checked"`
	Error      string           `json:"error,omitempty"`
	Log        *refreshResponse `json:"log,omitempty"`
}

func (a *App) updateJob(update func(*scanJob)) { a.jobMu.Lock(); defer a.jobMu.Unlock(); update(a.job) }
func (a *App) handleMapStart(w http.ResponseWriter, r *http.Request, mode string) {
	if a.mapPath == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "MAP_FILE_PATH is not configured"})
		return
	}
	if !a.scanMu.TryLock() {
		writeError(w, errBusy)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	job := scanJob{ID: newID(), Mode: mode, State: "running", Stage: "logs", StartedAt: time.Now()}
	a.jobMu.Lock()
	a.job = &job
	a.cancel = cancel
	copy := job
	a.jobMu.Unlock()
	go a.runMapJob(ctx, mode)
	writeJSON(w, http.StatusAccepted, copy)
}
func (a *App) runMapJob(ctx context.Context, mode string) {
	defer a.scanMu.Unlock()
	finish := func(err error) {
		now := time.Now()
		a.jobMu.Lock()
		defer a.jobMu.Unlock()
		if a.cancel != nil {
			a.cancel()
			a.cancel = nil
		}
		a.job.FinishedAt = &now
		if err == nil {
			a.job.State = "succeeded"
			a.job.Stage = "done"
		} else if errors.Is(err, context.Canceled) {
			a.job.State = "cancelled"
			a.job.Stage = "done"
		} else {
			a.job.State = "failed"
			a.job.Stage = "done"
			a.job.Error = err.Error()
			a.logger.Printf("map scan failed: %v", err)
		}
	}
	logs, err := a.refreshLogsLocked(ctx, false)
	if err != nil {
		finish(err)
		return
	}
	a.updateJob(func(job *scanJob) { job.Stage = "map"; job.Log = &logs })
	s := a.snapshot()
	summary, err := scanMap(ctx, a.mapPath, mode, &s, func(blocks, found, checked int) {
		a.updateJob(func(job *scanJob) { job.Blocks = blocks; job.Found = found; job.Checked = checked })
	})
	if err != nil {
		finish(err)
		return
	}
	a.jobMu.Lock()
	summary.ID = a.job.ID
	a.jobMu.Unlock()
	s.LastMapScan = &summary
	a.updateJob(func(job *scanJob) {
		job.Stage = "saving"
		job.Blocks = summary.Blocks
		job.Found = summary.Found
		job.Checked = summary.Checked
	})
	if err = ctx.Err(); err != nil {
		finish(err)
		return
	}
	err = a.commit(s)
	finish(err)
}
func (a *App) handleCancel(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "expected scan id"})
		return
	}
	a.jobMu.Lock()
	if a.job == nil || a.job.State != "running" || a.job.ID != request.ID || a.cancel == nil {
		a.jobMu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{"error": "scan is no longer running"})
		return
	}
	cancel := a.cancel
	a.jobMu.Unlock()
	cancel()
	writeJSON(w, http.StatusAccepted, map[string]string{"state": "cancellation_requested"})
}
func (a *App) handleStatus(w http.ResponseWriter, r *http.Request) {
	counts := map[string]int{"confirmed": 0, "unverified": 0, "collected": 0}
	s := a.snapshot()
	for _, g := range s.Graves {
		counts[g.Status]++
	}
	var job *scanJob
	a.jobMu.Lock()
	if a.job != nil {
		copy := *a.job
		job = &copy
	}
	a.jobMu.Unlock()
	busy := true
	if a.scanMu.TryLock() {
		busy = false
		a.scanMu.Unlock()
	}
	writeJSON(w, http.StatusOK, struct {
		Version     string         `json:"version"`
		MapEnabled  bool           `json:"map_enabled"`
		Busy        bool           `json:"busy"`
		Counts      map[string]int `json:"counts"`
		Job         *scanJob       `json:"job"`
		LastMapScan *scanSummary   `json:"last_map_scan"`
	}{appVersion, a.mapPath != "", busy, counts, job, s.LastMapScan})
}

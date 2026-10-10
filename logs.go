package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
)

func (s *server) handleClearLogs(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ID *int `json:"id"`
	}
	if err := decode(w, r, &request); err != nil {
		failure(w, 400, err)
		return
	}
	if request.ID == nil || *request.ID < 0 {
		failure(w, 400, errors.New("a non-negative PM2 process ID is required"))
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	processes, err := s.list(r.Context())
	if err != nil {
		failure(w, 502, err)
		return
	}
	for _, process := range processes {
		if process.ID != *request.ID {
			continue
		}
		managed := false
		for _, project := range s.config.Projects {
			if project.Name == process.Name {
				managed = true
				break
			}
		}
		if !managed {
			failure(w, 403, errors.New("unmanaged process logs are read only"))
			return
		}
		if err := clearProcessLogs(process, processes); err != nil {
			failure(w, 409, err)
			return
		}
		writeJSON(w, 200, map[string]any{"id": process.ID, "message": "Logs cleared. New output may appear immediately."})
		return
	}
	failure(w, 404, errors.New("process not found"))
}

func clearProcessLogs(selected Process, processes []Process) error {
	files := []*os.File{}
	defer func() {
		for _, file := range files {
			file.Close()
		}
	}()
	seen := map[string]bool{}
	for _, path := range []string{selected.Env.OutLog, selected.Env.ErrLog} {
		if path == "" {
			continue
		}
		if !filepath.IsAbs(path) {
			return errors.New("PM2 log paths must be absolute")
		}
		path = filepath.Clean(path)
		if seen[path] {
			continue
		}
		seen[path] = true
		// Never create a replacement file or follow a log symlink. Truncating
		// the existing inode preserves PM2's open log descriptors.
		file, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("cannot open log for clearing: %w", err)
		}
		files = append(files, file)
		info, err := file.Stat()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("only regular log files can be cleared")
		}
		for _, other := range processes {
			if other.ID == selected.ID {
				continue
			}
			for _, otherPath := range []string{other.Env.OutLog, other.Env.ErrLog} {
				otherInfo, err := os.Stat(otherPath)
				if err == nil && os.SameFile(info, otherInfo) {
					return fmt.Errorf("log file is shared with PM2 #%d; clearing only the selected instance is not possible", other.ID)
				}
			}
		}
	}
	// Check both files before changing either of them.
	for _, file := range files {
		if err := file.Truncate(0); err != nil {
			return fmt.Errorf("could not clear all logs; some may already be cleared: %w", err)
		}
	}
	return nil
}

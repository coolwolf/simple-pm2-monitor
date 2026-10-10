package main

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
)

type browseEntry struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Directory bool   `json:"directory"`
}

type browseResult struct {
	Path      string        `json:"path"`
	Parent    string        `json:"parent"`
	Entries   []browseEntry `json:"entries"`
	Truncated bool          `json:"truncated"`
}

func (s *server) handleBrowse(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		failure(w, 405, errors.New("use GET to browse directories"))
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		var err error
		path, err = os.UserHomeDir()
		if err != nil {
			failure(w, 400, err)
			return
		}
	}
	if !filepath.IsAbs(path) {
		failure(w, 400, errors.New("directory path must be absolute"))
		return
	}
	path = filepath.Clean(path)
	dir, err := os.Open(path)
	if err != nil {
		failure(w, 400, err)
		return
	}
	defer dir.Close()
	// Bound filesystem work even for unusually large directories.
	entries, err := dir.ReadDir(5001)
	if err != nil && err != io.EOF {
		failure(w, 400, err)
		return
	}
	result := browseResult{Path: path, Parent: filepath.Dir(path), Entries: []browseEntry{}, Truncated: len(entries) > 5000}
	if result.Truncated {
		entries = entries[:5000]
	}
	for _, entry := range entries {
		if entry.Name()[0] == '.' && r.URL.Query().Get("hidden") != "true" {
			continue
		}
		fullPath := filepath.Join(path, entry.Name())
		info, err := os.Stat(fullPath)
		if err != nil {
			continue
		}
		if info.IsDir() || (info.Mode().IsRegular() && isProjectFile(fullPath)) {
			result.Entries = append(result.Entries, browseEntry{entry.Name(), fullPath, info.IsDir()})
		}
	}
	sort.Slice(result.Entries, func(i, j int) bool {
		a, b := result.Entries[i], result.Entries[j]
		if a.Directory != b.Directory {
			return a.Directory
		}
		return a.Name < b.Name
	})
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, result)
}

package main

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestBrowseHomeNavigationAndFiltering(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, name := range []string{"service", ".hidden"} {
		if err := os.Mkdir(filepath.Join(home, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"App.sln", "package.json", "credentials.txt"} {
		if err := os.WriteFile(filepath.Join(home, name), []byte("test"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(home, "service"), filepath.Join(home, "linked")); err != nil {
		t.Fatal(err)
	}
	s := testServer(t)
	w := call(s, "GET", "/api/browse", "", "", "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	var result browseResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Path != home || result.Parent != filepath.Dir(home) || len(result.Entries) != 4 {
		t.Fatal(result)
	}
	if result.Entries[0].Name != "linked" || !result.Entries[0].Directory || result.Entries[2].Name != "App.sln" {
		t.Fatal(result.Entries)
	}
	w = call(s, "GET", "/api/browse?hidden=true&path="+url.QueryEscape(home), "", "", "")
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 5 {
		t.Fatal(result)
	}
	w = call(s, "GET", "/api/browse?path="+url.QueryEscape(filepath.Join(home, "linked")), "", "", "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 0 {
		t.Fatal(result)
	}
	for _, path := range []string{"relative", filepath.Join(home, "missing"), filepath.Join(home, "App.sln")} {
		w = call(s, "GET", "/api/browse?path="+url.QueryEscape(path), "", "", "")
		if w.Code != 400 {
			t.Fatal(path, w.Code)
		}
	}
	if w := call(s, "POST", "/api/browse", "{}", "", ""); w.Code != 405 {
		t.Fatal(w.Code)
	}
	if w := basicCall(s, "GET", "/api/browse", "", "wrong", "wrong"); w.Code != 401 {
		t.Fatal(w.Code)
	}
}

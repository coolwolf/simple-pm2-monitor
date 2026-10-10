package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func basicCall(s *server, method, path, body, username, password string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Host = "dev.example:8989"
	r.SetBasicAuth(username, password)
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	return w
}
func TestAdminLoginRejectsBearerAuthentication(t *testing.T) {
	s := testServer(t)
	s.config = Config{AdminUsername: "owner", AdminPassword: "password\u2603:word", Projects: []Project{}}
	for _, test := range []struct {
		user, password string
		status         int
	}{{"owner", "password\u2603:word", 200}, {"wrong", "password\u2603:word", 401}, {"owner", "wrong", 401}, {"", "", 401}} {
		w := basicCall(s, "GET", "/api/config", "", test.user, test.password)
		if w.Code != test.status {
			t.Fatal(w.Code)
		}
		if w.Code != 200 && strings.Contains(w.Body.String(), "password\u2603:word") {
			t.Fatal("credentials leaked in login error")
		}
	}
	if w := call(s, "GET", "/api/projects", "", "Bearer old-secret", ""); w.Code != 401 {
		t.Fatal("Bearer auth accepted")
	}
	if w := basicCall(s, "GET", "/api/projects", "", "owner", "password\u2603:word"); w.Code != 200 {
		t.Fatal(w.Code)
	}
}
func TestAdminConfigurationSavePreservesAndUpdatesCredentials(t *testing.T) {
	s := testServer(t)
	s.config = Config{Listen: defaultListen, AdminUsername: "owner", AdminPassword: "old-pass", Projects: []Project{}}
	if w := basicCall(s, "PUT", "/api/config", `{"projects":[]}`, "owner", "old-pass"); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if s.config.AdminUsername != "owner" || s.config.AdminPassword != "old-pass" {
		t.Fatal("project save removed credentials")
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatal(err)
	}
	var saved Config
	if err = json.Unmarshal(data, &saved); err != nil || saved.AdminPassword != "old-pass" {
		t.Fatal("credentials not persisted")
	}
	if w := basicCall(s, "PUT", "/api/config", `{"adminUsername":"new-owner","adminPassword":"new-pass","projects":[]}`, "owner", "old-pass"); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if w := basicCall(s, "GET", "/api/config", "", "owner", "old-pass"); w.Code != 401 {
		t.Fatal("old login remained valid")
	}
	if w := basicCall(s, "GET", "/api/config", "", "new-owner", "new-pass"); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := basicCall(s, "PUT", "/api/config", `{"adminUsername":"owner","projects":[]}`, "new-owner", "new-pass"); w.Code != 400 {
		t.Fatal("partial credentials accepted")
	}
}
func TestSavedAdminAllowsPublicListen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "projects.json")
	c := Config{Listen: "0.0.0.0:8989", AdminUsername: "owner", AdminPassword: "saved-password", Projects: []Project{}}
	if err := saveConfig(path, c); err != nil {
		t.Fatal(err)
	}
	loaded, address, err := prepareConfig(path, defaultListen, false, false, strings.NewReader(""), &bytes.Buffer{})
	if err != nil || address != c.Listen || loaded.AdminUsername != c.AdminUsername || loaded.AdminPassword != c.AdminPassword {
		t.Fatal("saved admin failed to load", err)
	}
}
func TestEnvironmentCredentialsAreIgnored(t *testing.T) {
	t.Setenv("PM2_MONITOR_TOKEN", "old-secret")
	t.Setenv("PM2_MONITOR_ADMIN_USERNAME", "env-user")
	t.Setenv("PM2_MONITOR_ADMIN_PASSWORD", "env-password")
	path := filepath.Join(t.TempDir(), "projects.json")
	saved := Config{Listen: "0.0.0.0:8989", AdminUsername: "json-user", AdminPassword: "json-password", Projects: []Project{}}
	if err := saveConfig(path, saved); err != nil {
		t.Fatal(err)
	}
	c, _, err := prepareConfig(path, defaultListen, false, true, strings.NewReader(""), &bytes.Buffer{})
	if err != nil || c.AdminUsername != saved.AdminUsername || c.AdminPassword != saved.AdminPassword {
		t.Fatal("JSON credentials overridden", err)
	}
	s := testServer(t)
	s.config = c
	if w := basicCall(s, "GET", "/api/config", "", "env-user", "env-password"); w.Code != 401 {
		t.Fatal("environment login accepted")
	}
	if w := call(s, "GET", "/api/config", "", "Bearer old-secret", ""); w.Code != 401 {
		t.Fatal("environment token accepted")
	}
	missing := filepath.Join(t.TempDir(), "projects.json")
	if _, _, err := prepareConfig(missing, defaultListen, false, true, strings.NewReader(""), &bytes.Buffer{}); err == nil {
		t.Fatal("environment created config")
	}
}
func TestAdminValidation(t *testing.T) {
	for _, c := range []Config{{AdminUsername: "owner"}, {AdminPassword: "pass"}, {AdminUsername: "bad:user", AdminPassword: "pass"}, {AdminUsername: "owner", AdminPassword: " "}} {
		if err := validate(c); err == nil {
			t.Fatal("invalid admin accepted")
		}
	}
}

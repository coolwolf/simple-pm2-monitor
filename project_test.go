package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func projectBody(t *testing.T, name string, p *Project) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{"name": name, "project": p})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
func TestProjectCRUDPreservesOtherSettingsAndDoesNotRunPM2(t *testing.T) {
	s := testServer(t)
	s.config.Listen = "127.0.0.1:8989"
	s.run = func(context.Context, ...string) ([]byte, error) {
		t.Fatal("project mutation invoked PM2")
		return nil, nil
	}
	api := project(t, "api")
	web := project(t, "web")
	for _, p := range []*Project{&api, &web} {
		w := call(s, "POST", "/api/project", projectBody(t, "", p), "", "")
		if w.Code != 201 {
			t.Fatal(w.Code, w.Body)
		}
	}
	api.Name = "renamed-api"
	api.Group = "new-group"
	api.Port = 6001
	api.Args = []string{"watch", "run"}
	api.Env = map[string]string{"MODE": "updated"}
	api.MetricsURL = "http://localhost:6001/metrics"
	api.RequestMetric = "requests_total"
	w := call(s, "PUT", "/api/project", projectBody(t, "api", &api), "", "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if len(s.config.Projects) != 2 || !reflect.DeepEqual(s.config.Projects[0], api) || !reflect.DeepEqual(s.config.Projects[1], web) {
		t.Fatal("edit lost order or project data")
	}
	w = call(s, "DELETE", "/api/project", `{"name":"renamed-api"}`, "", "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if len(s.config.Projects) != 1 || s.config.Projects[0].Name != "web" || s.config.AdminUsername != "test-admin" || s.config.AdminPassword != "test-password" || s.config.Listen != "127.0.0.1:8989" {
		t.Fatal("delete lost unrelated settings")
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatal(err)
	}
	var persisted Config
	if err = json.Unmarshal(b, &persisted); err != nil || !reflect.DeepEqual(persisted, s.config) {
		t.Fatal("persisted settings differ", err)
	}
}
func TestProjectMutationsRejectDuplicateMissingAndInvalidProjects(t *testing.T) {
	s := testServer(t)
	api := project(t, "api")
	web := project(t, "web")
	s.config.Projects = []Project{api, web}
	for _, tc := range []struct {
		method, body string
		status       int
	}{
		{"POST", projectBody(t, "", &api), 409},
		{"PUT", projectBody(t, "web", &api), 409},
		{"PUT", projectBody(t, "missing", &api), 404},
		{"DELETE", `{"name":"missing"}`, 404},
		{"PUT", projectBody(t, "", &api), 400},
		{"POST", `{}`, 400},
		{"DELETE", `{}`, 400},
		{"POST", `{"project":{"name":"bad","cwd":"relative","command":"dotnet"}}`, 400},
		{"POST", `{"project":null}`, 400},
		{"POST", `{"extra":true}`, 400},
	} {
		w := call(s, tc.method, "/api/project", tc.body, "", "")
		if w.Code != tc.status {
			t.Fatal(tc.method, w.Code, w.Body)
		}
		if len(s.config.Projects) != 2 || !reflect.DeepEqual(s.config.Projects[0], api) || !reflect.DeepEqual(s.config.Projects[1], web) {
			t.Fatal("failed request mutated settings")
		}
	}
}
func TestProjectFailedWriteDoesNotMutateMemory(t *testing.T) {
	s := testServer(t)
	api := project(t, "api")
	s.config.Projects = []Project{api}
	s.path = filepath.Join(t.TempDir(), "missing", "projects.json")
	updated := api
	updated.Group = "changed"
	for _, tc := range []struct{ method, body string }{{"POST", projectBody(t, "", func() *Project { p := project(t, "new"); return &p }())}, {"PUT", projectBody(t, "api", &updated)}, {"DELETE", `{"name":"api"}`}} {
		w := call(s, tc.method, "/api/project", tc.body, "", "")
		if w.Code != 500 || len(s.config.Projects) != 1 || !reflect.DeepEqual(s.config.Projects[0], api) {
			t.Fatal(w.Code, "write failure changed memory")
		}
	}
}
func TestProjectMutationRequiresAuthentication(t *testing.T) {
	s := testServer(t)
	for _, method := range []string{"POST", "PUT", "DELETE"} {
		req := httptest.NewRequest(method, "/api/project", strings.NewReader(`{"name":"api"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.routes().ServeHTTP(w, req)
		if w.Code != 401 {
			t.Fatal(method, w.Code)
		}
	}
}

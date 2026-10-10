package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func logFixture(t *testing.T) (*server, []Process) {
	t.Helper()
	s := testServer(t)
	s.config.Projects = []Project{project(t, "api")}
	root := t.TempDir()
	processes := []Process{{ID: 0, Name: "api"}, {ID: 8, Name: "other"}}
	for i := range processes {
		processes[i].Env.OutLog = fixture(t, root, processes[i].Name+"-out.log", "old output\n")
		processes[i].Env.ErrLog = fixture(t, root, processes[i].Name+"-error.log", "old errors\n")
	}
	return s, processes
}
func mockLogList(s *server, processes []Process) {
	s.run = func(_ context.Context, args ...string) ([]byte, error) { return json.Marshal(processes) }
}
func TestClearLogsUsesSelectedInstanceAndPreservesOpenFiles(t *testing.T) {
	s, processes := logFixture(t)
	mockLogList(s, processes)
	path := processes[0].Env.OutLog
	before, _ := os.Stat(path)
	writer, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	w := call(s, "DELETE", "/api/logs", `{"id":0}`, "", "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	for _, p := range []string{path, processes[0].Env.ErrLog} {
		data, err := os.ReadFile(p)
		if err != nil || len(data) != 0 {
			t.Fatal(p, string(data), err)
		}
	}
	after, _ := os.Stat(path)
	if !os.SameFile(before, after) || before.Mode() != after.Mode() {
		t.Fatal("replaced the log file")
	}
	writer.WriteString("new output\n")
	data, _ := os.ReadFile(path)
	if string(data) != "new output\n" {
		t.Fatal(string(data))
	}
	for _, p := range []string{processes[1].Env.OutLog, processes[1].Env.ErrLog} {
		data, _ := os.ReadFile(p)
		if len(data) == 0 {
			t.Fatal("cleared another instance")
		}
	}
}
func TestClearLogsRejectsInvalidTargetsAndUnmanagedProcesses(t *testing.T) {
	s, processes := logFixture(t)
	mockLogList(s, processes)
	for _, test := range []struct {
		body string
		code int
	}{{`{}`, 400}, {`{"id":-1}`, 400}, {`{"id":"0"}`, 400}, {`{"id":0,"path":"/tmp/anything"}`, 400}, {`{"id":99}`, 404}, {`{"id":8}`, 403}} {
		w := call(s, "DELETE", "/api/logs", test.body, "", "")
		if w.Code != test.code {
			t.Fatal(test.body, w.Code, w.Body)
		}
	}
	if w := basicCall(s, "DELETE", "/api/logs", `{"id":0}`, "wrong", "wrong"); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := call(s, "DELETE", "/api/logs", `{"id":0}`, "", "https://other.example"); w.Code != 403 {
		t.Fatal(w.Code)
	}
	data, _ := os.ReadFile(processes[0].Env.OutLog)
	if len(data) == 0 {
		t.Fatal("rejected request changed logs")
	}
}
func TestClearLogsRefusesSharedAndUnsafeFilesBeforeTruncating(t *testing.T) {
	for _, kind := range []string{"shared-path", "hardlink", "symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			s, processes := logFixture(t)
			switch kind {
			case "shared-path":
				processes[1].Env.ErrLog = processes[0].Env.ErrLog
			case "hardlink":
				other := filepath.Join(t.TempDir(), "shared.log")
				if err := os.Link(processes[0].Env.ErrLog, other); err != nil {
					t.Fatal(err)
				}
				processes[1].Env.ErrLog = other
			case "symlink":
				link := filepath.Join(t.TempDir(), "link.log")
				if err := os.Symlink(processes[0].Env.ErrLog, link); err != nil {
					t.Fatal(err)
				}
				processes[0].Env.ErrLog = link
			case "directory":
				processes[0].Env.ErrLog = t.TempDir()
			}
			mockLogList(s, processes)
			w := call(s, "DELETE", "/api/logs", `{"id":0}`, "", "")
			if w.Code != 409 {
				t.Fatal(w.Code, w.Body)
			}
			data, _ := os.ReadFile(processes[0].Env.OutLog)
			if string(data) != "old output\n" {
				t.Fatal("cleared stdout before checking stderr")
			}
		})
	}
}
func TestClearLogsMissingFilesAreNotCreated(t *testing.T) {
	s, processes := logFixture(t)
	if err := os.Remove(processes[0].Env.ErrLog); err != nil {
		t.Fatal(err)
	}
	mockLogList(s, processes)
	w := call(s, "DELETE", "/api/logs", `{"id":0}`, "", "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if _, err := os.Stat(processes[0].Env.ErrLog); !os.IsNotExist(err) {
		t.Fatal("created missing log", err)
	}
}

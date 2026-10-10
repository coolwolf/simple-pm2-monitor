package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFirstRunDefaultsSavedAndNotAskedAgain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "projects.json")
	var out bytes.Buffer
	c, address, err := prepareConfig(path, defaultListen, false, false, strings.NewReader("\n\n\nsetup-password\n\n\n"), &out)
	if err != nil || address != defaultListen || c.Listen != defaultListen || len(c.Projects) != 0 {
		t.Fatalf("%+v %s %v", c, address, err)
	}
	for _, prompt := range []string{"Listening IP", "Listening port", "Admin username", "Admin password", "Run as service", "Add project settings"} {
		if !strings.Contains(out.String(), prompt) {
			t.Fatal(out.String())
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved Config
	if err = json.Unmarshal(data, &saved); err != nil || saved.Listen != defaultListen || saved.Projects == nil || saved.AdminUsername != "admin" || saved.AdminPassword != "setup-password" {
		t.Fatal(string(data), err)
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0600 {
		t.Fatal(st.Mode())
	}
	out.Reset()
	_, address, err = prepareConfig(path, "127.0.0.1:9999", false, false, strings.NewReader(""), &out)
	if err != nil || address != defaultListen || out.Len() != 0 {
		t.Fatal(address, err, out.String())
	}
}
func TestFirstRunProjectsAndRetryInvalidInputs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "projects.json")
	input := strings.Join([]string{
		"bad-ip", "::1", "0", "99999", "9090", "bad:user", "admin", "", "setup-password", "n", "maybe", "y",
		"", "api", "shop", "relative", "/srv/api", "", "/usr/bin/dotnet",
		"null", `[1]`, `["watch","run"]`, `[]`, `{"MODE":123}`, `{"MODE":"dev"}`, "-1", "5001",
		"y", "api", "web", "shop", "/srv/web", "/usr/bin/npm", `["run","dev"]`, `{}`, "3000", "n",
	}, "\n") + "\n"
	var out bytes.Buffer
	c, address, err := prepareConfig(path, defaultListen, false, false, strings.NewReader(input), &out)
	if err != nil || address != "[::1]:9090" || len(c.Projects) != 2 {
		t.Fatalf("%+v %s %v\n%s", c, address, err, out.String())
	}
	if c.Projects[0].Env["MODE"] != "dev" || c.Projects[0].Port != 5001 || c.Projects[1].Name != "web" {
		t.Fatal(c.Projects)
	}
	if !strings.Contains(out.String(), "Error:") {
		t.Fatal("validation did not reprompt")
	}
}
func TestInterruptedSetupDoesNotWriteConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "projects.json")
	_, _, err := prepareConfig(path, defaultListen, false, false, strings.NewReader("\n\n"), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "non-interactive") {
		t.Fatal(err)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("partial config persisted")
	}
}
func TestListenPrecedenceAndLegacyConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "projects.json")
	if err := saveConfig(path, Config{Listen: "127.0.0.1:9090", AdminUsername: "test-admin", AdminPassword: "test-password", Projects: []Project{}}); err != nil {
		t.Fatal(err)
	}
	for _, override := range []bool{false, true} {
		_, address, err := prepareConfig(path, "127.0.0.1:7070", override, false, strings.NewReader(""), &bytes.Buffer{})
		want := "127.0.0.1:9090"
		if override {
			want = "127.0.0.1:7070"
		}
		if err != nil || address != want {
			t.Fatal(address, err)
		}
	}
	if err := os.WriteFile(path, []byte(`{"projects":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	_, address, err := prepareConfig(path, defaultListen, false, false, strings.NewReader(""), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "adminUsername") {
		t.Fatal("legacy config without credentials accepted", address, err)
	}
}
func TestNonInteractiveRequiresExistingConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "projects.json")
	_, _, err := prepareConfig(path, defaultListen, false, true, strings.NewReader(""), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "adminUsername") {
		t.Fatal("missing file accepted", err)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("invalid config written")
	}
	saved := Config{Listen: "0.0.0.0:8989", AdminUsername: "admin", AdminPassword: "saved-password", Projects: []Project{}}
	if err = saveConfig(path, saved); err != nil {
		t.Fatal(err)
	}
	c, address, err := prepareConfig(path, defaultListen, false, true, strings.NewReader(""), &bytes.Buffer{})
	if err != nil || address != saved.Listen || c.AdminPassword != saved.AdminPassword {
		t.Fatal("saved config failed", err)
	}
	var out bytes.Buffer
	c, err = firstRun(strings.NewReader("0.0.0.0\n\n\nsetup-password\nn\nn\n"), &out, defaultListen)
	if err != nil || c.Listen != "0.0.0.0:8989" || c.AdminUsername != "admin" || c.AdminPassword != "setup-password" {
		t.Fatal("public wizard failed", err)
	}
}
func TestWebProjectSavePreservesListen(t *testing.T) {
	s := testServer(t)
	s.config = Config{Listen: "127.0.0.1:9090", AdminUsername: "test-admin", AdminPassword: "test-password", Projects: []Project{}}
	w := call(s, "PUT", "/api/config", `{"projects":[]}`, "", "")
	if w.Code != 200 || s.config.Listen != "127.0.0.1:9090" {
		t.Fatal(w.Code, w.Body)
	}
	w = call(s, "PUT", "/api/config", `{"listen":"127.0.0.1:0","projects":[]}`, "", "")
	if w.Code != 400 || s.config.Listen != "127.0.0.1:9090" {
		t.Fatal(w.Code, w.Body)
	}
	w = call(s, "PUT", "/api/config", `{"listen":"127.0.0.1:8989","projects":[]}`, "", "")
	if w.Code != 200 || s.config.Listen != "127.0.0.1:8989" {
		t.Fatal(w.Code, w.Body)
	}
}

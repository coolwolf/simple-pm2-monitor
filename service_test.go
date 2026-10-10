package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type fakeService struct {
	enabled, active, unavailable, failEnable bool
	calls                                    [][]string
}

func serviceFixture(t *testing.T) (*serviceManager, *fakeService) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "service files")
	src := fixture(t, root, "source", "fake dashboard executable")
	config := fixture(t, root, "projects.json", `{"adminPassword":"private-password"}`)
	fake := &fakeService{}
	m := &serviceManager{unitPath: filepath.Join(root, "config/systemd/user", serviceUnit), binaryPath: filepath.Join(root, "data/simple-pm2-monitor"), sourcePath: src, configPath: config, pm2Path: "/usr/bin/pm2", home: root, pathEnv: "/usr/bin:/bin", pm2Home: filepath.Join(root, ".pm2")}
	m.run = func(_ context.Context, args ...string) ([]byte, error) {
		fake.calls = append(fake.calls, append([]string{}, args...))
		switch args[0] {
		case "show-environment":
			if fake.unavailable {
				return nil, errors.New("no user bus")
			}
		case "is-enabled":
			if fake.enabled {
				return []byte("enabled\n"), nil
			}
			return []byte("disabled\n"), errors.New("disabled")
		case "is-active":
			if fake.active {
				return []byte("active\n"), nil
			}
			return []byte("inactive\n"), errors.New("inactive")
		case "enable":
			if fake.failEnable {
				return nil, errors.New("cannot enable")
			}
			fake.enabled = true
		case "disable":
			fake.enabled = false
		case "start":
			fake.active = true
		}
		return nil, nil
	}
	return m, fake
}
func TestServiceInstallationCopiesBinaryAndPreservesUserEnvironment(t *testing.T) {
	m, fake := serviceFixture(t)
	m.listenOverride = "0.0.0.0:8989"
	if err := m.enable(context.Background()); err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(m.binaryPath)
	if err != nil || string(binary) != "fake dashboard executable" {
		t.Fatal(string(binary), err)
	}
	st, _ := os.Stat(m.binaryPath)
	if st.Mode().Perm() != 0755 {
		t.Fatal(st.Mode())
	}
	unit, err := os.ReadFile(m.unitPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{serviceMarker, "WantedBy=default.target", "DEV_HARBOR_SERVICE=1", "PM2_HOME=", "-non-interactive", `"-listen" "0.0.0.0:8989"`, m.configPath, m.pm2Path} {
		if !strings.Contains(string(unit), want) {
			t.Fatal(want, string(unit))
		}
	}
	if strings.Contains(string(unit), "private-password") || strings.Contains(string(unit), "User=") {
		t.Fatal("credentials or a different OS user written to unit")
	}
	if !fake.enabled || fake.active {
		t.Fatal("installation started before HTTP listener handover")
	}
	if err := m.start(context.Background()); err != nil || !fake.active {
		t.Fatal(err)
	}
	m.running = true
	if err := m.enable(context.Background()); err != nil {
		t.Fatal("running service could not be enabled", err)
	}
	if err := m.disable(context.Background()); err != nil || fake.enabled || !fake.active {
		t.Fatal("disable should only change autostart", err)
	}
}
func TestServiceRejectsUnavailableManagerAndCustomUnits(t *testing.T) {
	m, fake := serviceFixture(t)
	fake.unavailable = true
	if err := m.enable(context.Background()); err == nil {
		t.Fatal("ignored unavailable user manager")
	}
	if _, err := os.Stat(m.unitPath); !os.IsNotExist(err) {
		t.Fatal("wrote files without a user manager")
	}
	fake.unavailable = false
	fixture(t, filepath.Dir(m.unitPath), filepath.Base(m.unitPath), "[Service]\nExecStart=/usr/bin/true\n")
	if err := m.enable(context.Background()); err == nil {
		t.Fatal("overwrote custom service")
	}
	if err := m.disable(context.Background()); err == nil {
		t.Fatal("disabled custom service")
	}
	data, _ := os.ReadFile(m.unitPath)
	if strings.HasPrefix(string(data), serviceMarker) {
		t.Fatal("custom unit changed")
	}
}
func TestServiceAPISchedulesHandoverOnlyAfterSuccessfulEnable(t *testing.T) {
	s := testServer(t)
	m, fake := serviceFixture(t)
	s.service = m
	s.handoff = make(chan struct{}, 1)
	fake.failEnable = true
	w := call(s, "POST", "/api/service", `{"action":"enable"}`, "", "")
	if w.Code != 500 || len(s.handoff) != 0 {
		t.Fatal(w.Code, w.Body)
	}
	fake.failEnable = false
	w = call(s, "POST", "/api/service", `{"action":"enable"}`, "", "")
	var response struct{ Handoff bool }
	json.Unmarshal(w.Body.Bytes(), &response)
	if w.Code != 200 || !response.Handoff || len(s.handoff) != 1 || fake.active {
		t.Fatal(w.Code, w.Body)
	}
	<-s.handoff
	m.running = true
	fake.active = true
	w = call(s, "POST", "/api/service", `{"action":"disable"}`, "", "")
	if w.Code != 200 || fake.enabled || !fake.active || len(s.handoff) != 0 {
		t.Fatal(w.Code, w.Body)
	}
	for _, body := range []string{`{"action":"stop"}`, `{"action":"enable","command":"anything"}`} {
		if w := call(s, "POST", "/api/service", body, "", ""); w.Code != 400 {
			t.Fatal(w.Code)
		}
	}
	if w := basicCall(s, "POST", "/api/service", `{"action":"enable"}`, "wrong", "wrong"); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := call(s, "POST", "/api/service", `{"action":"enable"}`, "", "https://other.example"); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := call(s, "GET", "/api/service", "", "", ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
}
func TestFirstRunServiceChoiceIsTransientAndConfigIsSavedBeforeInstallation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "projects.json")
	var output bytes.Buffer
	c, _, err := prepareConfig(path, defaultListen, false, false, strings.NewReader("\n\n\npassword\ny\nn\n"), &output)
	if err != nil || !c.runAsService {
		t.Fatal(c, err, output.String())
	}
	data, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(data), "runAsService") {
		t.Fatal(string(data), err)
	}
	c, _, err = prepareConfig(path, defaultListen, false, true, strings.NewReader(""), &output)
	if err != nil || c.runAsService {
		t.Fatal("service startup must not rerun setup or reinstall", err)
	}
}
func TestServiceUnitQuotingAndSyntax(t *testing.T) {
	m, _ := serviceFixture(t)
	m.binaryPath = "/usr/bin/true"
	m.configPath = filepath.Join(filepath.Dir(m.configPath), "project $name% file.json")
	data, err := m.unit()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "$$name%% file.json") {
		t.Fatal(string(data))
	}
	m.pathEnv = "invalid\nEnvironment=INJECTED=1"
	if _, err = m.unit(); err == nil {
		t.Fatal("accepted a multiline environment value")
	}
	tool, err := exec.LookPath("systemd-analyze")
	if err != nil {
		t.Skip("systemd-analyze unavailable")
	}
	m.pathEnv = "/usr/bin:/bin"
	data, _ = m.unit()
	path := filepath.Join(t.TempDir(), serviceUnit)
	os.WriteFile(path, data, 0600)
	if output, err := exec.Command(tool, "verify", path).CombinedOutput(); err != nil {
		t.Fatalf("invalid generated systemd unit: %v: %s", err, output)
	}
}

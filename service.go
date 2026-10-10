package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const serviceUnit = "simple-pm2-monitor.service"
const serviceMarker = "# Managed by simple-pm2-monitor"

type serviceManager struct {
	mu                                                                                      sync.Mutex
	unitPath, binaryPath, sourcePath, configPath, pm2Path, home, pathEnv, pm2Home, username string
	listenOverride                                                                          string
	running                                                                                 bool
	run                                                                                     func(context.Context, ...string) ([]byte, error)
}
type serviceStatus struct {
	Available        bool   `json:"available"`
	Installed        bool   `json:"installed"`
	Enabled          bool   `json:"enabled"`
	Active           bool   `json:"active"`
	RunningAsService bool   `json:"runningAsService"`
	Linger           bool   `json:"linger"`
	UnitPath         string `json:"unitPath"`
	Error            string `json:"error,omitempty"`
	Hint             string `json:"hint,omitempty"`
}

func newServiceManager(config, pm2, listen string, override bool) (*serviceManager, error) {
	if runtime.GOOS != "linux" {
		return nil, errors.New("Run as service requires Linux and a systemd user manager")
	}
	ctl, err := exec.LookPath("systemctl")
	if err != nil {
		return nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	config, err = filepath.Abs(config)
	if err != nil {
		return nil, err
	}
	pm2, err = exec.LookPath(pm2)
	if err != nil {
		return nil, err
	}
	pm2, err = filepath.Abs(pm2)
	if err != nil {
		return nil, err
	}
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		configHome = filepath.Join(home, ".config")
	}
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		dataHome = filepath.Join(home, ".local", "share")
	}
	if !filepath.IsAbs(configHome) || !filepath.IsAbs(dataHome) {
		return nil, errors.New("XDG config/data directories must be absolute")
	}
	m := &serviceManager{unitPath: filepath.Join(configHome, "systemd", "user", serviceUnit), binaryPath: filepath.Join(dataHome, "simple-pm2-monitor", "simple-pm2-monitor"), sourcePath: executable, configPath: config, pm2Path: pm2, home: home, pathEnv: os.Getenv("PATH"), pm2Home: os.Getenv("PM2_HOME"), running: os.Getenv("DEV_HARBOR_SERVICE") == "1"}
	if m.pm2Home == "" {
		m.pm2Home = filepath.Join(home, ".pm2")
	}
	if u, err := user.Current(); err == nil {
		m.username = u.Username
	}
	if override {
		m.listenOverride = listen
	}
	m.run = func(ctx context.Context, args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, ctl, append([]string{"--user"}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return out, fmt.Errorf("systemctl --user %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
		}
		return out, nil
	}
	return m, nil
}
func (m *serviceManager) status(ctx context.Context) serviceStatus {
	st := serviceStatus{RunningAsService: m.running, UnitPath: m.unitPath}
	if _, err := m.run(ctx, "show-environment"); err != nil {
		st.Error = "Cannot connect to the systemd user manager."
		st.Hint = "Run systemctl --user status in a login session on this server."
		return st
	}
	st.Available = true
	if content, err := os.ReadFile(m.unitPath); err == nil {
		st.Installed = true
		if !strings.HasPrefix(string(content), serviceMarker+"\n") {
			st.Available = false
			st.Error = "A custom user service already uses this unit name; it will not be changed."
			return st
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		st.Available = false
		st.Error = "Cannot read the user service unit: " + err.Error()
		return st
	}
	enabled, _ := m.run(ctx, "is-enabled", serviceUnit)
	st.Enabled = strings.TrimSpace(string(enabled)) == "enabled"
	active, _ := m.run(ctx, "is-active", serviceUnit)
	st.Active = strings.TrimSpace(string(active)) == "active"
	if m.username != "" {
		if _, err := os.Stat(filepath.Join("/var/lib/systemd/linger", m.username)); err == nil {
			st.Linger = true
		}
		st.Hint = "For startup at boot and after logout, run: sudo loginctl enable-linger " + m.username
	}
	if st.Linger {
		st.Hint = "User lingering is enabled: the service can run at boot and after logout."
	}
	return st
}
func serviceQuote(value string) (string, error) {
	if strings.ContainsAny(value, "\x00\r\n") {
		return "", errors.New("service paths and environment values cannot contain control characters")
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%").Replace(value) + `"`, nil
}
func (m *serviceManager) unit() ([]byte, error) {
	args := []string{m.binaryPath, "-non-interactive", "-config", m.configPath, "-pm2", m.pm2Path}
	if m.listenOverride != "" {
		args = append(args, "-listen", m.listenOverride)
	}
	words := []string{}
	for _, arg := range args {
		// ExecStart also performs dollar expansion; $$ denotes a literal dollar.
		word, err := serviceQuote(strings.ReplaceAll(arg, "$", "$$"))
		if err != nil {
			return nil, err
		}
		words = append(words, word)
	}
	working := filepath.Dir(m.configPath)
	_, err := serviceQuote(working)
	if err != nil {
		return nil, err
	}
	// WorkingDirectory uses a single path value, not ExecStart's word parser.
	working = strings.ReplaceAll(working, "%", "%%")
	env := []string{}
	for _, value := range []string{"PATH=" + m.pathEnv, "HOME=" + m.home, "PM2_HOME=" + m.pm2Home, "DEV_HARBOR_SERVICE=1"} {
		quoted, err := serviceQuote(value)
		if err != nil {
			return nil, err
		}
		env = append(env, quoted)
	}
	return []byte(fmt.Sprintf("%s\n[Unit]\nDescription=Dev Harbor PM2 development monitor\n\n[Service]\nType=simple\nWorkingDirectory=%s\nEnvironment=%s\nExecStart=%s\nRestart=on-failure\nRestartSec=3\n\n[Install]\nWantedBy=default.target\n", serviceMarker, working, strings.Join(env, " "), strings.Join(words, " "))), nil
}
func atomicServiceFile(path string, mode os.FileMode, reader io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".service-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = f.Chmod(mode); err != nil {
		return err
	}
	if _, err = io.Copy(f, reader); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
func (m *serviceManager) enable(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.status(ctx)
	if !st.Available {
		return errors.New(st.Error + " " + st.Hint)
	}
	if st.Active && !m.running {
		return errors.New("the user service is already running; use its dashboard or stop it before switching this instance")
	}
	if os.Getenv("INVOCATION_ID") != "" && !m.running {
		return errors.New("this dashboard is managed by another service; manage that service before installing a user service")
	}
	if old, err := os.ReadFile(m.unitPath); err == nil {
		if !strings.HasPrefix(string(old), serviceMarker+"\n") {
			return errors.New("a custom user service already exists; it will not be overwritten")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	unit, err := m.unit()
	if err != nil {
		return err
	}
	source, err := os.Open(m.sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	srcInfo, err := source.Stat()
	if err != nil {
		return err
	}
	destInfo, _ := os.Stat(m.binaryPath)
	if destInfo == nil || !os.SameFile(srcInfo, destInfo) {
		if err := atomicServiceFile(m.binaryPath, 0755, source); err != nil {
			return err
		}
	}
	if err := atomicServiceFile(m.unitPath, 0600, strings.NewReader(string(unit))); err != nil {
		return err
	}
	if _, err = m.run(ctx, "daemon-reload"); err != nil {
		return err
	}
	_, err = m.run(ctx, "enable", serviceUnit)
	return err
}
func (m *serviceManager) start(ctx context.Context) error {
	_, err := m.run(ctx, "start", serviceUnit)
	return err
}
func (m *serviceManager) disable(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	content, err := os.ReadFile(m.unitPath)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(string(content), serviceMarker+"\n") {
		return errors.New("a custom user service will not be changed")
	}
	_, err = m.run(ctx, "disable", serviceUnit)
	return err
}
func (s *server) handleService(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "POST" {
		failure(w, 405, errors.New("use GET or POST"))
		return
	}
	if s.service == nil {
		if r.Method == "GET" {
			writeJSON(w, 200, serviceStatus{Error: "Run as service is unavailable. Linux, systemctl, and a PM2 executable are required."})
			return
		}
		failure(w, 503, errors.New("systemd user service is unavailable"))
		return
	}
	if r.Method == "GET" {
		writeJSON(w, 200, s.service.status(r.Context()))
		return
	}
	var request struct {
		Action string `json:"action"`
	}
	if err := decode(w, r, &request); err != nil {
		failure(w, 400, err)
		return
	}
	switch request.Action {
	case "enable":
		if !s.service.running && s.handoff == nil {
			failure(w, 503, errors.New("service handover is unavailable"))
			return
		}
		if err := s.service.enable(r.Context()); err != nil {
			failure(w, 500, err)
			return
		}
		handoff := !s.service.running
		writeJSON(w, 200, map[string]any{"handoff": handoff, "message": "Service installed and enabled.", "status": s.service.status(r.Context())})
		if handoff {
			select {
			case s.handoff <- struct{}{}:
			default:
			}
		}
	case "disable":
		if err := s.service.disable(r.Context()); err != nil {
			failure(w, 500, err)
			return
		}
		writeJSON(w, 200, map[string]any{"handoff": false, "message": "Autostart disabled. The current dashboard keeps running.", "status": s.service.status(r.Context())})
	default:
		failure(w, 400, errors.New("service action must be enable or disable"))
	}
}

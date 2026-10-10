package main

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

//go:embed web/*
var assets embed.FS

type Project struct {
	Name          string            `json:"name"`
	Group         string            `json:"group"`
	Cwd           string            `json:"cwd"`
	Command       string            `json:"command"`
	Args          []string          `json:"args"`
	Env           map[string]string `json:"env"`
	Port          int               `json:"port"`
	MetricsURL    string            `json:"metricsUrl"`
	RequestMetric string            `json:"requestMetric"`
	WatchFiles    *bool             `json:"watchFiles,omitempty"`
}
type Config struct {
	Listen        string    `json:"listen,omitempty"`
	AdminUsername string    `json:"adminUsername,omitempty"`
	AdminPassword string    `json:"adminPassword,omitempty"`
	Projects      []Project `json:"projects"`
	runAsService  bool
}
type Process struct {
	ID    int    `json:"pm_id"`
	Name  string `json:"name"`
	PID   int    `json:"pid"`
	Monit struct {
		CPU    float64 `json:"cpu"`
		Memory uint64  `json:"memory"`
	} `json:"monit"`
	Env struct {
		Status    string          `json:"status"`
		Cwd       string          `json:"pm_cwd"`
		Namespace string          `json:"namespace"`
		Uptime    int64           `json:"pm_uptime"`
		Restarts  int             `json:"restart_time"`
		OutLog    string          `json:"pm_out_log_path"`
		ErrLog    string          `json:"pm_err_log_path"`
		Port      json.RawMessage `json:"PORT"`
		Args      []string        `json:"args"`
	} `json:"pm2_env"`
}
type View struct {
	Project
	Status       string    `json:"status"`
	Instances    []Process `json:"instances"`
	CPU          float64   `json:"cpu"`
	Memory       uint64    `json:"memory"`
	Requests     *float64  `json:"requestsPerSecond"`
	MetricsError string    `json:"metricsError,omitempty"`
	Managed      bool      `json:"managed"`
}
type sample struct {
	value float64
	at    time.Time
}
type server struct {
	mu        sync.Mutex
	config    Config
	path, pm2 string
	run       func(context.Context, ...string) ([]byte, error)
	samples   map[string]sample
	client    *http.Client
	service   *serviceManager
	handoff   chan struct{}
}

func main() {
	listen := flag.String("listen", defaultListen, "HTTP listen address (overrides saved configuration)")
	config := flag.String("config", "projects.json", "project configuration")
	pm2 := flag.String("pm2", "pm2", "PM2 executable")
	nonInteractive := flag.Bool("non-interactive", false, "require existing configuration without terminal prompts")
	flag.Parse()
	listenOverride := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "listen" {
			listenOverride = true
		}
	})
	cfg, address, err := prepareConfig(*config, *listen, listenOverride, *nonInteractive, os.Stdin, os.Stdout)
	if err != nil {
		log.Fatal(err)
	}
	manager, serviceErr := newServiceManager(*config, *pm2, *listen, listenOverride)
	if cfg.runAsService {
		if serviceErr == nil {
			serviceErr = manager.enable(context.Background())
		}
		if serviceErr == nil {
			serviceErr = manager.start(context.Background())
		}
		if serviceErr == nil {
			fmt.Printf("Dashboard is running as a user service at http://%s.\n", address)
			return
		}
		log.Printf("Could not run as a service: %v. Continuing in the foreground; configure the service later in Configuration.", serviceErr)
	}
	s := &server{path: *config, pm2: *pm2, samples: map[string]sample{}, client: &http.Client{Timeout: 3 * time.Second}, service: manager, handoff: make(chan struct{}, 1)}
	s.config = cfg
	s.run = s.command
	h := &http.Server{Addr: address, Handler: s.routes(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 90 * time.Second, IdleTimeout: 60 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	shutdown := make(chan bool, 1)
	go func() {
		asService := false
		select {
		case <-ctx.Done():
		case <-s.handoff:
			asService = true
		}
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = h.Shutdown(c)
		shutdown <- asService
	}()
	log.Printf("PM2 Monitor: http://%s (PM2 user: current OS user)", address)
	if err = h.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
	if <-shutdown {
		if err = manager.start(context.Background()); err != nil {
			log.Fatal("Could not start user service: ", err)
		}
		log.Print("Dashboard handed over to the user service.")
	}
}
func (s *server) command(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, s.pm2, args...)
	c.Env = append(os.Environ(), "NO_COLOR=1")
	out, err := c.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("pm2 %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return out, nil
}
func (s *server) list(ctx context.Context) ([]Process, error) {
	out, err := s.run(ctx, "jlist")
	if err != nil {
		return nil, err
	}
	// PM2 may prepend daemon startup notices to the JSON array.
	start := strings.Index(string(out), "[{\"")
	if start < 0 {
		start = strings.Index(string(out), "[]")
	}
	if start < 0 {
		return nil, errors.New("PM2 returned no JSON process list")
	}
	var p []Process
	err = json.NewDecoder(strings.NewReader(string(out[start:]))).Decode(&p)
	return p, err
}
func validate(c Config) error {
	if err := validateAdmin(c.AdminUsername, c.AdminPassword); err != nil {
		return err
	}
	if c.Listen != "" {
		if err := validateListen(c.Listen); err != nil {
			return err
		}
	}
	names := map[string]bool{}
	for _, p := range c.Projects {
		if p.Name == "" || len(p.Name) > 100 || strings.HasPrefix(p.Name, "-") || strings.ContainsAny(p.Name, "/\\\x00\n\r") {
			return errors.New("invalid project name")
		}
		if names[p.Name] {
			return fmt.Errorf("duplicate name: %s", p.Name)
		}
		names[p.Name] = true
		if !filepath.IsAbs(p.Cwd) {
			return fmt.Errorf("%s: cwd must be absolute", p.Name)
		}
		if p.Command == "" || strings.ContainsRune(p.Command, 0) {
			return fmt.Errorf("%s: command is required", p.Name)
		}
		if p.Port < 0 || p.Port > 65535 {
			return errors.New("invalid port")
		}
		if p.MetricsURL != "" {
			u, e := url.Parse(p.MetricsURL)
			if e != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
				return errors.New("invalid metrics URL")
			}
		}
	}
	return nil
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func failure(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
func (s *server) authorized(r *http.Request, username, password string) bool {
	u, p, ok := r.BasicAuth()
	if !ok || username == "" || password == "" {
		return false
	}
	userOK := subtle.ConstantTimeCompare([]byte(u), []byte(username))
	passwordOK := subtle.ConstantTimeCompare([]byte(p), []byte(password))
	return userOK&passwordOK == 1
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if d.Decode(&struct{}{}) != io.EOF {
		return errors.New("expected one JSON value")
	}
	return nil
}
func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/config", s.handleConfig)
	mux.HandleFunc("/api/project", s.handleProject)
	mux.HandleFunc("/api/discover", s.handleDiscover)
	mux.HandleFunc("/api/browse", s.handleBrowse)
	mux.HandleFunc("/api/projects", s.handleProjects)
	mux.HandleFunc("/api/action", s.handleAction)
	mux.HandleFunc("/api/logs", s.handleLogs)
	mux.HandleFunc("/api/service", s.handleService)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		path := "web" + r.URL.Path
		if r.URL.Path == "/" {
			path = "web/index.html"
		}
		b, e := assets.ReadFile(path)
		if e != nil {
			http.NotFound(w, r)
			return
		}
		switch filepath.Ext(path) {
		case ".html":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
		case ".js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		case ".css":
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
		}
		_, _ = w.Write(b)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; frame-ancestors 'none'")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			s.mu.Lock()
			username, password := s.config.AdminUsername, s.config.AdminPassword
			s.mu.Unlock()
			if !s.authorized(r, username, password) {
				failure(w, 401, errors.New("admin username and password required"))
				return
			}
			if origin := r.Header.Get("Origin"); origin != "" {
				u, e := url.Parse(origin)
				if e != nil || u.Host != r.Host {
					failure(w, 403, errors.New("cross-origin request rejected"))
					return
				}
			}
			if r.Method != "GET" && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
				failure(w, 415, errors.New("application/json required"))
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}
func (s *server) handleConfig(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch r.Method {
	case "GET":
		writeJSON(w, 200, s.config)
	case "PUT":
		var c Config
		if e := decode(w, r, &c); e != nil {
			failure(w, 400, e)
			return
		}
		if c.Listen == "" {
			c.Listen = s.config.Listen
		}
		if c.AdminUsername == "" && c.AdminPassword == "" {
			c.AdminUsername, c.AdminPassword = s.config.AdminUsername, s.config.AdminPassword
		}
		if e := validate(c); e != nil {
			failure(w, 400, e)
			return
		}
		if e := saveConfig(s.path, c); e != nil {
			failure(w, 500, e)
			return
		}
		s.config = c
		s.samples = map[string]sample{}
		writeJSON(w, 200, c)
	default:
		w.WriteHeader(405)
	}
}
func (s *server) handleProjects(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		w.WriteHeader(405)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ps, e := s.list(r.Context())
	if e != nil {
		failure(w, 502, e)
		return
	}
	views := []View{}
	known := map[string]bool{}
	for _, p := range s.config.Projects {
		v := View{Project: p, Status: "not_registered", Managed: true, Instances: []Process{}}
		for _, proc := range ps {
			if proc.Name == p.Name {
				v.Instances = append(v.Instances, proc)
				v.CPU += proc.Monit.CPU
				v.Memory += proc.Monit.Memory
				if v.Status == "not_registered" {
					v.Status = proc.Env.Status
				} else if v.Status != proc.Env.Status {
					v.Status = "mixed"
				}
			}
		}
		if p.MetricsURL != "" && v.Status == "online" {
			v.Requests, v.MetricsError = s.requestRate(r.Context(), p)
		} else {
			delete(s.samples, p.Name)
		}
		views = append(views, v)
		known[p.Name] = true
	}
	for _, p := range ps {
		if !known[p.Name] {
			views = append(views, View{Project: Project{Name: p.Name, Group: p.Env.Namespace, Cwd: p.Env.Cwd, Port: envPort(p.Env.Port)}, Status: p.Env.Status, Instances: []Process{p}, CPU: p.Monit.CPU, Memory: p.Monit.Memory})
		}
	}
	writeJSON(w, 200, map[string]any{"projects": views, "updatedAt": time.Now()})
}
func envPort(b json.RawMessage) int {
	var s string
	if json.Unmarshal(b, &s) == nil {
		n, _ := strconv.Atoi(s)
		return n
	}
	var n int
	_ = json.Unmarshal(b, &n)
	return n
}
func (s *server) requestRate(ctx context.Context, p Project) (*float64, string) {
	req, e := http.NewRequestWithContext(ctx, "GET", p.MetricsURL, nil)
	if e != nil {
		return nil, e.Error()
	}
	resp, e := s.client.Do(req)
	if e != nil {
		return nil, e.Error()
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Sprintf("metrics HTTP %d", resp.StatusCode)
	}
	b, e := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if e != nil {
		return nil, e.Error()
	}
	metric := p.RequestMetric
	if metric == "" {
		metric = "http_requests_total"
	}
	total, found := parseCounter(string(b), metric)
	if !found {
		return nil, "counter not found: " + metric
	}
	now := time.Now()
	old, ok := s.samples[p.Name]
	s.samples[p.Name] = sample{total, now}
	if !ok || total < old.value {
		return nil, ""
	}
	rate := (total - old.value) / now.Sub(old.at).Seconds()
	return &rate, ""
}
func parseCounter(body, metric string) (float64, bool) {
	total := 0.0
	found := false
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		end := strings.IndexAny(line, "{ \t")
		if end < 0 || line[:end] != metric {
			continue
		}
		rest := line[end:]
		if rest[0] == '{' {
			close := strings.LastIndex(rest, "}")
			if close < 0 {
				continue
			}
			rest = rest[close+1:]
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		v, e := strconv.ParseFloat(fields[0], 64)
		if e == nil && v >= 0 && v < 1e300 {
			total += v
			found = true
		}
	}
	return total, found
}

type actionResult struct {
	Name  string `json:"name"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

func (s *server) handleAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		w.WriteHeader(405)
		return
	}
	var q struct {
		Action string  `json:"action"`
		Name   string  `json:"name"`
		Group  *string `json:"group"`
	}
	if e := decode(w, r, &q); e != nil {
		failure(w, 400, e)
		return
	}
	if q.Action != "start" && q.Action != "stop" && q.Action != "restart" {
		failure(w, 400, errors.New("invalid action"))
		return
	}
	if (q.Name == "") == (q.Group == nil) {
		failure(w, 400, errors.New("select name or group"))
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	selected := []Project{}
	for _, p := range s.config.Projects {
		if (q.Group != nil && p.Group == *q.Group) || (q.Name != "" && p.Name == q.Name) {
			selected = append(selected, p)
		}
	}
	if len(selected) == 0 {
		failure(w, 404, errors.New("managed project not found"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 75*time.Second)
	defer cancel()
	ps, e := s.list(ctx)
	if e != nil {
		failure(w, 502, e)
		return
	}
	if q.Action == "stop" {
		for i, j := 0, len(selected)-1; i < j; i, j = i+1, j-1 {
			selected[i], selected[j] = selected[j], selected[i]
		}
	}
	results := []actionResult{}
	for index, p := range selected {
		ids := []int{}
		for _, proc := range ps {
			if proc.Name == p.Name {
				ids = append(ids, proc.ID)
			}
		}
		var err error
		if len(ids) == 0 {
			if q.Action != "stop" {
				err = s.start(ctx, p)
			}
		} else if q.Action != "stop" && (p.WatchFiles != nil || filepath.Base(p.Command) == "dotnet") {
			// Apply the selected launch mode to existing records without deleting them.
			desired, modeErr := pm2LaunchProject(p)
			err = modeErr
			update := q.Action == "restart"
			for _, proc := range ps {
				if proc.Name == p.Name && !slices.Equal(proc.Env.Args, desired.Args) {
					update = true
				}
			}
			if err == nil && update {
				err = s.launch(ctx, p, "restart")
			} else if err == nil {
				for _, id := range ids {
					_, instanceErr := s.run(ctx, q.Action, strconv.Itoa(id))
					err = errors.Join(err, instanceErr)
					if instanceErr != nil {
						break
					}
				}
			}
		} else {
			for _, id := range ids {
				_, instanceErr := s.run(ctx, q.Action, strconv.Itoa(id))
				err = errors.Join(err, instanceErr)
				if instanceErr != nil && q.Action != "stop" {
					break
				}
			}
		}
		result := actionResult{Name: p.Name, OK: err == nil}
		if err != nil {
			result.Error = err.Error()
		}
		results = append(results, result)
		// Do not launch dependent projects after an earlier start failure.
		if err != nil && q.Action != "stop" {
			for _, skipped := range selected[index+1:] {
				results = append(results, actionResult{Name: skipped.Name, Error: "skipped after earlier project failure"})
			}
			break
		}
	}
	writeJSON(w, 200, map[string]any{"results": results})
}
func (s *server) start(ctx context.Context, p Project) error {
	return s.launch(ctx, p, "start")
}
func (s *server) launch(ctx context.Context, p Project, action string) error {
	var err error
	p, err = pm2LaunchProject(p)
	if err != nil {
		return err
	}
	if st, e := os.Stat(p.Cwd); e != nil || !st.IsDir() {
		return fmt.Errorf("working directory unavailable: %s", p.Cwd)
	}
	group := p.Group
	if group == "" {
		group = "default"
	}
	launchEnv := map[string]string{}
	for k, v := range p.Env {
		launchEnv[k] = v
	}
	// PM2's ecosystem schema drops these runtime keys at the top level.
	// Its restart API merges environment keys into the runtime configuration.
	launchEnv["pm_exec_path"] = p.Command
	launchEnv["pm_cwd"] = p.Cwd
	launchEnv["exec_interpreter"] = "none"
	b, e := json.Marshal(map[string]any{"apps": []any{map[string]any{"name": p.Name, "namespace": group, "cwd": p.Cwd, "script": p.Command, "args": p.Args, "env": launchEnv, "interpreter": "none", "autorestart": false, "exec_mode": "fork"}}})
	if e != nil {
		return e
	}
	f, e := os.CreateTemp("", "pm2-monitor-*.json")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if _, e = f.Write(b); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	_, e = s.run(ctx, action, f.Name())
	return e
}
func tail(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	f, e := os.Open(path)
	if e != nil {
		return "", e
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil {
		return "", e
	}
	if !st.Mode().IsRegular() {
		return "", errors.New("log is not a regular file")
	}
	offset := st.Size() - 64*1024
	if offset < 0 {
		offset = 0
	}
	_, e = f.Seek(offset, io.SeekStart)
	if e != nil {
		return "", e
	}
	b, e := io.ReadAll(io.LimitReader(f, 64*1024))
	if offset > 0 {
		if i := strings.IndexByte(string(b), '\n'); i >= 0 {
			b = b[i+1:]
		}
	}
	return string(b), e
}
func (s *server) handleLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method == "DELETE" {
		s.handleClearLogs(w, r)
		return
	}
	if r.Method != "GET" {
		w.WriteHeader(405)
		return
	}
	id, e := strconv.Atoi(r.URL.Query().Get("id"))
	if e != nil {
		failure(w, 400, e)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ps, e := s.list(r.Context())
	if e != nil {
		failure(w, 502, e)
		return
	}
	for _, p := range ps {
		if p.ID == id {
			out, oe := tail(p.Env.OutLog)
			errout, ee := tail(p.Env.ErrLog)
			notes := []string{}
			if oe != nil {
				notes = append(notes, oe.Error())
			}
			if ee != nil {
				notes = append(notes, ee.Error())
			}
			writeJSON(w, 200, map[string]any{"stdout": out, "stderr": errout, "warnings": notes})
			return
		}
	}
	failure(w, 404, errors.New("process not found"))
}

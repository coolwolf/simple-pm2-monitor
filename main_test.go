package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func testServer(t *testing.T) *server {
	t.Helper()
	return &server{path: filepath.Join(t.TempDir(), "projects.json"), config: Config{AdminUsername: "test-admin", AdminPassword: "test-password", Projects: []Project{}}, samples: map[string]sample{}, client: &http.Client{Timeout: time.Second}, run: func(context.Context, ...string) ([]byte, error) { return []byte("[]"), nil }}
}
func call(s *server, method, path, body, authorization, origin string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Host = "localhost"
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	} else {
		req.SetBasicAuth("test-admin", "test-password")
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, req)
	return w
}
func project(t *testing.T, name string) Project {
	t.Helper()
	return Project{Name: name, Group: "shop", Cwd: t.TempDir(), Command: "/usr/bin/dotnet", Args: []string{"run"}, Env: map[string]string{"MODE": "dev"}}
}
func TestAccessControls(t *testing.T) {
	s := testServer(t)
	for _, tc := range []struct {
		username, password, origin string
		authenticated              bool
		code                       int
	}{
		{"", "", "", false, 401},
		{"test-admin", "wrong", "", true, 401},
		{"test-admin", "test-password", "http://evil.test", true, 403},
		{"test-admin", "test-password", "http://localhost", true, 200},
	} {
		req := httptest.NewRequest("GET", "/api/config", nil)
		req.Host = "localhost"
		if tc.authenticated {
			req.SetBasicAuth(tc.username, tc.password)
		}
		if tc.origin != "" {
			req.Header.Set("Origin", tc.origin)
		}
		w := httptest.NewRecorder()
		s.routes().ServeHTTP(w, req)
		if w.Code != tc.code {
			t.Fatalf("status %d: %s", w.Code, w.Body)
		}
	}
	w := call(s, "POST", "/api/action", `{"action":"stop","name":"foo"}`, "", "http://evil.test")
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	req := httptest.NewRequest("PUT", "/api/config", strings.NewReader(`{"projects":[]}`))
	req.Host = "localhost"
	req.SetBasicAuth("test-admin", "test-password")
	w = httptest.NewRecorder()
	s.routes().ServeHTTP(w, req)
	if w.Code != 415 {
		t.Fatal(w.Code)
	}
}
func TestConfigPersistenceAndValidation(t *testing.T) {
	s := testServer(t)
	p := project(t, "api")
	b, _ := json.Marshal(Config{Projects: []Project{p}})
	w := call(s, "PUT", "/api/config", string(b), "", "")
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	saved, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatal(err)
	}
	var c Config
	if err = json.Unmarshal(saved, &c); err != nil || len(c.Projects) != 1 {
		t.Fatal(string(saved))
	}
	st, _ := os.Stat(s.path)
	if st.Mode().Perm() != 0600 {
		t.Fatal(st.Mode())
	}
	for _, body := range []string{`{"projects":[{"name":"bad","cwd":"relative","command":"dotnet"}]}`, `{"projects":[],"extra":1}`, `{"projects":[]} {}`} {
		if w = call(s, "PUT", "/api/config", body, "", ""); w.Code != 400 {
			t.Fatal(w.Code, w.Body)
		}
	}
	if len(s.config.Projects) != 1 {
		t.Fatal("invalid save mutated config")
	}
	if err = validate(Config{Projects: []Project{p, p}}); err == nil {
		t.Fatal("duplicate accepted")
	}
}
func TestGroupOrderAndNumericTargets(t *testing.T) {
	for _, action := range []string{"start", "restart", "stop"} {
		t.Run(action, func(t *testing.T) {
			s := testServer(t)
			s.config.Projects = []Project{project(t, "api"), project(t, "web")}
			for i := range s.config.Projects {
				s.config.Projects[i].Command = "/usr/bin/node"
			}
			calls := []string{}
			s.run = func(_ context.Context, args ...string) ([]byte, error) {
				if args[0] == "jlist" {
					return []byte(`[{"name":"api","pm_id":3},{"name":"web","pm_id":9}]`), nil
				}
				calls = append(calls, strings.Join(args, " "))
				return nil, nil
			}
			w := call(s, "POST", "/api/action", `{"action":"`+action+`","group":"shop"}`, "", "")
			if w.Code != 200 {
				t.Fatal(w.Body)
			}
			want := []string{action + " 3", action + " 9"}
			if action == "stop" {
				want = []string{"stop 9", "stop 3"}
			}
			if !reflect.DeepEqual(calls, want) {
				t.Fatalf("got %v want %v", calls, want)
			}
		})
	}
}
func TestStartEcosystemAndNoShell(t *testing.T) {
	s := testServer(t)
	p := project(t, "api")
	p.Args = []string{"run", "literal; $(touch /tmp/never)"}
	s.config.Projects = []Project{p}
	var temp string
	s.run = func(_ context.Context, args ...string) ([]byte, error) {
		if args[0] == "jlist" {
			return []byte("[PM2] Starting daemon\n[]"), nil
		}
		if args[0] != "start" {
			t.Fatal(args)
		}
		temp = args[1]
		b, e := os.ReadFile(temp)
		if e != nil {
			t.Fatal(e)
		}
		var cfg struct {
			Apps []struct {
				Name, Cwd, Script, Interpreter string
				Args                           []string
				Autorestart                    bool
			}
		}
		if e = json.Unmarshal(b, &cfg); e != nil {
			t.Fatal(e)
		}
		app := cfg.Apps[0]
		if app.Name != p.Name || app.Cwd != p.Cwd || filepath.Base(app.Script) != "env" || app.Interpreter != "none" || app.Autorestart || !reflect.DeepEqual(app.Args, append([]string{"-u", "version", p.Command}, p.Args...)) {
			t.Fatalf("bad ecosystem: %s", b)
		}
		return nil, nil
	}
	w := call(s, "POST", "/api/action", `{"action":"start","name":"api"}`, "", "")
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	if _, e := os.Stat(temp); !os.IsNotExist(e) {
		t.Fatal("temporary file not removed")
	}
}
func TestFailureStopsDependentsAndStopContinues(t *testing.T) {
	for _, action := range []string{"restart", "stop"} {
		s := testServer(t)
		s.config.Projects = []Project{project(t, "api"), project(t, "web")}
		calls := 0
		s.run = func(_ context.Context, args ...string) ([]byte, error) {
			if args[0] == "jlist" {
				return []byte(`[{"name":"api","pm_id":1},{"name":"web","pm_id":2}]`), nil
			}
			calls++
			return nil, errors.New("failure")
		}
		w := call(s, "POST", "/api/action", `{"action":"`+action+`","group":"shop"}`, "", "")
		var body struct{ Results []actionResult }
		if e := json.Unmarshal(w.Body.Bytes(), &body); e != nil {
			t.Fatal(e)
		}
		if len(body.Results) != 2 || body.Results[0].OK || body.Results[1].OK {
			t.Fatal(w.Body)
		}
		want := 1
		if action == "stop" {
			want = 2
		}
		if calls != want {
			t.Fatal(calls)
		}
	}
}
func TestUnmanagedCannotBeControlled(t *testing.T) {
	s := testServer(t)
	w := call(s, "POST", "/api/action", `{"action":"stop","name":"foreign"}`, "", "")
	if w.Code != 404 {
		t.Fatal(w.Body)
	}
}
func TestProcessViewHidesEnvironmentAndAggregates(t *testing.T) {
	s := testServer(t)
	s.config.Projects = []Project{project(t, "api")}
	s.run = func(context.Context, ...string) ([]byte, error) {
		return []byte(`[{"name":"api","pm_id":0,"pid":123,"monit":{"cpu":2.5,"memory":100},"pm2_env":{"status":"online","SECRET":"do-not-expose"}},{"name":"api","pm_id":1,"monit":{"cpu":4,"memory":200},"pm2_env":{"status":"stopped"}},{"name":"outside","pm_id":2,"pm2_env":{"status":"online","pm_cwd":"/srv","PORT":"3000"}}]`), nil
	}
	w := call(s, "GET", "/api/projects", "", "", "")
	if strings.Contains(w.Body.String(), "do-not-expose") {
		t.Fatal("PM2 environment leaked")
	}
	var body struct{ Projects []View }
	if e := json.Unmarshal(w.Body.Bytes(), &body); e != nil {
		t.Fatal(e)
	}
	v := body.Projects[0]
	if v.CPU != 6.5 || v.Memory != 300 || v.Status != "mixed" || len(v.Instances) != 2 {
		t.Fatal(v)
	}
	if body.Projects[1].Managed || body.Projects[1].Port != 3000 {
		t.Fatal(body.Projects[1])
	}
}
func TestRequestCounterRateAndReset(t *testing.T) {
	body := "# TYPE http_requests_total counter\nhttp_requests_total{path=\"/with spaces\"} 5\nhttp_requests_total{path=\"/api\"} 7\nhttp_requests_total_created 999\n"
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
	defer endpoint.Close()
	s := testServer(t)
	p := Project{Name: "api", MetricsURL: endpoint.URL}
	rate, e := s.requestRate(context.Background(), p)
	if e != "" || rate != nil {
		t.Fatal(rate, e)
	}
	s.samples[p.Name] = sample{value: 2, at: time.Now().Add(-time.Second)}
	rate, e = s.requestRate(context.Background(), p)
	if e != "" || rate == nil || *rate < 8 || *rate > 11 {
		t.Fatal(rate, e)
	}
	s.samples[p.Name] = sample{value: 100, at: time.Now().Add(-time.Second)}
	rate, e = s.requestRate(context.Background(), p)
	if e != "" || rate != nil {
		t.Fatal(rate, e)
	}
	total, found := parseCounter("http_requests_total NaN\nhttp_requests_total +Inf\nhttp_requests_total -1", "http_requests_total")
	if found || total != 0 {
		t.Fatal(total, found)
	}
}
func TestLogsAreBoundedAndCannotChoosePath(t *testing.T) {
	s := testServer(t)
	path := filepath.Join(t.TempDir(), "out.log")
	if e := os.WriteFile(path, []byte(strings.Repeat("line\n", 20000)), 0600); e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal([]Process{{ID: 7}})
	var ps []Process
	_ = json.Unmarshal(b, &ps)
	ps[0].Env.OutLog = path
	b, _ = json.Marshal(ps)
	s.run = func(context.Context, ...string) ([]byte, error) { return b, nil }
	w := call(s, "GET", "/api/logs?id=7", "", "", "")
	if w.Code != 200 {
		t.Fatal(w.Body)
	}
	var result struct{ Stdout string }
	_ = json.Unmarshal(w.Body.Bytes(), &result)
	if len(result.Stdout) > 65536 || !strings.HasSuffix(result.Stdout, "line\n") {
		t.Fatal(len(result.Stdout))
	}
	w = call(s, "GET", "/api/logs?id=99&path=/etc/passwd", "", "", "")
	if w.Code != 404 {
		t.Fatal(w.Code)
	}
}

func TestUnauthenticatedExternalHostRejected(t *testing.T) {
	s := testServer(t)
	r := httptest.NewRequest("GET", "http://attacker.test/api/config", nil)
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
}

// Opt-in integration: uses a disposable PM2_HOME and never touches the user's daemon.
func TestPM2Integration(t *testing.T) {
	if os.Getenv("PM2_MONITOR_INTEGRATION") != "1" {
		t.Skip("set PM2_MONITOR_INTEGRATION=1 to exercise an isolated PM2 daemon")
	}
	t.Setenv("PM2_HOME", t.TempDir())
	s := testServer(t)
	s.pm2 = "pm2"
	s.run = s.command
	defer func() { _, _ = s.command(context.Background(), "kill") }()
	p := project(t, "monitor-integration")
	p.Command = "/usr/bin/node"
	p.Args = []string{"app.js"}
	if e := os.WriteFile(filepath.Join(p.Cwd, "app.js"), []byte("console.log('integration-ready'); setInterval(()=>{}, 1000);"), 0600); e != nil {
		t.Fatal(e)
	}
	s.config.Projects = []Project{p}
	for _, action := range []string{"start", "stop", "start", "restart"} {
		w := call(s, "POST", "/api/action", `{"action":"`+action+`","name":"monitor-integration"}`, "", "")
		var result struct{ Results []actionResult }
		if e := json.Unmarshal(w.Body.Bytes(), &result); e != nil {
			t.Fatal(e)
		}
		if w.Code != 200 || len(result.Results) != 1 || !result.Results[0].OK {
			t.Fatal(w.Code, w.Body)
		}
		ps, e := s.list(context.Background())
		if e != nil || len(ps) != 1 {
			t.Fatal(ps, e)
		}
		want := "online"
		if action == "stop" {
			want = "stopped"
		}
		if ps[0].Env.Status != want || ps[0].Env.Cwd != p.Cwd {
			t.Fatal(ps[0])
		}
	}
}

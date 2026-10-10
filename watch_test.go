package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestDotnetWatchModesPreserveArgumentsAndLegacyConfiguration(t *testing.T) {
	off, on := false, true
	original := Project{Command: "/usr/bin/dotnet", Args: []string{"watch", "--project", "/srv/api.csproj", "run", "--launch-profile", "http"}}
	legacy, err := projectLaunchMode(original)
	if err != nil || !reflect.DeepEqual(legacy, original) {
		t.Fatal(legacy, err)
	}
	original.WatchFiles = &off
	plain, err := projectLaunchMode(original)
	if err != nil || !reflect.DeepEqual(plain.Args, []string{"run", "--project", "/srv/api.csproj", "--launch-profile", "http"}) {
		t.Fatal(plain, err)
	}
	if original.Args[0] != "watch" {
		t.Fatal("mutated saved arguments")
	}
	plain.WatchFiles = &on
	watched, err := projectLaunchMode(plain)
	if err != nil || watched.Args[0] != "watch" || watched.Args[1] != "run" {
		t.Fatal(watched, err)
	}
	unsupported := Project{Command: "/usr/bin/dotnet", Args: []string{"watch", "test"}, WatchFiles: &off}
	if _, err := projectLaunchMode(unsupported); err == nil {
		t.Fatal("silently changed a custom watch command")
	}
}

func TestNextWatchModeSelectsScriptsAndEnvironment(t *testing.T) {
	root := t.TempDir()
	fixture(t, root, "package.json", `{"dependencies":{"next":"*"},"scripts":{"dev":"next dev","start":"next start"}}`)
	off, on := false, true
	p := Project{Cwd: root, Command: "/usr/bin/npm", Args: []string{"run", "dev", "--", "--port", "3001"}, Env: map[string]string{"NODE_ENV": "development", "CUSTOM": "keep"}, WatchFiles: &off}
	launch, err := projectLaunchMode(p)
	if err != nil || !reflect.DeepEqual(launch.Args, []string{"run", "start", "--", "--port", "3001"}) || launch.Env["NODE_ENV"] != "production" || launch.Env["CUSTOM"] != "keep" {
		t.Fatal(launch, err)
	}
	if p.Env["NODE_ENV"] != "development" || p.Args[1] != "dev" {
		t.Fatal("mutated saved config")
	}
	launch.WatchFiles = &on
	launch, err = projectLaunchMode(launch)
	if err != nil || launch.Args[1] != "dev" || launch.Env["NODE_ENV"] != "development" {
		t.Fatal(launch, err)
	}
	fixture(t, root, "package.json", `{"dependencies":{"next":"*"},"scripts":{"dev":"next dev"}}`)
	if _, err = projectLaunchMode(p); err == nil {
		t.Fatal("accepted missing production script")
	}
}

func TestWatchSettingPersistsAndUpdatesExistingRecords(t *testing.T) {
	s := testServer(t)
	p := project(t, "api")
	p.Args = []string{"watch", "run", "--urls", "http://localhost:5181"}
	s.config.Projects = []Project{p}
	off := false
	p.WatchFiles = &off
	body, _ := json.Marshal(map[string]any{"name": "api", "project": p})
	w := call(s, "PUT", "/api/project", string(body), "", "")
	if w.Code != 200 || s.config.Projects[0].WatchFiles == nil || *s.config.Projects[0].WatchFiles {
		t.Fatal(w.Code, w.Body)
	}
	saved, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatal(err)
	}
	var stored Config
	json.Unmarshal(saved, &stored)
	if stored.Projects[0].WatchFiles == nil || *stored.Projects[0].WatchFiles {
		t.Fatal("watch setting not saved")
	}
	for _, action := range []string{"start", "restart", "stop"} {
		calls := 0
		s.run = func(_ context.Context, args ...string) ([]byte, error) {
			if args[0] == "jlist" {
				return []byte(`[{"name":"api","pm_id":7,"pm2_env":{"args":["watch","run","--urls","http://localhost:5181"]}}]`), nil
			}
			calls++
			if action == "stop" {
				if !reflect.DeepEqual(args, []string{"stop", "7"}) {
					t.Fatal(args)
				}
				return nil, nil
			}
			if args[0] != "restart" {
				t.Fatal(args)
			}
			data, err := os.ReadFile(args[1])
			if err != nil {
				t.Fatal(err)
			}
			var config struct {
				Apps []struct {
					Args []string
					Name string
				}
			}
			json.Unmarshal(data, &config)
			if len(config.Apps) != 1 || config.Apps[0].Name != "api" || !reflect.DeepEqual(config.Apps[0].Args, []string{"-u", "version", p.Command, "run", "--urls", "http://localhost:5181"}) {
				t.Fatal(string(data))
			}
			return nil, nil
		}
		w = call(s, "POST", "/api/action", `{"action":"`+action+`","name":"api"}`, "", "")
		if w.Code != 200 || calls != 1 {
			t.Fatal(w.Code, w.Body, calls)
		}
	}
}

func TestPM2WatchModeIntegration(t *testing.T) {
	if os.Getenv("PM2_MONITOR_INTEGRATION") != "1" {
		t.Skip("enable isolated PM2 integration")
	}
	t.Setenv("PM2_HOME", t.TempDir())
	s := testServer(t)
	s.pm2 = "pm2"
	s.run = s.command
	defer func() { s.command(context.Background(), "kill") }()
	p := project(t, "watch-integration")
	p.Command = filepath.Join(p.Cwd, "dotnet")
	if err := os.WriteFile(p.Command, []byte("#!/bin/sh\nexec /usr/bin/node runner.js \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p.Cwd, "runner.js"), []byte("require('node:fs').writeFileSync('environment.json', JSON.stringify({version:process.env.version,args:process.argv.slice(2)})); setInterval(()=>{},1000);"), 0600); err != nil {
		t.Fatal(err)
	}
	// Reproduce an existing record created before the environment wrapper.
	if _, err := s.command(context.Background(), "start", p.Command, "--name", p.Name, "--interpreter", "none", "--no-autorestart", "--", "watch", "run"); err != nil {
		t.Fatal(err)
	}
	initial, err := s.list(context.Background())
	if err != nil || len(initial) != 1 {
		t.Fatal(initial, err)
	}
	p.Args = []string{"watch", "run"}
	off, on := false, true
	p.WatchFiles = &off
	s.config.Projects = []Project{p}
	oldID := initial[0].ID
	for _, test := range []struct {
		action string
		watch  bool
		arg    string
	}{{"start", off, "run"}, {"restart", on, "watch"}, {"stop", on, "watch"}, {"start", off, "run"}, {"restart", off, "run"}} {
		s.config.Projects[0].WatchFiles = &test.watch
		w := call(s, "POST", "/api/action", `{"action":"`+test.action+`","name":"watch-integration"}`, "", "")
		var result struct{ Results []actionResult }
		json.Unmarshal(w.Body.Bytes(), &result)
		if w.Code != 200 || len(result.Results) != 1 || !result.Results[0].OK {
			t.Fatal(w.Code, w.Body)
		}
		ps, err := s.list(context.Background())
		if err != nil || len(ps) != 1 {
			t.Fatal(ps, err)
		}

		if ps[0].ID != oldID || len(ps[0].Env.Args) < 4 || ps[0].Env.Args[3] != test.arg {
			t.Fatal(ps[0])
		}
		if test.action != "stop" {
			clean := false
			for attempt := 0; attempt < 40; attempt++ {
				data, _ := os.ReadFile(filepath.Join(p.Cwd, "environment.json"))
				var actual struct {
					Version *string
					Args    []string
				}
				json.Unmarshal(data, &actual)
				if actual.Version == nil && len(actual.Args) > 0 && actual.Args[0] == test.arg {
					clean = true
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
			if !clean {
				data, _ := os.ReadFile(filepath.Join(p.Cwd, "environment.json"))
				out, _ := tail(ps[0].Env.OutLog)
				errLog, _ := tail(ps[0].Env.ErrLog)
				t.Fatalf("migration failed at %s: observation=%s stdout=%s stderr=%s", test.action, data, out, errLog)
			}
		}
	}
}

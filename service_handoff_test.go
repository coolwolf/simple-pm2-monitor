package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Runs two real dashboard processes with a fake user service manager. All
// files and processes are disposable; no real systemd or PM2 unit is touched.
func TestServiceHandoffIntegration(t *testing.T) {
	if os.Getenv("PM2_MONITOR_SERVICE_INTEGRATION") != "1" {
		t.Skip("enable isolated service handover integration")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("Node.js is required for the fake service manager")
	}
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	os.MkdirAll(bin, 0700)
	binary := filepath.Join(bin, "monitor")
	if output, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v: %s", err, output)
	}
	fakeCtl := `#!/usr/bin/node
const fs=require('node:fs'),path=require('node:path'),cp=require('node:child_process');
const root=process.env.SERVICE_TEST_ROOT,args=process.argv.slice(2);
if(args.shift()!=='--user')process.exit(9);
const enabled=path.join(root,'enabled'),pidFile=path.join(root,'service.pid');
switch(args[0]){
 case 'show-environment':break;
 case 'is-enabled':console.log(fs.existsSync(enabled)?'enabled':'disabled');break;
 case 'is-active':console.log(fs.existsSync(pidFile)?'active':'inactive');break;
 case 'daemon-reload':break;
 case 'enable':fs.writeFileSync(enabled,'1');break;
 case 'disable':if(fs.existsSync(enabled))fs.unlinkSync(enabled);break;
 case 'start':{
  const child=cp.spawn(path.join(process.env.XDG_DATA_HOME,'simple-pm2-monitor','simple-pm2-monitor'),['-non-interactive','-config',path.join(root,'projects.json')],{env:{...process.env,DEV_HARBOR_SERVICE:'1'},detached:true,stdio:'ignore'});
  fs.writeFileSync(pidFile,String(child.pid));child.unref();break;
 }
 default:process.exit(8);
}
`
	os.WriteFile(filepath.Join(bin, "systemctl"), []byte(fakeCtl), 0700)
	os.WriteFile(filepath.Join(bin, "pm2"), []byte("#!/bin/sh\nprintf '[]'\n"), 0700)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	config := Config{Listen: address, AdminUsername: "test-admin", AdminPassword: "test-password", Projects: []Project{}}
	configPath := filepath.Join(root, "projects.json")
	if err := saveConfig(configPath, config); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	cmd := exec.Command(binary, "-non-interactive", "-config", configPath)
	cmd.Env = append(os.Environ(), "HOME="+root, "XDG_CONFIG_HOME="+filepath.Join(root, "config"), "XDG_DATA_HOME="+filepath.Join(root, "data"), "PATH="+bin+":"+os.Getenv("PATH"), "SERVICE_TEST_ROOT="+root, "DEV_HARBOR_SERVICE=0", "INVOCATION_ID=")
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	defer func() {
		data, _ := os.ReadFile(filepath.Join(root, "service.pid"))
		if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
			p, _ := os.FindProcess(pid)
			p.Kill()
		}
	}()
	client := &http.Client{Timeout: 2 * time.Second}
	callService := func(method, body string) (serviceStatus, bool, error) {
		request, _ := http.NewRequest(method, "http://"+address+"/api/service", strings.NewReader(body))
		request.SetBasicAuth(config.AdminUsername, config.AdminPassword)
		if method == "POST" {
			request.Header.Set("Content-Type", "application/json")
		}
		response, err := client.Do(request)
		if err != nil {
			return serviceStatus{}, false, err
		}
		defer response.Body.Close()
		data, _ := io.ReadAll(response.Body)
		if response.StatusCode != 200 {
			return serviceStatus{}, false, &serviceTestError{string(data)}
		}
		if method == "POST" {
			var result struct{ Handoff bool }
			err = json.Unmarshal(data, &result)
			return serviceStatus{}, result.Handoff, err
		}
		var status serviceStatus
		err = json.Unmarshal(data, &status)
		return status, false, err
	}
	ready := false
	for attempt := 0; attempt < 50; attempt++ {
		if status, _, err := callService("GET", ""); err == nil && status.Available {
			ready = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ready {
		cmd.Process.Kill()
		cmd.Wait()
		t.Fatal("foreground dashboard did not start", output.String())
	}
	if _, handoff, err := callService("POST", `{"action":"enable"}`); err != nil || !handoff {
		cmd.Process.Kill()
		cmd.Wait()
		t.Fatal("handover request", handoff, err, output.String())
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal("foreground process did not exit cleanly", err, output.String())
	}
	for attempt := 0; attempt < 50; attempt++ {
		if status, _, err := callService("GET", ""); err == nil && status.RunningAsService && status.Active && status.Enabled {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("service did not take over the same HTTP port", output.String())
}

type serviceTestError struct{ message string }

func (e *serviceTestError) Error() string { return e.message }

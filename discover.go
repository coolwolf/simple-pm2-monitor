package main

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type discoveredProject struct {
	Label      string   `json:"label"`
	Kind       string   `json:"kind"`
	Source     string   `json:"source"`
	Target     string   `json:"target"`
	PortSource string   `json:"portSource"`
	Project    Project  `json:"project"`
	Warnings   []string `json:"warnings"`
}
type discovery struct {
	Candidates []discoveredProject `json:"candidates"`
	Warnings   []string            `json:"warnings"`
}

func (s *server) handleDiscover(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		w.WriteHeader(405)
		return
	}
	var q struct {
		Path string `json:"path"`
	}
	if err := decode(w, r, &q); err != nil {
		failure(w, 400, err)
		return
	}
	result, err := discover(q.Path)
	if err != nil {
		failure(w, 400, err)
		return
	}
	writeJSON(w, 200, result)
}
func readProjectFile(path string) ([]byte, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, errors.New("project settings must be a regular file")
	}
	if st.Size() > 2<<20 {
		return nil, errors.New("project settings exceed the 2 MiB size limit")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, (2<<20)+1))
	if len(b) > 2<<20 {
		return nil, errors.New("project settings exceed the 2 MiB size limit")
	}
	return b, err
}
func discover(path string) (discovery, error) {
	result := discovery{Candidates: []discoveredProject{}, Warnings: []string{}}
	if !filepath.IsAbs(path) {
		return result, errors.New("enter an absolute path to a directory or file on the server")
	}
	path = filepath.Clean(path)
	st, err := os.Stat(path)
	if err != nil {
		return result, err
	}
	files := []string{}
	if st.IsDir() {
		count := 0
		err = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				result.Warnings = append(result.Warnings, fmt.Sprintf("%s: %v", p, err))
				return nil
			}
			count++
			if count > 4000 {
				return errors.New("scan stopped at 4000 entries; choose a narrower directory")
			}
			if d.IsDir() && p != path {
				rel, _ := filepath.Rel(path, p)
				if strings.Count(rel, string(filepath.Separator)) >= 4 || strings.HasPrefix(d.Name(), ".") || map[string]bool{"node_modules": true, "bin": true, "obj": true, "vendor": true, "dist": true}[d.Name()] {
					return filepath.SkipDir
				}
			}
			if !d.IsDir() && isProjectFile(p) {
				files = append(files, p)
			}
			return nil
		})
		if err != nil {
			result.Warnings = append(result.Warnings, err.Error())
		}
	} else if isProjectFile(path) {
		files = append(files, path)
	} else {
		return result, errors.New("choose a .sln, .slnx, .csproj/.fsproj/.vbproj, package.json, or project directory")
	}
	seen := map[string]bool{}
	var addFile func(string)
	addFile = func(file string) {
		file = filepath.Clean(file)
		if seen[file] {
			return
		}
		seen[file] = true
		if len(result.Candidates) >= 200 {
			return
		}
		switch strings.ToLower(filepath.Ext(file)) {
		case ".sln", ".slnx":
			refs, e := solutionProjects(file)
			if e != nil {
				result.Warnings = append(result.Warnings, file+": "+e.Error())
				return
			}
			for _, ref := range refs {
				addFile(ref)
			}
		case ".csproj", ".fsproj", ".vbproj":
			candidates, e := discoverDotnet(file)
			if e != nil {
				result.Warnings = append(result.Warnings, file+": "+e.Error())
				return
			}
			result.Candidates = append(result.Candidates, candidates...)
		case ".json":
			candidates, e := discoverNext(file)
			if e != nil {
				result.Warnings = append(result.Warnings, file+": "+e.Error())
				return
			}
			result.Candidates = append(result.Candidates, candidates...)
		}
	}
	sort.Strings(files)
	for _, file := range files {
		addFile(file)
	}
	if len(result.Candidates) > 200 {
		result.Candidates = result.Candidates[:200]
	}
	if len(result.Candidates) == 200 {
		result.Warnings = append(result.Warnings, "Results are limited to 200 options; choose a narrower path.")
	}
	if len(result.Candidates) == 0 {
		result.Warnings = append(result.Warnings, "No runnable projects found. Check the path or add a project manually.")
	}
	return result, nil
}
func isProjectFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".sln", ".slnx", ".csproj", ".fsproj", ".vbproj":
		return true
	}
	return filepath.Base(path) == "package.json"
}

var solutionLine = regexp.MustCompile(`(?m)^\s*Project\([^\r\n]*\)\s*=\s*"[^"]*"\s*,\s*"([^"]+)"`)

func solutionProjects(path string) ([]string, error) {
	b, err := readProjectFile(path)
	if err != nil {
		return nil, err
	}
	refs := []string{}
	if strings.EqualFold(filepath.Ext(path), ".slnx") {
		decoder := xml.NewDecoder(strings.NewReader(string(b)))
		for {
			token, e := decoder.Token()
			if e == io.EOF {
				break
			}
			if e != nil {
				return nil, e
			}
			if el, ok := token.(xml.StartElement); ok && el.Name.Local == "Project" {
				for _, attr := range el.Attr {
					if attr.Name.Local == "Path" {
						refs = append(refs, attr.Value)
					}
				}
			}
		}
	} else {
		for _, m := range solutionLine.FindAllStringSubmatch(string(b), -1) {
			refs = append(refs, m[1])
		}
	}
	paths := []string{}
	for _, ref := range refs {
		ref = strings.ReplaceAll(ref, "\\", "/")
		full := filepath.Join(filepath.Dir(path), filepath.FromSlash(ref))
		if filepath.IsAbs(ref) {
			full = ref
		}
		switch strings.ToLower(filepath.Ext(full)) {
		case ".csproj", ".fsproj", ".vbproj":
			paths = append(paths, full)
		}
	}
	return paths, nil
}
func commandPath(name string) (string, []string) {
	path, err := exec.LookPath(name)
	if err == nil {
		return path, []string{}
	}
	return name, []string{name + " was not found on PATH; check the command path."}
}
func safeProjectName(name string) string {
	name = strings.TrimSpace(strings.TrimPrefix(name, "@"))
	name = strings.NewReplacer("/", "-", "\\", "-", ":", "-", " ", "-").Replace(name)
	name = strings.TrimLeft(name, "-")
	if name == "" {
		name = "project"
	}
	if len(name) > 100 {
		name = name[:100]
	}
	return name
}
func discoverDotnet(path string) ([]discoveredProject, error) {
	b, err := readProjectFile(path)
	if err != nil {
		return nil, err
	}
	var projectXML struct {
		SDK    string `xml:"Sdk,attr"`
		Groups []struct {
			OutputType string `xml:"OutputType"`
		} `xml:"PropertyGroup"`
	}
	if err = xml.Unmarshal(b, &projectXML); err != nil {
		return nil, err
	}
	cwd := filepath.Dir(path)
	launchPath := filepath.Join(cwd, "Properties", "launchSettings.json")
	var launch struct {
		Profiles map[string]struct {
			CommandName string            `json:"commandName"`
			URL         string            `json:"applicationUrl"`
			Env         map[string]string `json:"environmentVariables"`
		} `json:"profiles"`
	}
	warnings := []string{}
	launchBytes, e := readProjectFile(launchPath)
	if e == nil {
		if e = json.Unmarshal(cleanJSONC(launchBytes), &launch); e != nil {
			warnings = append(warnings, "Could not read launchSettings.json: "+e.Error())
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		warnings = append(warnings, e.Error())
	}
	runnable := strings.Contains(strings.ToLower(projectXML.SDK), "web")
	for _, group := range projectXML.Groups {
		if strings.EqualFold(group.OutputType, "Exe") || strings.EqualFold(group.OutputType, "WinExe") {
			runnable = true
		}
	}
	profiles := []string{}
	for name, profile := range launch.Profiles {
		if strings.EqualFold(profile.CommandName, "Project") {
			profiles = append(profiles, name)
			runnable = true
		}
	}
	if !runnable {
		return nil, errors.New("no runnable SDK/OutputType or Project launch profile found (possibly a library)")
	}
	sort.Slice(profiles, func(i, j int) bool {
		if profiles[i] == profiles[j] {
			return false
		}
		if profiles[i] == "http" {
			return true
		}
		if profiles[j] == "http" {
			return false
		}
		return profiles[i] < profiles[j]
	})
	command, notes := commandPath("dotnet")
	warnings = append(warnings, notes...)
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	result := []discoveredProject{}
	if len(profiles) == 0 {
		profiles = append(profiles, "")
	}
	for _, name := range profiles {
		p := Project{Name: safeProjectName(base), Cwd: cwd, Command: command, Args: []string{"watch", "--project", path, "run"}, Env: map[string]string{}, RequestMetric: "http_requests_total"}
		label := base
		source := ""
		notes := append([]string{}, warnings...)
		if name != "" {
			profile := launch.Profiles[name]
			p.Args = append(p.Args, "--launch-profile", name)
			for k, v := range profile.Env {
				p.Env[k] = v
			}
			urls := profile.URL
			if urls == "" {
				urls = p.Env["ASPNETCORE_URLS"]
			}
			if urls == "" {
				urls = p.Env["DOTNET_URLS"]
			}
			p.Port = portFromURLs(urls)
			source = "Properties/launchSettings.json · " + name
			label += " · " + name
		} else {
			p.Args = append(p.Args, "--no-launch-profile")
			p.Env["ASPNETCORE_ENVIRONMENT"] = "Development"
		}
		// The dashboard runs on a server without a desktop browser.
		p.Env["DOTNET_WATCH_SUPPRESS_LAUNCH_BROWSER"] = "1"
		watch := false
		p.WatchFiles = &watch
		p, _ = projectLaunchMode(p)
		if p.Port == 0 {
			environment := p.Env["DOTNET_ENVIRONMENT"]
			if environment == "" {
				environment = p.Env["ASPNETCORE_ENVIRONMENT"]
			}
			if environment == "" {
				environment = "Production"
			}
			p.Port, source = dotnetSettingsPort(cwd, environment)
			if p.Port == 0 {
				notes = append(notes, "Could not determine the port; enter the application port manually.")
				source = ""
			}
		}
		result = append(result, discoveredProject{Label: label, Kind: "dotnet", Source: path, Target: name, PortSource: source, Project: p, Warnings: notes})
	}
	return result, nil
}
func portFromURLs(values string) int {
	https := 0
	for _, value := range strings.Split(values, ";") {
		u, e := url.Parse(strings.TrimSpace(value))
		if e != nil {
			continue
		}
		port := 0
		if u.Port() != "" {
			port, _ = strconv.Atoi(u.Port())
		} else if u.Scheme == "http" {
			port = 80
		} else if u.Scheme == "https" {
			port = 443
		}
		if port < 1 || port > 65535 {
			continue
		}
		if u.Scheme == "http" {
			return port
		}
		if u.Scheme == "https" && https == 0 {
			https = port
		}
	}
	return https
}
func dotnetSettingsPort(cwd, environment string) (int, string) {
	files := []string{"appsettings.json"}
	if environment != "" && !strings.ContainsAny(environment, "/\\") {
		files = append([]string{"appsettings." + environment + ".json"}, files...)
	}
	for _, name := range files {
		b, e := readProjectFile(filepath.Join(cwd, name))
		if e != nil {
			continue
		}
		var config struct {
			URLs    string `json:"Urls"`
			Kestrel struct {
				Endpoints map[string]struct {
					URL string `json:"Url"`
				} `json:"Endpoints"`
			} `json:"Kestrel"`
		}
		if json.Unmarshal(cleanJSONC(b), &config) != nil {
			continue
		}
		keys := []string{}
		for key := range config.Kestrel.Endpoints {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		urls := []string{}
		for _, key := range keys {
			urls = append(urls, config.Kestrel.Endpoints[key].URL)
		}
		if port := portFromURLs(strings.Join(urls, ";")); port != 0 {
			return port, name + " · Kestrel.Endpoints"
		}
		if port := portFromURLs(config.URLs); port != 0 {
			return port, name + " · Urls"
		}
	}
	return 0, ""
}

var nextServer = regexp.MustCompile(`(?:^|[\s;&|])next\s+(dev|start)\b`)
var portFlag = regexp.MustCompile(`(?:^|\s)(?:--port(?:=|\s+)|-p(?:=|\s+)?)([^\s;&|]+)`)
var inlinePort = regexp.MustCompile(`(?:^|\s)PORT=["']?([0-9]+)(?:["']?(?:\s|$))`)

func nextScriptPort(script string) (int, string) {
	if len(nextServer.FindAllStringIndex(script, -1)) > 1 {
		return 0, "script · multiple server commands"
	}
	m := nextServer.FindStringIndex(script)
	if m == nil {
		return 0, ""
	}
	command := script[m[0]:]
	scopeStart := strings.LastIndexAny(script[:m[0]], ";&|") + 1
	prefix := script[scopeStart:m[1]]
	if end := strings.IndexAny(command, ";&|"); end >= 0 {
		command = command[:end]
	}
	if flag := portFlag.FindStringSubmatch(command); flag != nil {
		value := strings.Trim(flag[1], "\"'")
		port, _ := strconv.Atoi(value)
		if port > 0 && port <= 65535 {
			return port, "script · --port/-p"
		}
		return 0, "script · dynamic/invalid port"
	}
	if env := inlinePort.FindStringSubmatch(prefix); env != nil {
		port, _ := strconv.Atoi(env[1])
		if port > 0 && port <= 65535 {
			return port, "script · PORT"
		}
		return 0, "script · invalid PORT"
	}
	if strings.Contains(prefix, "PORT=") {
		return 0, "script · dynamic PORT"
	}
	return 3000, "Next.js default"
}
func discoverNext(path string) ([]discoveredProject, error) {
	b, err := readProjectFile(path)
	if err != nil {
		return nil, err
	}
	var pkg struct {
		Name            string                     `json:"name"`
		Scripts         map[string]string          `json:"scripts"`
		Dependencies    map[string]json.RawMessage `json:"dependencies"`
		DevDependencies map[string]json.RawMessage `json:"devDependencies"`
	}
	if err = json.Unmarshal(b, &pkg); err != nil {
		return nil, err
	}
	_, nextDep := pkg.Dependencies["next"]
	_, nextDevDep := pkg.DevDependencies["next"]
	isNext := nextDep || nextDevDep
	for _, script := range pkg.Scripts {
		if nextServer.MatchString(script) {
			isNext = true
		}
	}
	if !isNext {
		return nil, errors.New("no Next.js dependency or next dev/start command found in package.json")
	}
	if len(pkg.Scripts) == 0 {
		return nil, errors.New("no scripts found in package.json")
	}
	keys := []string{}
	for key := range pkg.Scripts {
		keys = append(keys, key)
	}
	rank := func(s string) int {
		if s == "dev" {
			return 0
		}
		if s == "start" {
			return 1
		}
		return 2
	}
	sort.Slice(keys, func(i, j int) bool {
		if rank(keys[i]) != rank(keys[j]) {
			return rank(keys[i]) < rank(keys[j])
		}
		return keys[i] < keys[j]
	})
	command, warnings := commandPath("npm")
	cwd := filepath.Dir(path)
	name := pkg.Name
	if name == "" {
		name = filepath.Base(cwd)
	}
	result := []discoveredProject{}
	for _, key := range keys {
		if len(result) >= 200 {
			break
		}
		port, source := nextScriptPort(pkg.Scripts[key])
		notes := append([]string{}, warnings...)
		if port == 0 {
			notes = append(notes, "Could not determine a server port for this script; targets such as build/lint may be one-off tasks.")
		}
		result = append(result, discoveredProject{Label: key + " · " + pkg.Scripts[key], Kind: "nextjs", Source: path, Target: key, PortSource: source, Warnings: notes, Project: Project{Name: safeProjectName(name), Cwd: cwd, Command: command, Args: []string{"run", key}, Env: map[string]string{}, Port: port, RequestMetric: "http_requests_total"}})
	}
	return result, nil
}

// launchSettings/appsettings allow comments and trailing commas. Keep quoted URLs intact.
func cleanJSONC(b []byte) []byte {
	b = bytes.TrimPrefix(b, []byte{0xef, 0xbb, 0xbf})
	out := []byte{}
	quoted, escaped := false, false
	for i := 0; i < len(b); i++ {
		c := b[i]
		if quoted {
			out = append(out, c)
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				quoted = false
			}
			continue
		}
		if c == '"' {
			quoted = true
			out = append(out, c)
			continue
		}
		if c == '/' && i+1 < len(b) && b[i+1] == '/' {
			i += 2
			for i < len(b) && b[i] != '\n' {
				i++
			}
			out = append(out, '\n')
			continue
		}
		if c == '/' && i+1 < len(b) && b[i+1] == '*' {
			i += 2
			for i+1 < len(b) && !(b[i] == '*' && b[i+1] == '/') {
				i++
			}
			i++
			out = append(out, ' ')
			continue
		}
		out = append(out, c)
	}
	result := []byte{}
	quoted, escaped = false, false
	for i, c := range out {
		if quoted {
			result = append(result, c)
			if escaped {
				escaped = false
			} else if c == '\\' {
				escaped = true
			} else if c == '"' {
				quoted = false
			}
			continue
		}
		if c == '"' {
			quoted = true
		}
		if c == ',' {
			j := i + 1
			for j < len(out) && strings.ContainsRune(" \t\r\n", rune(out[j])) {
				j++
			}
			if j < len(out) && (out[j] == '}' || out[j] == ']') {
				continue
			}
		}
		result = append(result, c)
	}
	return result
}

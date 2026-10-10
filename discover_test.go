package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func fixture(t *testing.T, root, path, body string) string {
	t.Helper()
	file := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return file
}
func TestSolutionReadsProfilesAndSkipsLibraries(t *testing.T) {
	root := t.TempDir()
	api := fixture(t, root, "src/Shop.Api/Shop.Api.csproj", `<Project Sdk="Microsoft.NET.Sdk.Web"><PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup></Project>`)
	fixture(t, root, "src/Shared/Shared.csproj", `<Project Sdk="Microsoft.NET.Sdk" />`)
	fixture(t, root, "src/Shop.Api/Properties/launchSettings.json", `{
 // Keep the URL slashes and commas inside strings.
 "profiles": {
  "https": {"commandName":"Project","applicationUrl":"https://localhost:7001;http://localhost:5001","environmentVariables":{"ASPNETCORE_ENVIRONMENT":"Development"}},
  "http": {"commandName":"Project","applicationUrl":"http://localhost:5100","environmentVariables":{"ASPNETCORE_ENVIRONMENT":"Development","LABEL":"a,}"},},
  "IIS Express": {"commandName":"IISExpress","applicationUrl":"http://localhost:1234"},
 },
}`)
	sln := fixture(t, root, "Shop.sln", "Microsoft Visual Studio Solution File, Format Version 12.00\nProject(\"{type}\") = \"Shop.Api\", \"src\\Shop.Api\\Shop.Api.csproj\", \"{id}\"\nEndProject\nProject(\"{type}\") = \"Shared\", \"src\\Shared\\Shared.csproj\", \"{id}\"\nEndProject\n")
	result, err := discover(sln)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Candidates) != 2 {
		t.Fatal(result)
	}
	http := result.Candidates[0]
	if http.Target != "http" || http.Project.Port != 5100 || http.Project.Cwd != filepath.Dir(api) || http.Project.Name != "Shop.Api" {
		t.Fatal(http)
	}
	want := []string{"run", "--project", api, "--launch-profile", "http"}
	if !reflect.DeepEqual(http.Project.Args, want) {
		t.Fatal(http.Project.Args)
	}
	if result.Candidates[1].Project.Port != 5001 {
		t.Fatal("HTTP port should be preferred over HTTPS")
	}
	if http.Project.Env["LABEL"] != "a,}" {
		t.Fatal("JSONC parser changed a string")
	}
	for _, candidate := range result.Candidates {
		if candidate.Project.WatchFiles == nil || *candidate.Project.WatchFiles || candidate.Project.Env["DOTNET_WATCH_SUPPRESS_LAUNCH_BROWSER"] != "1" {
			t.Fatal("discovered launch profiles must suppress browser launch on the server")
		}
	}
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "library") {
		t.Fatal(result.Warnings)
	}
}
func TestSlnxAndDotnetSettingsFallback(t *testing.T) {
	root := t.TempDir()
	api := fixture(t, root, "Api/Api.csproj", `<Project Sdk="Microsoft.NET.Sdk.Web"/>`)
	fixture(t, root, "Api/appsettings.Development.json", `{"Kestrel":{"Endpoints":{"Https":{"Url":"https://localhost:7443"},"Http":{"Url":"http://localhost:5222"}}}}`)
	fixture(t, root, "Api/appsettings.json", `{"Urls":"http://localhost:5000"}`)
	path := fixture(t, root, "Shop.slnx", `<Solution><Folder Name="/src/"><Project Path="Api/Api.csproj"/></Folder></Solution>`)
	result, err := discover(path)
	if err != nil || len(result.Candidates) != 1 {
		t.Fatal(result, err)
	}
	p := result.Candidates[0]
	if p.Project.Port != 5222 || p.Source != api || p.Project.Env["ASPNETCORE_ENVIRONMENT"] != "Development" || !strings.Contains(p.PortSource, "Kestrel") {
		t.Fatal(p)
	}
	if !strings.Contains(strings.Join(p.Project.Args, " "), "--no-launch-profile") {
		t.Fatal(p.Project.Args)
	}
	if p.Project.Env["DOTNET_WATCH_SUPPRESS_LAUNCH_BROWSER"] != "1" {
		t.Fatal("projects without launch profiles must also suppress browser launch")
	}
}
func TestDotnetUsesSelectedProfileEnvironment(t *testing.T) {
	root := t.TempDir()
	path := fixture(t, root, "Api.csproj", `<Project Sdk="Microsoft.NET.Sdk.Web"/>`)
	fixture(t, root, "Properties/launchSettings.json", `{"profiles":{"prod":{"commandName":"Project","environmentVariables":{"ASPNETCORE_ENVIRONMENT":"Production"}}}}`)
	fixture(t, root, "appsettings.Development.json", `{"Urls":"http://localhost:5001"}`)
	fixture(t, root, "appsettings.Production.json", `{"Urls":"http://localhost:8001"}`)
	result, err := discover(path)
	if err != nil || len(result.Candidates) != 1 || result.Candidates[0].Project.Port != 8001 {
		t.Fatal(result, err)
	}
}
func TestDotnetUnknownPortAndInvalidLaunchSettings(t *testing.T) {
	root := t.TempDir()
	path := fixture(t, root, "Worker.fsproj", `<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><OutputType>Exe</OutputType></PropertyGroup></Project>`)
	fixture(t, root, "Properties/launchSettings.json", `{broken`)
	result, err := discover(path)
	if err != nil || len(result.Candidates) != 1 {
		t.Fatal(result, err)
	}
	if result.Candidates[0].Project.Port != 0 || len(result.Candidates[0].Warnings) < 2 {
		t.Fatal(result)
	}
}
func TestNextScriptPorts(t *testing.T) {
	for _, test := range []struct {
		script string
		port   int
	}{
		{"next dev", 3000}, {"next start", 3000}, {"next dev --port 4100", 4100}, {"next dev --port=4200", 4200}, {"next dev -p 4300", 4300}, {"next dev -p4400", 4400},
		{"next dev -p '4500'", 4500}, {"PORT=4600 next dev", 4600}, {"cross-env PORT=4700 next start", 4700}, {"PORT=4800 next dev -p $PORT", 0},
		{"PORT=4900 next dev --port 5000", 5000}, {"next dev -p $APP_PORT", 0}, {"PORT=$APP_PORT next dev", 0}, {"next dev -p 99999", 0}, {"next build", 0}, {"node server.js", 0},
		{"next dev & next start -p 8000", 0}, {"next dev && tool --port 9000", 3000},
	} {
		t.Run(test.script, func(t *testing.T) {
			port, _ := nextScriptPort(test.script)
			if port != test.port {
				t.Fatalf("got %d want %d", port, test.port)
			}
		})
	}
}
func TestNextDiscoversAllTargetsAndDoesNotReadDotenvPort(t *testing.T) {
	root := t.TempDir()
	path := fixture(t, root, "package.json", `{"name":"@shop/web","dependencies":{"next":"^16"},"scripts":{"lint":"eslint .","build":"next build","start":"next start -p 4000","dev":"next dev --turbopack"}}`)
	fixture(t, root, ".env.local", "PORT=9999\n")
	result, err := discover(path)
	if err != nil || len(result.Candidates) != 4 {
		t.Fatal(result, err)
	}
	first := result.Candidates[0]
	if first.Target != "dev" || first.Project.Name != "shop-web" || first.Project.Port != 3000 || first.Project.Cwd != root {
		t.Fatal(first)
	}
	if !reflect.DeepEqual(first.Project.Args, []string{"run", "dev"}) {
		t.Fatal(first.Project.Args)
	}
	if result.Candidates[1].Target != "start" || result.Candidates[1].Project.Port != 4000 {
		t.Fatal(result.Candidates[1])
	}
	if result.Candidates[2].Target != "build" || result.Candidates[2].Project.Port != 0 {
		t.Fatal(result.Candidates[2])
	}
}
func TestDirectoryDiscoveryDeduplicatesAndSkipsBuildDirectories(t *testing.T) {
	root := t.TempDir()
	fixture(t, root, "Api/Api.csproj", `<Project Sdk="Microsoft.NET.Sdk.Web"/>`)
	fixture(t, root, "Shop.slnx", `<Solution><Project Path="Api/Api.csproj"/></Solution>`)
	fixture(t, root, "web/package.json", `{"scripts":{"dev":"next dev"}}`)
	for _, dir := range []string{"node_modules", "bin", "obj", ".git", ".next"} {
		fixture(t, root, dir+"/nested/package.json", `{"scripts":{"dev":"next dev"}}`)
	}
	result, err := discover(root)
	if err != nil || len(result.Candidates) != 2 {
		t.Fatal(result, err)
	}
}
func TestDiscoveryValidationAndAuthentication(t *testing.T) {
	s := testServer(t)
	root := t.TempDir()
	path := fixture(t, root, "package.json", `{"scripts":{"dev":"next dev -p 4567"}}`)
	b, _ := json.Marshal(map[string]string{"path": path})
	w := call(s, "POST", "/api/discover", string(b), "", "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if len(s.config.Projects) != 0 {
		t.Fatal("discovery registered a project")
	}
	req := httptest.NewRequest("POST", "/api/discover", strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	s.routes().ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
	for _, body := range []string{`{"path":"relative"}`, `{"path":"/does/not/exist"}`, `{"path":"/tmp","extra":true}`} {
		w = call(s, "POST", "/api/discover", body, "", "")
		if w.Code != 400 {
			t.Fatal(w.Code, w.Body)
		}
	}
	if _, err := discover(fixture(t, root, "random.json", `{}`)); err == nil {
		t.Fatal("unsupported file accepted")
	}
	if _, err := readProjectFile(fixture(t, root, "large.json", strings.Repeat(" ", (2<<20)+1))); err == nil {
		t.Fatal("oversized file accepted")
	}
}
func TestJSONCWithBOMAndStringEscapes(t *testing.T) {
	b := []byte("\xef\xbb\xbf" + `{/* block */ "url":"http://localhost:5001", "quoted":"a\\\"//b", "items":[1,2,],}`)
	var value map[string]any
	if err := json.Unmarshal(cleanJSONC(b), &value); err != nil {
		t.Fatal(err, string(cleanJSONC(b)))
	}
	if value["url"] != "http://localhost:5001" {
		t.Fatal(value)
	}
}

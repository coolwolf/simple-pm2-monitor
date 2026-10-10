package main

import (
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
)

func pm2LaunchProject(p Project) (Project, error) {
	p, err := projectLaunchMode(p)
	if err != nil {
		return p, err
	}
	if filepath.Base(p.Command) == "dotnet" {
		// PM2 injects its package version (often "N/A") as a lowercase
		// environment variable. MSBuild imports it as the Version property.
		envCommand, err := exec.LookPath("env")
		if err != nil {
			return p, errors.New("the env executable is required to launch .NET safely under PM2")
		}
		p.Args = append([]string{"-u", "version", p.Command}, p.Args...)
		p.Command = envCommand
	}
	return p, nil
}

// A missing setting preserves the original command for older configurations.
func projectLaunchMode(p Project) (Project, error) {
	if p.WatchFiles == nil {
		return p, nil
	}
	p.Args = append([]string{}, p.Args...)
	switch filepath.Base(p.Command) {
	case "dotnet":
		if len(p.Args) == 0 {
			return p, errors.New("Watch files requires dotnet run or dotnet watch run")
		}
		switch p.Args[0] {
		case "watch":
			if !*p.WatchFiles {
				runIndex := -1
				for i := 1; i < len(p.Args); i++ {
					if p.Args[i] == "--project" || p.Args[i] == "-p" {
						i++
						continue
					}
					if p.Args[i] == "run" {
						runIndex = i
						break
					}
					return p, errors.New("cannot disable watching for custom dotnet watch options; use dotnet run arguments")
				}
				if runIndex < 0 {
					return p, errors.New("Watch files requires an explicit dotnet watch run command")
				}
				args := append([]string{"run"}, p.Args[1:runIndex]...)
				p.Args = append(args, p.Args[runIndex+1:]...)
			}
		case "run":
			if *p.WatchFiles {
				p.Args = append([]string{"watch"}, p.Args...)
			}
		default:
			return p, errors.New("Watch files supports dotnet run and dotnet watch run only")
		}
	case "npm":
		if len(p.Args) < 2 || p.Args[0] != "run" || (p.Args[1] != "dev" && p.Args[1] != "start") {
			return p, errors.New("Watch files supports Next.js npm run dev/start only")
		}
		data, err := readProjectFile(filepath.Join(p.Cwd, "package.json"))
		if err != nil {
			return p, err
		}
		var pkg struct {
			Scripts         map[string]string          `json:"scripts"`
			Dependencies    map[string]json.RawMessage `json:"dependencies"`
			DevDependencies map[string]json.RawMessage `json:"devDependencies"`
		}
		if err := json.Unmarshal(data, &pkg); err != nil {
			return p, err
		}
		if pkg.Dependencies["next"] == nil && pkg.DevDependencies["next"] == nil {
			return p, errors.New("Watch files npm support requires a Next.js project")
		}
		target := "start"
		if *p.WatchFiles {
			target = "dev"
		}
		if pkg.Scripts[target] == "" {
			return p, errors.New("package.json is missing the selected " + target + " script")
		}
		p.Args[1] = target
		env := map[string]string{}
		for k, v := range p.Env {
			env[k] = v
		}
		env["NODE_ENV"] = "production"
		if *p.WatchFiles {
			env["NODE_ENV"] = "development"
		}
		p.Env = env
	default:
		return p, errors.New("Watch files supports dotnet and Next.js npm commands only")
	}
	return p, nil
}

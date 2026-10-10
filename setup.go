package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const defaultListen = "127.0.0.1:8989"

func validateListen(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("listening address must use IP:port format: %w", err)
	}
	if net.ParseIP(host) == nil {
		return errors.New("use a valid IP in the listening address")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return errors.New("listening port must be between 1 and 65535")
	}
	return nil
}
func validateAdmin(username, password string) error {
	if username == "" && password == "" {
		return nil
	}
	if strings.TrimSpace(username) == "" || strings.ContainsAny(username, ":\r\n\x00") || strings.TrimSpace(password) == "" {
		return errors.New("admin username and password are both required; username cannot contain ':' or control characters")
	}
	return nil
}
func checkConfigListen(address string, c Config) error {
	if err := validateListen(address); err != nil {
		return err
	}
	if c.AdminUsername == "" || c.AdminPassword == "" {
		return errors.New("add adminUsername and adminPassword to the JSON configuration; run in a terminal for first-time setup")
	}
	return validateAdmin(c.AdminUsername, c.AdminPassword)
}
func prepareConfig(path, cliListen string, override, nonInteractive bool, input io.Reader, output io.Writer) (Config, string, error) {
	c := Config{Projects: []Project{}}
	data, err := os.ReadFile(path)
	if err == nil {
		if err = json.Unmarshal(data, &c); err != nil {
			return c, "", err
		}
		if err = validate(c); err != nil {
			return c, "", err
		}
		address := c.Listen
		if address == "" {
			address = defaultListen
		}
		if override {
			address = cliListen
		}
		return c, address, checkConfigListen(address, c)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return c, "", err
	}
	c.Listen = cliListen
	if nonInteractive {
		return c, "", errors.New("configuration file is missing; run first-time setup in a terminal or create a JSON file containing adminUsername and adminPassword")
	} else {
		fmt.Fprintf(output, "%s was not found. Starting first-time setup.\nPress Enter to accept a default.\n", path)
		c, err = firstRun(input, output, cliListen)
		if err != nil {
			return c, "", err
		}
	}
	if err = saveConfig(path, c); err != nil {
		return c, "", fmt.Errorf("could not save configuration: %w", err)
	}
	fmt.Fprintf(output, "Configuration saved to %s. You can also manage projects through the web UI.\n", path)
	return c, c.Listen, nil
}
func saveConfig(path string, c Config) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".projects-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(append(b, '\n')); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	return err
}

type terminalSetup struct {
	reader *bufio.Reader
	output io.Writer
}

func (t terminalSetup) ask(label, fallback string, valid func(string) error) (string, error) {
	for {
		if fallback != "" {
			fmt.Fprintf(t.output, "%s [%s]: ", label, fallback)
		} else {
			fmt.Fprintf(t.output, "%s: ", label)
		}
		line, err := t.reader.ReadString('\n')
		if err != nil && (err != io.EOF || len(line) == 0) {
			return "", fmt.Errorf("setup input is incomplete; run in a terminal or use -non-interactive with an existing configuration: %w", err)
		}
		value := strings.TrimSpace(line)
		if value == "" {
			value = fallback
		}
		if valid != nil {
			if err = valid(value); err != nil {
				fmt.Fprintln(t.output, "Error:", err)
				continue
			}
		}
		return value, nil
	}
}
func yesNo(s string) error {
	switch strings.ToLower(s) {
	case "y", "yes", "n", "no":
		return nil
	}
	return errors.New("enter yes (y) or no (n)")
}
func isYes(s string) bool {
	switch strings.ToLower(s) {
	case "y", "yes":
		return true
	}
	return false
}
func firstRun(input io.Reader, output io.Writer, address string) (Config, error) {
	c := Config{Projects: []Project{}}
	t := terminalSetup{bufio.NewReader(input), output}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return c, err
	}
	host, err = t.ask("Listening IP", host, func(s string) error {
		ip := net.ParseIP(s)
		if ip == nil {
			return errors.New("enter a valid IPv4 or IPv6 address")
		}
		return nil
	})
	if err != nil {
		return c, err
	}
	port, err = t.ask("Listening port", port, func(s string) error { return validateListen(net.JoinHostPort(host, s)) })
	if err != nil {
		return c, err
	}
	c.Listen = net.JoinHostPort(host, port)
	c.AdminUsername, err = t.ask("Admin username", "admin", func(s string) error { return validateAdmin(s, "placeholder") })
	if err != nil {
		return c, err
	}
	c.AdminPassword, err = t.ask("Admin password (stored as plain text in projects.json)", "", func(s string) error { return validateAdmin(c.AdminUsername, s) })
	if err != nil {
		return c, err
	}
	if err = checkConfigListen(c.Listen, c); err != nil {
		return c, err
	}
	question := "Add project settings now? (y/n)"
	for {
		answer, err := t.ask(question, "n", yesNo)
		if err != nil {
			return c, err
		}
		if !isYes(answer) {
			break
		}
		p, err := t.project(c.Projects)
		if err != nil {
			return c, err
		}
		c.Projects = append(c.Projects, p)
		fmt.Fprintln(output, "The project will be saved without starting it.")
		question = "Add another project? (y/n)"
	}
	return c, validate(c)
}
func (t terminalSetup) project(existing []Project) (Project, error) {
	p := Project{Args: []string{}, Env: map[string]string{}, RequestMetric: "http_requests_total"}
	var err error
	p.Name, err = t.ask("Project name", "", func(name string) error {
		candidate := append(append([]Project{}, existing...), Project{Name: name, Cwd: "/", Command: "placeholder"})
		return validate(Config{Projects: candidate})
	})
	if err != nil {
		return p, err
	}
	p.Group, err = t.ask("Group (optional)", "", nil)
	if err != nil {
		return p, err
	}
	p.Cwd, err = t.ask("Working directory (absolute path)", "", func(path string) error {
		if !filepath.IsAbs(path) {
			return errors.New("enter an absolute directory path")
		}
		return nil
	})
	if err != nil {
		return p, err
	}
	p.Command, err = t.ask("Command (for example: /usr/bin/dotnet or /usr/bin/npm)", "", func(s string) error {
		if s == "" || strings.ContainsRune(s, 0) {
			return errors.New("command is required")
		}
		return nil
	})
	if err != nil {
		return p, err
	}
	_, err = t.ask("Arguments (JSON array)", "[]", func(s string) error {
		if !strings.HasPrefix(s, "[") {
			return errors.New("enter a JSON array, for example: [\"run\",\"dev\"]")
		}
		return json.Unmarshal([]byte(s), &p.Args)
	})
	if err != nil {
		return p, err
	}
	_, err = t.ask("Environment variables (JSON object)", "{}", func(s string) error {
		if !strings.HasPrefix(s, "{") {
			return errors.New("enter a JSON object")
		}
		var env map[string]string
		if err := json.Unmarshal([]byte(s), &env); err != nil {
			return err
		}
		p.Env = env
		return nil
	})
	if err != nil {
		return p, err
	}
	_, err = t.ask("Project port (0 if unknown)", "0", func(s string) error {
		n, e := strconv.Atoi(s)
		if e != nil || n < 0 || n > 65535 {
			return errors.New("project port must be between 0 and 65535")
		}
		p.Port = n
		return nil
	})
	if err != nil {
		return p, err
	}
	return p, nil
}

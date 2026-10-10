package main

import (
	"errors"
	"net/http"
)

// Project mutations preserve admin settings and unrelated projects. They never run PM2 commands.
func (s *server) handleProject(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" && r.Method != "PUT" && r.Method != "DELETE" {
		w.WriteHeader(405)
		return
	}
	var q struct {
		Name    string   `json:"name"`
		Project *Project `json:"project,omitempty"`
	}
	if err := decode(w, r, &q); err != nil {
		failure(w, 400, err)
		return
	}
	if r.Method != "POST" && q.Name == "" {
		failure(w, 400, errors.New("original project name is required"))
		return
	}
	if r.Method != "DELETE" && q.Project == nil {
		failure(w, 400, errors.New("project settings are required"))
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.config
	c.Projects = append([]Project{}, s.config.Projects...)
	index := -1
	for i, p := range c.Projects {
		if p.Name == q.Name {
			index = i
			break
		}
	}
	if r.Method != "POST" && index < 0 {
		failure(w, 404, errors.New("project no longer exists"))
		return
	}
	if r.Method != "DELETE" {
		for i, p := range c.Projects {
			if p.Name == q.Project.Name && (r.Method == "POST" || i != index) {
				failure(w, 409, errors.New("a project with that name already exists"))
				return
			}
		}
	}
	switch r.Method {
	case "POST":
		c.Projects = append(c.Projects, *q.Project)
	case "PUT":
		c.Projects[index] = *q.Project
	case "DELETE":
		c.Projects = append(c.Projects[:index], c.Projects[index+1:]...)
	}
	if err := validate(c); err != nil {
		failure(w, 400, err)
		return
	}
	if err := saveConfig(s.path, c); err != nil {
		failure(w, 500, err)
		return
	}
	s.config = c
	delete(s.samples, q.Name)
	if q.Project != nil {
		delete(s.samples, q.Project.Name)
	}
	status := 200
	if r.Method == "POST" {
		status = 201
	}
	writeJSON(w, status, c)
}

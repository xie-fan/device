package api

import (
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"

	"toy-device-simulator/config"
	"toy-device-simulator/ui"
)

func (s *Server) mountUI() {
	s.mux.HandleFunc("GET /{$}", s.handleUIIndex)
	s.mux.HandleFunc("GET /ui/{name}", s.handleUIFile)
	s.mux.HandleFunc("GET /samples", s.handleListSamples)
	s.mux.HandleFunc("GET /samples/hello.wav", s.handleSampleHello)
	s.mux.HandleFunc("GET /samples/content", s.handleSampleContent)
}

func (s *Server) handleUIIndex(w http.ResponseWriter, r *http.Request) {
	s.writeUI(w, r, "index.html")
}

func (s *Server) handleUIFile(w http.ResponseWriter, r *http.Request) {
	s.writeUI(w, r, path.Base(r.PathValue("name")))
}

func (s *Server) writeUI(w http.ResponseWriter, r *http.Request, name string) {
	ctype, ok := uiContentType(name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	f, err := ui.FS.Open(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, f)
}

func (s *Server) handleSampleHello(w http.ResponseWriter, r *http.Request) {
	p := sampleWAVPath()
	if p == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeFile(w, r, p)
}

func sampleWAVPath() string {
	p, _ := findSampleFile("hello.wav")
	return p
}

func sampleDirs() []string {
	return []string{
		filepath.Join("testdata", "audio", "veepai-test"),
		filepath.Join("testdata", "audio"),
		filepath.Join("testdata"),
		filepath.Join("..", "testdata", "audio", "veepai-test"),
		filepath.Join("..", "testdata", "audio"),
		filepath.Join("..", "testdata"),
	}
}

func findSampleFile(name string) (string, error) {
	if err := config.ValidatePathComponent(name); err != nil {
		return "", err
	}
	if filepath.Ext(name) != ".wav" {
		return "", os.ErrNotExist
	}
	for _, dir := range sampleDirs() {
		p := filepath.Join(dir, name)
		st, err := os.Stat(p)
		if err == nil && !st.IsDir() {
			return p, nil
		}
	}
	return "", os.ErrNotExist
}

func (s *Server) handleListSamples(w http.ResponseWriter, r *http.Request) {
	seen := map[string]bool{}
	list := []map[string]string{}
	for _, dir := range sampleDirs() {
		ents, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if e.IsDir() || filepath.Ext(e.Name()) != ".wav" || seen[e.Name()] {
				continue
			}
			seen[e.Name()] = true
			list = append(list, map[string]string{
				"name": e.Name(),
				"url":  "/samples/content?name=" + url.QueryEscape(e.Name()),
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"samples": list})
}

func (s *Server) handleSampleContent(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	p, err := findSampleFile(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeFile(w, r, p)
}

func uiContentType(name string) (string, bool) {
	switch name {
	case "index.html":
		return "text/html; charset=utf-8", true
	case "app.css":
		return "text/css; charset=utf-8", true
	case "app.js":
		return "text/javascript; charset=utf-8", true
	default:
		return "", false
	}
}

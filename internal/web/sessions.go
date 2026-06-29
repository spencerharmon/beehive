package web

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// sessionsList shows the recorded honeybee sessions for a submodule.
func (s *Server) sessionsList(w http.ResponseWriter, r *http.Request) {
	sm, err := s.submodule(r.PathValue("name"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	names := sessionNames(sm.SessionsDir())
	s.render(w, "session_list.html", map[string]interface{}{"Name": sm.Name, "Sessions": names})
}

// sessionView is the page shell; its body auto-refreshes via HTMX polling.
func (s *Server) sessionView(w http.ResponseWriter, r *http.Request) {
	sm, err := s.submodule(r.PathValue("name"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	branch := r.PathValue("branch")
	if !safeBranch(branch) {
		http.Error(w, "bad branch", http.StatusBadRequest)
		return
	}
	s.render(w, "session_view.html", map[string]interface{}{"Name": sm.Name, "Branch": branch})
}

// sessionBody returns just the transcript text, read live from the working-tree
// file the honeybee's recorder writes. The frontend polls this; opencode is
// never contacted here, so viewers add no load to the agent server.
func (s *Server) sessionBody(w http.ResponseWriter, r *http.Request) {
	sm, err := s.submodule(r.PathValue("name"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	branch := r.PathValue("branch")
	if !safeBranch(branch) {
		http.Error(w, "bad branch", http.StatusBadRequest)
		return
	}
	b, err := os.ReadFile(filepath.Join(sm.SessionsDir(), branch+".md"))
	if err != nil {
		if os.IsNotExist(err) {
			s.render(w, "session_body.html", map[string]interface{}{"Body": "(waiting for session output…)"})
			return
		}
		http.Error(w, err.Error(), 500)
		return
	}
	s.render(w, "session_body.html", map[string]interface{}{"Body": string(b)})
}

func sessionNames(dir string) []string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		out = append(out, strings.TrimSuffix(e.Name(), ".md"))
	}
	sort.Strings(out)
	return out
}

// safeBranch guards the path component against traversal; branches are simple
// stems like "bee-bootstrap" or "bee-T3".
func safeBranch(b string) bool {
	if b == "" || len(b) > 128 {
		return false
	}
	for _, c := range b {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return !strings.Contains(b, "..")
}

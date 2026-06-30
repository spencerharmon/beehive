package web

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/spencerharmon/beehive/internal/editor"
)

// editNew opens a fresh chat-diff editor session over an arbitrary repo file and
// redirects to its chat page. The generic surface takes ?path=<repo-relative>;
// ?file= is accepted as a back-compatible alias for the per-file "edit with AI"
// links. The path is validated to stay inside the repo (editor.ValidateFile).
func (s *Server) editNew(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		path = r.URL.Query().Get("file")
	}
	sess, err := s.editors.Open(r.Context(), path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/editor/"+sess.ID, http.StatusSeeOther)
}

// editorPage is the chat shell; its panel auto-refreshes via HTMX so the diff and
// state update live as the agent edits.
func (s *Server) editorPage(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.editors.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.render(w, "editor.html", map[string]interface{}{"ID": sess.ID, "File": sess.File})
}

// editorPanel renders the live chat log, diff, state indicator and merge button.
func (s *Server) editorPanel(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.editors.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.render(w, "editor_panel.html", s.panelData(r.Context(), sess))
}

func (s *Server) editorChat(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.editors.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	msg := r.FormValue("message")
	if msg != "" {
		// Background context: the turn outlives this request; the panel poll shows
		// progress and the final reply.
		_ = sess.StartChat(context.Background(), msg)
	}
	s.render(w, "editor_panel.html", s.panelData(r.Context(), sess))
}

func (s *Server) editorMerge(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.editors.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := sess.Merge(r.Context()); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.render(w, "editor_panel.html", s.panelData(r.Context(), sess))
}

// editorApprove commits the pending proposal onto the session's edit branch (the
// human gate: a turn writes nothing to the branch until approved).
func (s *Server) editorApprove(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.editors.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := sess.Approve(r.Context()); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.render(w, "editor_panel.html", s.panelData(r.Context(), sess))
}

// editorReject discards the pending proposal, restoring the target file so the
// session is a no-op.
func (s *Server) editorReject(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.editors.Get(r.PathValue("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := sess.Reject(r.Context()); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	s.render(w, "editor_panel.html", s.panelData(r.Context(), sess))
}

func (s *Server) editorClose(w http.ResponseWriter, r *http.Request) {
	_ = s.editors.Close(r.Context(), r.PathValue("id"))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) panelData(ctx context.Context, sess *editor.Session) map[string]interface{} {
	base, proposed, _ := sess.Diff(ctx)
	state := sess.State(ctx)
	pending := sess.Pending(ctx)
	log := sess.Log()
	return map[string]interface{}{
		"ID":      sess.ID,
		"File":    sess.File,
		"Log":     log,
		"Rows":    editor.RenderDiff(base, proposed),
		"State":   state,
		"Live":    state == "live",
		"Pending": pending,
		"Merged":  state == "live" && len(log) > 0,
		"Busy":    sess.Busy(),
		"Error":   sess.Err(),
	}
}

// ---- JSON API (browser-free clients) ----

func writeJSON(w http.ResponseWriter, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) apiEditorOpen(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
		File string `json:"file"` // back-compat alias for path
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	path := req.Path
	if path == "" {
		path = req.File
	}
	sess, err := s.editors.Open(r.Context(), path)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]interface{}{
		"id": sess.ID, "file": sess.File, "branch": sess.Branch, "state": sess.State(r.Context()),
	})
}

func (s *Server) apiEditorGet(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.editors.Get(r.PathValue("id"))
	if !ok {
		writeJSON(w, 404, map[string]string{"error": "no such session"})
		return
	}
	writeJSON(w, 200, map[string]interface{}{
		"id": sess.ID, "file": sess.File, "branch": sess.Branch,
		"state": sess.State(r.Context()), "busy": sess.Busy(), "error": sess.Err(), "log": sess.Log(),
	})
}

func (s *Server) apiEditorChat(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.editors.Get(r.PathValue("id"))
	if !ok {
		writeJSON(w, 404, map[string]string{"error": "no such session"})
		return
	}
	var req struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Message == "" {
		writeJSON(w, 400, map[string]string{"error": "message required"})
		return
	}
	reply, err := sess.Chat(r.Context(), req.Message)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	state := sess.State(r.Context())
	writeJSON(w, 200, map[string]interface{}{"reply": reply, "state": state, "merged": state == "live"})
}

func (s *Server) apiEditorMerge(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.editors.Get(r.PathValue("id"))
	if !ok {
		writeJSON(w, 404, map[string]string{"error": "no such session"})
		return
	}
	if err := sess.Merge(r.Context()); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]interface{}{"state": sess.State(r.Context())})
}

func (s *Server) apiEditorApprove(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.editors.Get(r.PathValue("id"))
	if !ok {
		writeJSON(w, 404, map[string]string{"error": "no such session"})
		return
	}
	if err := sess.Approve(r.Context()); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]interface{}{"state": sess.State(r.Context()), "pending": sess.Pending(r.Context())})
}

func (s *Server) apiEditorReject(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.editors.Get(r.PathValue("id"))
	if !ok {
		writeJSON(w, 404, map[string]string{"error": "no such session"})
		return
	}
	if err := sess.Reject(r.Context()); err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]interface{}{"state": sess.State(r.Context()), "pending": sess.Pending(r.Context())})
}

func (s *Server) apiEditorDiff(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.editors.Get(r.PathValue("id"))
	if !ok {
		writeJSON(w, 404, map[string]string{"error": "no such session"})
		return
	}
	base, proposed, err := sess.Diff(r.Context())
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]interface{}{"base": base, "proposed": proposed, "state": sess.State(r.Context())})
}

package web

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/spencerharmon/beehive/internal/editor"
)

// editNew opens a fresh, publish-capable editor session for the requested
// coordination file and redirects to its chat page. Every edit-with-AI link
// across the UI (dashboard/explorer/roi_editor) passes the file as ?path=; a
// bare ?file= is accepted too (the older param name, and any direct/API
// caller). This is the ONE HTTP entry point every edit-with-AI request reaches
// (ai-edit-publish-to-main) — see editEntry in web.go.
func (s *Server) editNew(w http.ResponseWriter, r *http.Request) {
	sess, err := s.editors.Open(r.Context(), requestedFile(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/editor/"+sess.ID, http.StatusSeeOther)
}

// requestedFile resolves the repo-relative file a GET /edit request names,
// preferring the query string (every template link is a plain GET anchor with
// ?path=) and falling back to a form value, so a POST body or a future ?file=
// caller resolves the same way with no separate code path. path= is checked
// before the older file= from either source.
func requestedFile(r *http.Request) string {
	if v := r.URL.Query().Get("path"); v != "" {
		return v
	}
	if v := r.URL.Query().Get("file"); v != "" {
		return v
	}
	if v := r.FormValue("path"); v != "" {
		return v
	}
	return r.FormValue("file")
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
	var err error
	if r.FormValue("confirm") == "delete" {
		err = sess.MergeConfirm(r.Context())
	} else {
		err = sess.Merge(r.Context())
	}
	if err != nil && err != editor.ErrDeleteNeedsConfirm {
		http.Error(w, err.Error(), 500)
		return
	}
	data := s.panelData(r.Context(), sess)
	if err == editor.ErrDeleteNeedsConfirm {
		// Default-blocked: surface the block in the panel; DeleteRisk drives the
		// distinct confirm button so the human can authorize the deletion.
		data["Error"] = err.Error()
	}
	s.render(w, "editor_panel.html", data)
}

func (s *Server) editorClose(w http.ResponseWriter, r *http.Request) {
	_ = s.editors.Close(r.Context(), r.PathValue("id"))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) panelData(ctx context.Context, sess *editor.Session) map[string]interface{} {
	base, proposed, _ := sess.Diff(ctx)
	state := sess.State(ctx)
	log := sess.Log()
	return map[string]interface{}{
		"ID":         sess.ID,
		"File":       sess.File,
		"Log":        log,
		"Rows":       editor.RenderDiff(base, proposed),
		"State":      state,
		"Live":       state == "live",
		"Merged":     state == "live" && len(log) > 0,
		"Busy":       sess.Busy(),
		"Error":      sess.Err(),
		"DeleteRisk": editor.ProtectedDeletion(sess.File, base, proposed),
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
		File string `json:"file"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	sess, err := s.editors.Open(r.Context(), req.File)
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
	// Body is optional; an empty body means confirm=false (default-block a
	// protected deletion). Only an explicit {"confirm":true} authorizes it.
	var req struct {
		Confirm bool `json:"confirm"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	var err error
	if req.Confirm {
		err = sess.MergeConfirm(r.Context())
	} else {
		err = sess.Merge(r.Context())
	}
	if err == editor.ErrDeleteNeedsConfirm {
		writeJSON(w, 409, map[string]interface{}{"error": err.Error(), "needs_confirm": true})
		return
	}
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, 200, map[string]interface{}{"state": sess.State(r.Context())})
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

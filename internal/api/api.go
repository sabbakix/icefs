// Package api serves the engine to the TUI over a root-only unix socket.
package api

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/sabbakix/icefs/internal/engine"
)

type Server struct {
	eng *engine.Engine
	mux *http.ServeMux
}

func New(eng *engine.Engine) *Server {
	s := &Server{eng: eng, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /v1/status", s.status)
	s.mux.HandleFunc("GET /v1/shares/{share}/chains", s.chains)
	s.mux.HandleFunc("POST /v1/shares/{share}/chains/delete", s.deleteChain)
	s.mux.HandleFunc("POST /v1/shares/{share}/chains/extend", s.extendChain)
	s.mux.HandleFunc("PUT /v1/shares/{share}", s.updateShare)
	s.mux.HandleFunc("GET /v1/events", s.events)
	return s
}

func (s *Server) Handler() http.Handler { return s.mux }

// Listen opens the unix socket with 0600 permissions so only root can use it.
func Listen(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		l.Close()
		return nil, err
	}
	return l, nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	st, err := s.eng.Status()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"shares": st})
}

func (s *Server) chains(w http.ResponseWriter, r *http.Request) {
	cs, err := s.eng.Chains(r.PathValue("share"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"chains": cs})
}

type chainReq struct {
	Chain string `json:"chain"`
	Days  int    `json:"days"`
}

func (s *Server) deleteChain(w http.ResponseWriter, r *http.Request) {
	var req chainReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.eng.DeleteChain(r.PathValue("share"), req.Chain); err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, engine.ErrNotMelted) {
			code = http.StatusConflict
		}
		writeErr(w, code, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) extendChain(w http.ResponseWriter, r *http.Request) {
	var req chainReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.eng.ExtendChain(r.PathValue("share"), req.Chain, req.Days); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type shareReq struct {
	RetentionDays  int     `json:"retention_days"`
	SettleMinutes  int     `json:"settle_minutes"`
	MinFreePercent float64 `json:"min_free_percent"`
	AutoDelete     bool    `json:"auto_delete"`
}

func (s *Server) updateShare(w http.ResponseWriter, r *http.Request) {
	var req shareReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.eng.UpdateShare(r.PathValue("share"), req.RetentionDays, req.SettleMinutes, req.MinFreePercent, req.AutoDelete); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
	}
	ev, err := s.eng.Events(limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": ev})
}

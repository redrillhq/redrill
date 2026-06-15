package server

import (
	"net/http"
	"strings"
)

// handleUI serves the embedded single-page app: a real asset when the path maps
// to one, otherwise index.html so client-side (hash) routing and hard refreshes
// both resolve. The UI is unauthenticated — it is inert without the API, which
// basic auth gates.
func (s *Server) handleUI(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/")
	if name != "" {
		if f, err := s.ui.Open(name); err == nil {
			info, serr := f.Stat()
			_ = f.Close()
			if serr == nil && !info.IsDir() {
				s.uiFiles.ServeHTTP(w, r)
				return
			}
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(s.uiIndex)
}

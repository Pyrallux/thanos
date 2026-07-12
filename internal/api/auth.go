package api

import (
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strings"

	"thanos/internal/config"
)

// auth is middleware that enforces HTTP Basic Auth using the credentials
// stored in the Thanos SQLite config. During first-run setup, when no
// password hash exists, all requests pass through without auth.
func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// If no password hash configured yet, allow through (setup mode).
		if s.cfg.WebPasswordHash == "" {
			next(w, r)
			return
		}

		user, pass, ok := r.BasicAuth()
		if !ok ||
			subtle.ConstantTimeCompare([]byte(user), []byte(s.cfg.WebUsername)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="Thanos"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		// Verify password against bcrypt hash.
		if !config.CheckPassword(s.cfg.WebPasswordHash, pass) {
			w.Header().Set("WWW-Authenticate", `Basic realm="Thanos"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		next(w, r)
	}
}

// authQuery authenticates WebSocket connections using a ?token= query
// parameter containing the Base64-encoded "user:pass" string. This is
// needed because the browser WebSocket API cannot set HTTP headers.
func (s *Server) authQuery(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// If no password hash configured yet, allow through (setup mode).
		if s.cfg.WebPasswordHash == "" {
			next(w, r)
			return
		}

		token := r.URL.Query().Get("token")
		if token == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		decoded, err := base64.StdEncoding.DecodeString(token)
		if err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		parts := strings.SplitN(string(decoded), ":", 2)
		if len(parts) != 2 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		user, pass := parts[0], parts[1]
		if subtle.ConstantTimeCompare([]byte(user), []byte(s.cfg.WebUsername)) != 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if !config.CheckPassword(s.cfg.WebPasswordHash, pass) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		next(w, r)
	}
}
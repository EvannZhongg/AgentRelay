package controller

import (
	"embed"
	"errors"
	"net"
	"net/http"
	"strings"
)

//go:embed ui/index.html
var uiFiles embed.FS

func serveUI(w http.ResponseWriter, r *http.Request) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() || !sameOrigin(r) {
		writeError(w, http.StatusForbidden, errors.New("controller API is local only"))
		return
	}
	if r.Method != http.MethodGet || (r.URL.Path != "/ui" && r.URL.Path != "/ui/") {
		if r.URL.Path != "/ui" && r.URL.Path != "/ui/" {
			http.NotFound(w, r)
			return
		}
		methodNotAllowed(w)
		return
	}
	data, err := uiFiles.ReadFile("ui/index.html")
	if err != nil {
		http.Error(w, "management UI unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

func sameOrigin(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	return origin == "https://"+r.Host
}

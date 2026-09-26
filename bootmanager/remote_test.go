package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIPHostOnly(t *testing.T) {
	h := ipHostOnly(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	for host, want := range map[string]int{
		"172.16.42.1:8080":  http.StatusOK,
		"127.0.0.1":         http.StatusOK,
		"[::1]:8080":        http.StatusOK,
		"localhost:8080":    http.StatusOK,
		"evil.example":      http.StatusMisdirectedRequest,
		"evil.example:8080": http.StatusMisdirectedRequest,
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.Host = host
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != want {
			t.Errorf("Host %s: %d, want %d", host, w.Code, want)
		}
	}
}

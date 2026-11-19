package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUIIsServedWithSecurityHeaders(t *testing.T) {
	srv := httptest.NewServer(New(openTestDB(t)).Handler())
	defer srv.Close()

	for _, path := range []string{"/", "/style.css", "/app.js"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s: status = %d, want 200", path, resp.StatusCode)
		}
		if !strings.Contains(resp.Header.Get("Content-Security-Policy"), "default-src 'self'") {
			t.Errorf("GET %s: missing Content-Security-Policy header", path)
		}
	}

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "headnode") {
		t.Error("GET / did not return the UI page")
	}
}

// "GET /" matches everything, so check that it does not swallow the API: an
// API path must answer with JSON (here 401, since nobody is logged in), not
// with the web page or a plain-text 404.
func TestAPIRoutesWinOverUIRoute(t *testing.T) {
	srv := httptest.NewServer(New(openTestDB(t)).Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/hosts")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("GET /api/hosts: Content-Type = %q, want application/json", ct)
	}
}

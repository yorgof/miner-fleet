package cyd

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func testClient(t *testing.T, handler http.HandlerFunc, password string) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	return New(u.Hostname(), port, password)
}

func TestPasswordAndRestart(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "miner-fleet" || password != "test-password" {
			t.Error("missing Basic authentication")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/api/restart" || r.Header.Get("Origin") != "" {
			t.Errorf("wrong restart request: %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, `{"ok":true}`)
	}, "test-password")
	if err := c.Restart(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestStatusErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		code int
		body string
		want string
	}{
		{"password", 401, `{"error":"Password needed."}`, "MINER_FLEET_CYD_PASSWORD"},
		{"wrong-host", 403, `{}`, "HTTP 403"},
		{"missing-endpoint", 404, "Nothing here.", "HTTP 404"},
		{"malformed", 200, "<html>", "decode /api/status"},
		{"unrelated-json", 200, `{}`, "missing cyd-miner"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.code)
				fmt.Fprint(w, tc.body)
			}, "")
			if _, _, err := c.FetchStatus(context.Background()); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q error, got %v", tc.want, err)
			}
		})
	}
}

func TestRestartRequiresAcknowledgement(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"ok":false}`)
	}, "")
	if err := c.Restart(context.Background()); err == nil {
		t.Fatal("restart without acknowledgement succeeded")
	}
}

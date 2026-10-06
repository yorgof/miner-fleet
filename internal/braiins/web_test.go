package braiins

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebSessionVariablesAndUnionFailure(t *testing.T) {
	secret := "fixture-web-password"
	mode := "ok"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		json.NewDecoder(r.Body).Decode(&v)
		if strings.Contains(v.Query, secret) {
			t.Error("password interpolated in query")
		}
		if strings.Contains(v.Query, "login(") {
			if v.Variables["password"] != secret {
				t.Error("login variables missing")
			}
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "fixture", Path: "/"})
			fmt.Fprint(w, `{"data":{"auth":{"login":{"__typename":"VoidResult"}}}}`)
			return
		}
		if c, e := r.Cookie("session"); e != nil || c.Value != "fixture" {
			t.Error("session not retained")
		}
		if mode == "union" {
			fmt.Fprint(w, `{"data":{"bosminer":{"config":{"updateAutotuning":{"__typename":"BosminerConfigError","message":"private device detail"}}}}}`)
			return
		}
		if mode == "unauthorized" {
			fmt.Fprint(w, `{"errors":[{"message":"UNAUTHORIZED fixture-web-password"}]}`)
			return
		}
		if !strings.Contains(v.Query, "autoUpgrade(value:$enable)") || v.Variables["enable"] != true {
			t.Error("incorrect firmware mutation contract")
		}
		fmt.Fprint(w, `{"data":{"bos":{"autoUpgrade":{"__typename":"VoidResult"}}}}`)
	}))
	defer server.Close()
	web := NewWeb(server.URL)
	ctx := context.Background()
	if err := web.Login(ctx, "root", secret); err != nil {
		t.Fatal(err)
	}
	if err := web.Mutation(ctx, "auto_upgrade", map[string]any{"enable": true}); err != nil {
		t.Fatal(err)
	}
	for _, failure := range []string{"union", "unauthorized"} {
		mode = failure
		if err := web.Mutation(ctx, "autotuning", map[string]any{"powerTarget": 700}); err == nil || strings.Contains(err.Error(), secret) {
			t.Fatal("failed operation accepted or secret leaked", err)
		}
	}
	if err := web.Mutation(ctx, "callCommand", map[string]any{}); err == nil {
		t.Fatal("arbitrary command allowed")
	}
}

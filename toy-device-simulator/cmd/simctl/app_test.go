package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// 一个 httptest 同时当 manager（/registry）和服务端（auth/account/Login）。
func TestLoginSavesTokenPerEnv(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/registry":
			json.NewEncoder(w).Encode(map[string]any{"environments": []map[string]any{{"name": "测试", "http_url": srv.URL + "/"}}})
		case "/auth/account/Login":
			if r.Header.Get("platform") != "app" {
				t.Errorf("缺 platform 头")
			}
			w.Write([]byte(`{"Code":0,"Data":{"AccessToken":"at1","RefreshToken":"rt1"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	t.Chdir(t.TempDir())
	oldOut := stdout
	stdout = io.Discard
	t.Cleanup(func() { stdout = oldOut })

	listen := strings.TrimPrefix(srv.URL, "http://")
	if code := cmdLogin(listen, []string{"--env", "测试", "--account", "a@b.c", "--password", "12345678"}); code != 0 {
		t.Fatalf("code=%d", code)
	}
	got := loadAppTokens()["测试"]
	if got.AccessToken != "at1" || got.Account != "a@b.c" {
		t.Fatalf("存的 token 不对: %+v", got)
	}
	if fi, err := os.Stat(appTokensPath); err != nil || fi.Size() == 0 {
		t.Fatalf("token 文件没写: %v", err)
	}
}

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

// 配了 Cloud Mail：register 发码后自己取信、校验、注册，一步存下 token。
func TestRegisterFetchesCodeFromCloudMail(t *testing.T) {
	sent := false
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch r.URL.Path {
		case "/registry":
			json.NewEncoder(w).Encode(map[string]any{"environments": []map[string]any{{"name": "测试", "http_url": srv.URL + "/"}}})
		case "/api/public/genToken":
			w.Write([]byte(`{"code":200,"data":{"token":"mt"}}`))
		case "/api/public/emailList":
			if r.Header.Get("Authorization") != "mt" {
				w.Write([]byte(`{"code":401,"message":"bad token"}`))
				return
			}
			old := `{"emailId":7,"subject":"旧信","text":"验证码 1111"}`
			if sent {
				w.Write([]byte(`{"code":200,"data":[{"emailId":8,"subject":"注册","content":"<p>2026 年</p><p>您的验证码是：<b>4821</b></p>"},` + old + `]}`))
			} else {
				w.Write([]byte(`{"code":200,"data":[` + old + `]}`))
			}
		case "/auth/otp/Send":
			sent = true
			w.Write([]byte(`{"Code":0,"RequestId":"rq1","Data":{"CooldownSec":60}}`))
		case "/auth/otp/Verify":
			if body["Code"] != "4821" || body["RequestId"] != "rq1" {
				t.Errorf("Verify 参数不对: %v", body)
			}
			w.Write([]byte(`{"Code":0,"Data":{"VerificationTicket":"vt"}}`))
		case "/auth/account/Register":
			w.Write([]byte(`{"Code":0,"Data":{"User":{"UserID":"u1"},"Token":{"AccessToken":"at2","RefreshToken":"rt2"}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	t.Chdir(t.TempDir())
	t.Setenv("SIMCTL_MAIL_URL", srv.URL)
	t.Setenv("SIMCTL_MAIL_ADMIN", "admin@x")
	t.Setenv("SIMCTL_MAIL_PASSWORD", "pw")
	oldOut := stdout
	stdout = io.Discard
	t.Cleanup(func() { stdout = oldOut })

	listen := strings.TrimPrefix(srv.URL, "http://")
	if code := cmdRegister(listen, []string{"--env", "测试", "--account", "a@b.c", "--password", "abc12345"}); code != 0 {
		t.Fatalf("code=%d", code)
	}
	if got := loadAppTokens()["测试"]; got.AccessToken != "at2" || got.UserID != "u1" {
		t.Fatalf("存的 token 不对: %+v", got)
	}
}

func TestExtractCode(t *testing.T) {
	for in, want := range map[string]string{
		"<p>2026-10-08</p>Your code: <b>123456</b>": "123456",
		"验证码为 0042，10 分钟内有效":                        "0042",
		"欢迎 9876 使用":                                "9876",
		"没有数字":                                      "",
	} {
		if got := extractCode(in); got != want {
			t.Errorf("extractCode(%q)=%q want %q", in, got, want)
		}
	}
}

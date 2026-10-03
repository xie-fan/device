package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// App 侧动词：register / login / bind / unbind 扮演手机 App，打环境的 http_url。
// 头与 App 包（uni.aitoys.app）request/index.js 一致。
const appID = "wxf8dd9a19ed3e6436"

type appEnvelope struct {
	Code      int             `json:"Code"`
	Message   string          `json:"Message"`
	RequestId string          `json:"RequestId"`
	Data      json.RawMessage `json:"Data"`
}

func appCall(method, base, path, token string, query url.Values, body any, timeout time.Duration) (appEnvelope, error) {
	var env appEnvelope
	u := strings.TrimRight(base, "/") + "/" + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	req, err := http.NewRequest(method, u, bytes.NewReader(raw))
	if err != nil {
		return env, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("appId", appID)
	req.Header.Set("platform", "app")
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err != nil {
		return env, err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return env, fmt.Errorf("%s %s: HTTP %d，响应不是 JSON", method, path, resp.StatusCode)
	}
	return env, nil
}

func envHTTPURL(listen, env string) (string, error) {
	var reg struct {
		Environments []struct {
			Name    string `json:"name"`
			HTTPURL string `json:"http_url"`
		} `json:"environments"`
	}
	if err := httpGet(listen, "/registry", &reg); err != nil {
		return "", err
	}
	for _, e := range reg.Environments {
		if e.Name == env {
			if e.HTTPURL == "" {
				return "", fmt.Errorf("环境 %s 没配 http_url", env)
			}
			return e.HTTPURL, nil
		}
	}
	return "", fmt.Errorf("配置树里没有环境 %s", env)
}

// App 账号登录态按环境存一份，bind 不带 --token 时用。仓库里已 .gitignore。
const appTokensPath = "data/app_tokens.json"

type appToken struct {
	Account      string `json:"account"`
	UserID       string `json:"user_id,omitempty"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

func loadAppTokens() map[string]appToken {
	m := map[string]appToken{}
	if b, err := os.ReadFile(appTokensPath); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func saveAppToken(env string, t appToken) error {
	m := loadAppTokens()
	m[env] = t
	b, _ := json.MarshalIndent(m, "", "  ")
	if err := os.MkdirAll(filepath.Dir(appTokensPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(appTokensPath, b, 0o600)
}

// otpChannel：带 @ 是邮箱，否则按手机号。
func otpChannel(account string) string {
	if strings.Contains(account, "@") {
		return "email"
	}
	return "phone"
}

func accountFlags(fs *flag.FlagSet, listen, env, account, password *string) {
	addListen(fs, listen)
	fs.StringVar(env, "env", "", "环境名（用它的 http_url）")
	fs.StringVar(account, "account", "", "邮箱或手机号")
	fs.StringVar(password, "password", os.Getenv("SIMCTL_APP_PASSWORD"), "密码 8-16 位，至少两类字符（默认 SIMCTL_APP_PASSWORD）")
}

// cmdRegister 两步：不带 --code 只发验证码；带 --code --request-id 则校验 + 注册，存下 token。
func cmdRegister(listen string, args []string) int {
	fs := newFS("register")
	var env, account, password, otp, reqID, country string
	accountFlags(fs, &listen, &env, &account, &password)
	fs.StringVar(&otp, "code", "", "收到的验证码；不带则只发验证码")
	fs.StringVar(&reqID, "request-id", "", "发验证码时返回的 request_id")
	fs.StringVar(&country, "country", "", "手机号的两位国家码，如 CN")
	if code, ok := parseFS(fs, args); !ok {
		return code
	}
	if env == "" || account == "" {
		return fail("register 需要 --env 和 --account")
	}
	base, err := envHTTPURL(listen, env)
	if err != nil {
		return fail(err.Error())
	}
	ch := otpChannel(account)
	if otp == "" {
		body := map[string]string{"Channel": ch, "Contact": account, "Purpose": "register"}
		if country != "" {
			body["CountryCode"] = country
		}
		res, err := appCall(http.MethodPost, base, "auth/otp/Send", "", nil, body, 10*time.Second)
		if err != nil {
			return fail(err.Error())
		}
		r := map[string]any{"step": "send", "code": res.Code, "message": res.Message, "request_id": res.RequestId}
		if res.Code == 0 {
			var d struct{ CooldownSec, ExpireInSeconds int }
			_ = json.Unmarshal(res.Data, &d)
			r["cooldown_sec"], r["expire_in_seconds"] = d.CooldownSec, d.ExpireInSeconds
			r["next"] = "simctl register --env " + env + " --account " + account + " --code <验证码> --request-id " + res.RequestId + " --password <密码>"
		}
		return out(r)
	}
	if reqID == "" || password == "" {
		return fail("带 --code 时还要 --request-id 和 --password")
	}
	res, err := appCall(http.MethodPost, base, "auth/otp/Verify", "", nil, map[string]string{
		"Channel": ch, "Contact": account, "Purpose": "register", "Code": otp, "RequestId": reqID,
	}, 10*time.Second)
	if err != nil {
		return fail(err.Error())
	}
	if res.Code != 0 {
		return out(map[string]any{"step": "verify", "code": res.Code, "message": res.Message})
	}
	var v struct{ VerificationTicket string }
	_ = json.Unmarshal(res.Data, &v)
	res, err = appCall(http.MethodPost, base, "auth/account/Register", "", nil, map[string]string{
		"VerificationTicket": v.VerificationTicket, "Password": password,
	}, 10*time.Second)
	if err != nil {
		return fail(err.Error())
	}
	if res.Code != 0 {
		return out(map[string]any{"step": "register", "code": res.Code, "message": res.Message})
	}
	var r struct {
		User  struct{ UserID string }
		Token struct{ AccessToken, RefreshToken string }
	}
	_ = json.Unmarshal(res.Data, &r)
	return finishLogin(env, appToken{Account: account, UserID: r.User.UserID,
		AccessToken: r.Token.AccessToken, RefreshToken: r.Token.RefreshToken})
}

func cmdLogin(listen string, args []string) int {
	fs := newFS("login")
	var env, account, password string
	accountFlags(fs, &listen, &env, &account, &password)
	if code, ok := parseFS(fs, args); !ok {
		return code
	}
	if env == "" || account == "" || password == "" {
		return fail("login 需要 --env、--account、--password（或 SIMCTL_APP_PASSWORD）")
	}
	base, err := envHTTPURL(listen, env)
	if err != nil {
		return fail(err.Error())
	}
	res, err := appCall(http.MethodPost, base, "auth/account/Login", "", nil, map[string]any{
		"Account": account, "Password": password,
		"Device": map[string]string{"Platform": "android", "Model": "simctl", "DeviceID": "simctl"},
	}, 10*time.Second)
	if err != nil {
		return fail(err.Error())
	}
	if res.Code != 0 {
		return out(map[string]any{"code": res.Code, "message": res.Message})
	}
	var t struct{ AccessToken, RefreshToken string }
	_ = json.Unmarshal(res.Data, &t)
	return finishLogin(env, appToken{Account: account, AccessToken: t.AccessToken, RefreshToken: t.RefreshToken})
}

// finishLogin 存 token；输出不回显 token。
func finishLogin(env string, t appToken) int {
	if t.AccessToken == "" {
		return fail("响应里没有 AccessToken")
	}
	if err := saveAppToken(env, t); err != nil {
		return fail(err.Error())
	}
	return out(map[string]any{"code": 0, "environment": env, "account": t.Account, "user_id": t.UserID, "saved": appTokensPath})
}

// cmdBind 调 user/device/Bind|UnBind，再查 user/device/Lists 看设备在不在，
// 并对照设备端有没有收到 bind/client。
func cmdBind(listen string, args []string, unbind bool) int {
	verb := map[bool]string{false: "bind", true: "unbind"}[unbind]
	fs := newFS(verb)
	token := os.Getenv("SIMCTL_APP_TOKEN")
	var identity string
	var b bind
	addListen(fs, &listen)
	fs.StringVar(&b.Env, "env", "", "设备没在跑时，按这三级拉起来（同 run）")
	fs.StringVar(&b.Ent, "enterprise", "", "厂商简称")
	fs.StringVar(&b.Typ, "device-type", "", "类型简称")
	fs.StringVar(&b.Product, "product", "", "产品（类型没配默认产品时要带）")
	fs.StringVar(&token, "token", token, "App 登录态 Authorization（默认 SIMCTL_APP_TOKEN，再退到该环境 login 存下的）")
	fs.StringVar(&identity, "identity", "", "IdentityID（服务端要求身份时带）")
	if code, ok := parseFS(fs, args); !ok {
		return code
	}
	dev := fs.Arg(0)
	if dev == "" {
		return fail(verb + " 需要 device_id")
	}
	idPath := "/devices/" + url.PathEscape(dev)
	if b.Env != "" || b.Ent != "" || b.Typ != "" {
		if b.Env == "" || b.Ent == "" || b.Typ == "" {
			return fail("--env / --enterprise / --device-type 要给全")
		}
		l, busy, err := tryLease(listen, dev, false)
		if err != nil {
			return fail(err.Error())
		}
		if busy {
			return fail("设备 " + dev + " 被别的 run 占着")
		}
		_, _, err = ensureReady(listen, l.Device, b, false)
		releaseLease(listen, dev, l.ID)
		if err != nil {
			return fail(annotateNotReady(listen, dev, err).Error())
		}
	}
	var row deviceRow
	if err := httpGet(listen, idPath, &row); err != nil {
		return fail(err.Error())
	}
	if row.Environment == "" {
		return fail("设备 " + dev + " 没挂靠环境：带 --env / --enterprise / --device-type 拉起来")
	}
	base, err := envHTTPURL(listen, row.Environment)
	if err != nil {
		return fail(err.Error())
	}
	if token == "" {
		token = loadAppTokens()[row.Environment].AccessToken
	}
	if token == "" {
		return fail("没有 token：先 simctl login --env " + row.Environment + "，或带 --token")
	}

	evPath := ""
	lastSeq := 0
	if row.InstanceID != "" {
		evPath = idPath + "/events?instance_id=" + url.QueryEscape(row.InstanceID)
		lastSeq = maxEventSeq(listen, evPath, 0)
	}

	body := map[string]string{"DeviceID": dev}
	if identity != "" {
		body["IdentityID"] = identity
	}
	start := time.Now()
	var res appEnvelope
	if unbind {
		res, err = appCall(http.MethodDelete, base, "user/device/UnBind", token, nil, body, 10*time.Second)
	} else {
		// 服务端最多等设备 10s；App 给 32s。
		res, err = appCall(http.MethodPost, base, "user/device/Bind", token, nil, body, 32*time.Second)
	}
	if err != nil {
		return fail(err.Error())
	}
	result := map[string]any{
		"device_id": dev, "environment": row.Environment, "http_url": base,
		"connection_state": row.ConnectionState,
		"code":             res.Code, "message": res.Message,
		"elapsed_ms": time.Since(start).Milliseconds(),
	}
	if !unbind && evPath != "" {
		result["bind_received"] = maxEventSeq(listen, evPath, lastSeq, "bind_received") > lastSeq
	}
	q := url.Values{"PageNum": {"1"}, "PageSize": {"100"}}
	if lists, err := appCall(http.MethodGet, base, "user/device/Lists", token, q, nil, 10*time.Second); err == nil && lists.Code == 0 {
		result["in_list"] = bytes.Contains(lists.Data, []byte(`"`+dev+`"`))
	}
	return out(result)
}

// maxEventSeq 返回 after 之后（可按类型过滤）最大的 event_seq；没有则返回 after。
func maxEventSeq(listen, evPath string, after int, types ...string) int {
	var w struct {
		Events []struct {
			Seq  int    `json:"event_seq"`
			Type string `json:"event_type"`
		} `json:"events"`
	}
	if httpGet(listen, evPath+"&after_event_seq="+fmt.Sprint(after), &w) != nil {
		return after
	}
	best := after
	for _, e := range w.Events {
		if len(types) > 0 && e.Type != types[0] {
			continue
		}
		if e.Seq > best {
			best = e.Seq
		}
	}
	return best
}

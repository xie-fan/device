package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

// Cloud Mail（github.com/maillab/cloud-mail）开放 API：register 发完验证码后自己去收件箱取。
// 配置走环境变量：SIMCTL_MAIL_URL（站点根，如 https://mail.example.com），
// SIMCTL_MAIL_TOKEN，或 SIMCTL_MAIL_ADMIN + SIMCTL_MAIL_PASSWORD（管理员账号，现场换 token）。
// 注意：genToken 每调一次就顶掉上一个 token，别的脚本在用开放 API 时优先给 SIMCTL_MAIL_TOKEN。
type cloudMail struct {
	base, token string
}

type mailRow struct {
	EmailID int    `json:"emailId"`
	Subject string `json:"subject"`
	Text    string `json:"text"`
	Content string `json:"content"`
}

// newCloudMail 没配 SIMCTL_MAIL_URL 返回 nil：register 退回手动两步。
func newCloudMail() (*cloudMail, error) {
	base := strings.TrimRight(os.Getenv("SIMCTL_MAIL_URL"), "/")
	if base == "" {
		return nil, nil
	}
	m := &cloudMail{base: base, token: os.Getenv("SIMCTL_MAIL_TOKEN")}
	if m.token != "" {
		return m, nil
	}
	admin, pwd := os.Getenv("SIMCTL_MAIL_ADMIN"), os.Getenv("SIMCTL_MAIL_PASSWORD")
	if admin == "" || pwd == "" {
		return nil, fmt.Errorf("配了 SIMCTL_MAIL_URL，还要 SIMCTL_MAIL_TOKEN 或 SIMCTL_MAIL_ADMIN + SIMCTL_MAIL_PASSWORD")
	}
	var d struct{ Token string }
	if err := m.call("public/genToken", map[string]string{"email": admin, "password": pwd}, &d); err != nil {
		return nil, err
	}
	m.token = d.Token
	return m, nil
}

func (m *cloudMail) call(path string, body, data any) error {
	raw, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, m.base+"/api/"+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if m.token != "" {
		req.Header.Set("Authorization", m.token)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var env struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return fmt.Errorf("cloud mail %s: HTTP %d，响应不是 JSON", path, resp.StatusCode)
	}
	if env.Code != 200 {
		return fmt.Errorf("cloud mail %s: %d %s", path, env.Code, env.Message)
	}
	return json.Unmarshal(env.Data, data)
}

// latest 返回发给 to 的最新一封；收件箱为空时 EmailID=0。
func (m *cloudMail) latest(to string) (mailRow, error) {
	var rows []mailRow
	err := m.call("public/emailList", map[string]any{"toEmail": to, "size": 1, "timeSort": "desc"}, &rows)
	if err != nil || len(rows) == 0 {
		return mailRow{}, err
	}
	return rows[0], nil
}

// waitCode 轮询 after 之后新到的信，抽出验证码。
func (m *cloudMail) waitCode(to string, after int, timeout time.Duration) (string, mailRow, error) {
	deadline := time.Now().Add(timeout)
	for {
		r, err := m.latest(to)
		if err != nil {
			return "", r, err
		}
		if r.EmailID > after {
			if c := extractCode(r.Subject + "\n" + r.Text + "\n" + r.Content); c != "" {
				return c, r, nil
			}
			return "", r, fmt.Errorf("新邮件 %d（%s）里没找到验证码", r.EmailID, r.Subject)
		}
		if time.Now().After(deadline) {
			return "", r, fmt.Errorf("%s 内没收到发给 %s 的新邮件", timeout, to)
		}
		time.Sleep(3 * time.Second)
	}
}

var (
	tagRe     = regexp.MustCompile(`<[^>]*>`)
	nearKeyRe = regexp.MustCompile(`(?i)(?:验证码|code)[^0-9]{0,40}(\d{4,8})`)
	digitsRe  = regexp.MustCompile(`(?:^|[^0-9])(\d{4,8})(?:[^0-9]|$)`)
)

// extractCode 先找「验证码 / code」后面的数字，再退到第一串 4-8 位数字。
func extractCode(s string) string {
	s = tagRe.ReplaceAllString(s, " ")
	if m := nearKeyRe.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	if m := digitsRe.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}

package main

import "testing"

// 判错就是把无认证的控制面送出去，或者把本机可用的地址挡掉。两边都不能错。
func TestIsLoopbackListen(t *testing.T) {
	for _, c := range []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:8090", true},
		{"127.0.0.1", true},
		{"127.5.5.5:8090", true}, // 整个 127/8 都是回环
		{"localhost:8090", true},
		{"[::1]:8090", true},
		{"::1", true},
		{":8090", false},        // 空 host = 所有网卡
		{"0.0.0.0:8090", false}, // 显式所有网卡
		{"192.168.1.5:8090", false},
		{"[::]:8090", false},
		{"example.com:8090", false}, // 域名解析不了就当暴露，宁可误挡
	} {
		if got := isLoopbackListen(c.addr); got != c.want {
			t.Errorf("isLoopbackListen(%q)=%v，想要 %v", c.addr, got, c.want)
		}
	}
}

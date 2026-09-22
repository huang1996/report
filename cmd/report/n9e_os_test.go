package main

// n9e_os_test.go —— 操作系统名称组装单测
//
// 重点：categraf 在不同版本里把麒麟版本号上报成 v10 / V10 两种写法，
// 必须归一成同一形态，否则同一批主机会被分成两组、影响表 3 的分组统计。

import "testing"

func TestOSDisplay(t *testing.T) {
	cases := []struct {
		name string
		m    map[string]string
		want string
	}{
		{
			name: "版本号小写 v 归一为大写 V（categraf 实际上报 v10）",
			m:    map[string]string{"os_name": "kylin", "os_version": "v10"},
			want: "Kylin V10",
		},
		{
			name: "版本号已是大写 V 保持不变",
			m:    map[string]string{"os_name": "kylin", "os_version": "V10"},
			want: "Kylin V10",
		},
		{
			name: "无 os_version 时从内核版本推导（走 V 拼接路径）",
			m:    map[string]string{"os_name": "kylin", "kernel_version": "4.19.90-52.22.v2207.ky10.x86_64"},
			want: "Kylin V10",
		},
		{
			name: "Ubuntu 小写版本号首字母大写",
			m:    map[string]string{"os_name": "ubuntu", "os_version": "22.04"},
			want: "ubuntu 22.04", // 数字开头，不做改动
		},
		{
			name: "Windows 只返回名称（版本号含 build 时丢弃）",
			m: map[string]string{"os_name": "Microsoft Windows Server 2012 R2 Datacenter",
				"os_version": "6.3.9600 build 9600"},
			want: "Microsoft Windows Server 2012 R2 Datacenter",
		},
		{
			name: "os_name 为空返回空串（缺 system_info 的主机）",
			m:    map[string]string{},
			want: "",
		},
		{
			name: "openeuler 版本号保持原样",
			m:    map[string]string{"os_name": "openeuler", "os_version": "22.03"},
			want: "openeuler 22.03",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := osDisplay(c.m); got != c.want {
				t.Errorf("osDisplay(%v) = %q，期望 %q", c.m, got, c.want)
			}
		})
	}
}

// TestOSDisplayKylinCaseUnified 锁定「两种写法归一后必须相等」这一约束。
func TestOSDisplayKylinCaseUnified(t *testing.T) {
	a := osDisplay(map[string]string{"os_name": "kylin", "os_version": "v10"})
	b := osDisplay(map[string]string{"os_name": "Kylin", "os_version": "V10"})
	c := osDisplay(map[string]string{"os_name": "kylin", "kernel_version": "x.ky10.y"})
	if a != b || b != c {
		t.Errorf("麒麟 V10 三种来源未归一：v10=%q V10=%q kernel=%q", a, b, c)
	}
	if a != "Kylin V10" {
		t.Errorf("期望 Kylin V10，实际 %q", a)
	}
}

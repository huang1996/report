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
			name: "Windows 去掉厂商前缀并丢弃含 build 的版本号",
			m: map[string]string{"os_name": "Microsoft Windows Server 2012 R2 Datacenter",
				"os_version": "6.3.9600 build 9600"},
			want: "Windows Server 2012 R2 Datacenter",
		},
		{
			name: "Windows 前缀大小写不敏感",
			m:    map[string]string{"os_name": "microsoft Windows Server 2016 Standard"},
			want: "Windows Server 2016 Standard",
		},
		{
			name: "Windows 前缀后多个空格也剥离干净",
			m:    map[string]string{"os_name": "  Microsoft   Windows Server 2019  "},
			want: "Windows Server 2019",
		},
		{
			name: "Windows 无 os_version 时同样剥离前缀",
			m:    map[string]string{"os_name": "Microsoft Windows 10 Pro", "os_version": "10.0.19045"},
			want: "Windows 10 Pro",
		},
		{
			name: "形近前缀不误剥离（Microsoft-adjacent 不是厂商前缀）",
			m:    map[string]string{"os_name": "Microsoft-adjacent Linux", "os_version": "1.0"},
			want: "Microsoft-adjacent Linux 1.0",
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

// TestOSDisplayWindowsVendorPrefix 单独锁定厂商前缀剥离：categraf 上报的 os_name
// 形如 Microsoft Windows Server 2016 Standard，展示时应从 Windows 开始。
// 前缀的大小写与空格数不固定，且必须只精确匹配「Microsoft + 空白」——
// 形近的发行版名（如 MicrosoftLinux）不能被误伤。
func TestOSDisplayWindowsVendorPrefix(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Microsoft Windows Server 2016 Standard", "Windows Server 2016 Standard"},
		{"Microsoft Windows Server 2012 R2 Datacenter", "Windows Server 2012 R2 Datacenter"},
		{"microsoft Windows Server 2016 Standard", "Windows Server 2016 Standard"},
		{"MICROSOFT WINDOWS SERVER 2019", "WINDOWS SERVER 2019"},
		{"Microsoft   Windows Server 2022 Datacenter", "Windows Server 2022 Datacenter"},
		// 无紧跟空白 → 不剥离（TrimSpace 后已无尾空格，正则不匹配）
		{"Microsoft", "Microsoft 1"},
		// 形近但非厂商前缀 → 不剥离
		{"MicrosoftLinux", "MicrosoftLinux 1"},
		{"Microsoft-adjacent OS", "Microsoft-adjacent OS 1"},
	}
	for _, c := range cases {
		got := osDisplay(map[string]string{"os_name": c.in, "os_version": "1"})
		if got != c.want {
			t.Errorf("osDisplay(os_name=%q) = %q，期望 %q", c.in, got, c.want)
		}
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

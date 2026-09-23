package main

// n9e_arch_test.go —— CPU 架构解析（archOf）单元测试。
//
// 架构唯一来源是 system_info 的 kernel_version 标签（已枚举全部数据源的指标名与标签名，
// n9e 侧不存在 arch / os_arch / machine 等标签），因此这里覆盖各类真实内核串：
// CentOS/RHEL 的 .x86_64、麒麟的 .ky10.x86_64、openEuler 的 .aarch64，
// 以及不含架构信息的 Ubuntu（-generic）与 Windows 串。

import "testing"

func TestArchOf(t *testing.T) {
	cases := []struct {
		kernel string
		want   string
		note   string
	}{
		// —— 真实取值（来自 n9e 全量数据）——
		{"3.10.0-1127.8.2.el7.x86_64", "amd64", "CentOS 7"},
		{"3.10.0-1160.119.1.el7.x86_64", "amd64", "CentOS 7"},
		{"5.4.195-1.el7.elrepo.x86_64", "amd64", "CentOS 7 + elrepo 内核"},
		{"4.19.90-52.22.v2207.ky10.x86_64", "amd64", "银河麒麟 V10"},
		{"6.6.0-159.4.3.154.oe2403sp4.aarch64", "arm64", "openEuler 24.03 ARM"},
		{"6.8.0-60-generic", "", "Ubuntu 通用内核不含架构"},
		{"5.15.0-76-generic", "", "Ubuntu 通用内核不含架构"},
		{"10.0.14393 Build 14393", "", "Windows Server 2016"},
		{"6.3.9600 Build 9600", "", "Windows Server 2012 R2"},
		{"", "", "标签缺失"},
		// —— 构造样例（覆盖其余架构分支）——
		{"5.10.0-armv7l", "arm", "32 位 ARM"},
		{"5.10.0-armv8l", "arm", "ARMv8 32 位用户态"},
		{"4.14.0-115.el7a.ppc64le", "ppc64le", "POWER 小端"},
		{"5.4.0-s390x", "s390x", "IBM Z"},
		{"5.19.0-loongarch64", "loong64", "龙芯"},
		{"6.1.0-riscv64", "riscv64", "RISC-V"},
		{"4.19.90-ky10.sw_64", "sw64", "申威"},
		{"4.18.0-i686", "386", "32 位 x86"},
	}
	for _, c := range cases {
		if got := archOf(c.kernel); got != c.want {
			t.Errorf("archOf(%q) = %q，期望 %q（%s）", c.kernel, got, c.want, c.note)
		}
	}
}

// TestArchOfPriority 更具体的标记必须优先命中：
// aarch64 不能退化成 arm64 之外的值、ppc64le 不能被 ppc64 抢先。
func TestArchOfPriority(t *testing.T) {
	cases := []struct{ kernel, want string }{
		{"5.10.0-aarch64", "arm64"},
		{"5.10.0-arm64", "arm64"},
		{"4.14.0-ppc64le", "ppc64le"},
		{"4.14.0-ppc64", "ppc64"},
		{"4.14.0-mips64el", "mips64le"},
		{"4.14.0-mips64", "mips64"},
	}
	for _, c := range cases {
		if got := archOf(c.kernel); got != c.want {
			t.Errorf("archOf(%q) = %q，期望 %q", c.kernel, got, c.want)
		}
	}
}

// TestArchOfCaseInsensitive 大小写与首尾空格都应容错（不同 categraf 版本上报格式并不统一）。
func TestArchOfCaseInsensitive(t *testing.T) {
	cases := []struct{ kernel, want string }{
		{"5.10.0-AARCH64", "arm64"},
		{"5.10.0-X86_64", "amd64"},
		{" 6.8.0-60-generic ", ""},
		{"   ", ""},
	}
	for _, c := range cases {
		if got := archOf(c.kernel); got != c.want {
			t.Errorf("archOf(%q) = %q，期望 %q", c.kernel, got, c.want)
		}
	}
}

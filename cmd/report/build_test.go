package main

// build_test.go —— 报告组装层的单元测试（页眉版本号、表号计数、容量单位换算）。

import (
	"strings"
	"testing"
)

// TestHeaderVersion 页眉需带程序版本号，格式「<页眉>-0.1.x」。
// 断言不写死具体版本号，避免每次发布都要改测试。
func TestHeaderVersion(t *testing.T) {
	ver := strings.TrimLeft(strings.TrimSpace(AppVersion), "vV")
	if ver == "" {
		t.Fatal("AppVersion 为空，无法验证页眉格式")
	}

	cases := []struct{ in, base string }{
		{"统筹运维项目", "统筹运维项目"},
		{"", "统筹运维项目"},       // 空值回落默认页眉
		{"   ", "统筹运维项目"},    // 纯空白同样回落
		{"泸州市政务云", "泸州市政务云"}, // 自定义页眉
	}
	for _, c := range cases {
		got := headerVersion(c.in)
		want := c.base + "-" + ver
		if got != want {
			t.Errorf("headerVersion(%q) = %q，期望 %q", c.in, got, want)
		}
		// 版本号不得带 v 前缀（格式约定是「-0.1.x」而非「-v0.1.x」）
		if strings.Contains(got, "-v") || strings.Contains(got, "-V") {
			t.Errorf("headerVersion(%q) = %q，版本号不应带 v 前缀", c.in, got)
		}
	}
}

// TestHeaderVersionStripsVPrefix 注入的 AppVersion 自带 v 前缀时（CI 用 tag 注入）也要归一。
func TestHeaderVersionStripsVPrefix(t *testing.T) {
	old := AppVersion
	defer func() { AppVersion = old }()

	for _, v := range []string{"v0.1.5", "V0.1.5", "0.1.5"} {
		AppVersion = v
		if got := headerVersion("统筹运维项目"); got != "统筹运维项目-0.1.5" {
			t.Errorf("AppVersion=%q 时页眉 = %q，期望 %q", v, got, "统筹运维项目-0.1.5")
		}
	}
}

// TestTableNo 表号按调用顺序递增，保证章节可选（业务巡检 / WAF 缺失）时表号仍连续。
func TestTableNo(t *testing.T) {
	tn := &tableNo{}
	for i := 1; i <= 13; i++ {
		if got := tn.next(); got != i {
			t.Errorf("第 %d 次 next() = %d，期望 %d", i, got, i)
		}
	}
}

// TestFmtGB 容量显示：满 1 TB 换算为 TB，避免出现「12275 GB」这类大数。
func TestFmtGB(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0, "0 GB"},
		{38.26, "38 GB"},
		{530.29, "530 GB"},
		{1023, "1023 GB"},
		{1024, "1.0 TB"},
		{12275, "12.0 TB"},
		{2047.0, "2.0 TB"},
	}
	for _, c := range cases {
		if got := fmtGB(c.in); got != c.want {
			t.Errorf("fmtGB(%.2f) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

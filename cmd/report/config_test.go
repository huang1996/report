package main

// config_test.go —— 命令行参数解析相关测试
//
// 重点覆盖布尔参数的两种写法：`-flag`（标准写法）与 `-flag true`（空格分隔给值）。
// 后者在标准库里会导致解析在 `true` 处提前终止，其后的参数被静默丢弃。

import (
	"os"
	"testing"
)

func TestBoolLiteral(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"true", "true", true},
		{"TRUE", "true", true},
		{"1", "true", true},
		{"yes", "true", true},
		{"on", "true", true},
		{"false", "false", true},
		{"False", "false", true},
		{"0", "false", true},
		{"no", "false", true},
		{"off", "false", true},
		{"  true  ", "true", true},
		{"10.20.30.11", "", false},
		{"INFO", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, ok := boolLiteral(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("boolLiteral(%q) = (%q,%v)，期望 (%q,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestNormalizeBoolArgs(t *testing.T) {
	boolFlags := map[string]bool{"now": true, "fake_when_empty": true}

	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "布尔参数带空格值被归一化",
			in:   []string{"-fake_when_empty", "true"},
			want: []string{"-fake_when_empty=true"},
		},
		{
			name: "布尔参数带空格假值被归一化",
			in:   []string{"-fake_when_empty", "false", "-now"},
			want: []string{"-fake_when_empty=false", "-now"},
		},
		{
			name: "已用等号写法保持不变",
			in:   []string{"-now=true"},
			want: []string{"-now=true"},
		},
		{
			name: "非布尔参数的值即使是 true 也不动",
			in:   []string{"-n9e_base", "https://example.com", "-report_name", "true"},
			want: []string{"-n9e_base", "https://example.com", "-report_name", "true"},
		},
		{
			name: "布尔参数后面跟的是另一个参数时不合并",
			in:   []string{"-now", "-fake_when_empty"},
			want: []string{"-now", "-fake_when_empty"},
		},
		{
			name: "单独的短横线不处理",
			in:   []string{"-", "-now"},
			want: []string{"-", "-now"},
		},
	}
	for _, c := range cases {
		got := normalizeBoolArgs(c.in, boolFlags)
		if len(got) != len(c.want) {
			t.Fatalf("%s：得到 %v，期望 %v", c.name, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s：第 %d 项得到 %q，期望 %q（全量：%v）", c.name, i, got[i], c.want[i], got)
			}
		}
	}
}

// TestParseConfigBoolFlagAfterStringValue 复现真实场景：
// `-report_include_biz true` 之后的参数此前会被全部忽略（含 -fake_when_empty）
func TestParseConfigBoolFlagAfterStringValue(t *testing.T) {
	t.Setenv("N9E_DS_ID", "")
	old := os.Args
	t.Cleanup(func() { os.Args = old })
	os.Args = []string{"report.exe",
		"-n9e_base", "https://n9e.example.com/",
		"-n9e_ds_id", "58",
		"-start", "2026-04-16", "-end", "2026-09-18", "-now",
		"-report_name", "泸州市河湖长制信息系统巡检周报",
		"-report_engineer", "黄桃",
		"-report_dir", "泸州市河湖长制信息系统",
		"-report_include_biz", "true",
		"-fake_when_empty",
	}

	cfg := parseConfig()

	if !cfg.FakeWhenEmpty {
		t.Error("-fake_when_empty 未被解析（位于 -report_include_biz true 之后，应被归一化后照常生效）")
	}
	if !cfg.IncludeBiz {
		t.Error("-report_include_biz true 未生效")
	}
	if !cfg.Now {
		t.Error("-now 未生效")
	}
	if cfg.DS != "58" || !cfg.DSExplicit {
		t.Errorf("数据源解析异常：DS=%q DSExplicit=%v", cfg.DS, cfg.DSExplicit)
	}
	if cfg.Start != "2026-04-16" || cfg.End != "2026-09-18" {
		t.Errorf("巡检窗口解析异常：%s ~ %s", cfg.Start, cfg.End)
	}
	if cfg.Engineer != "黄桃" {
		t.Errorf("工程师解析异常：%q", cfg.Engineer)
	}
	if cfg.ReportDir != "泸州市河湖长制信息系统" {
		t.Errorf("输出目录解析异常：%q", cfg.ReportDir)
	}
	if len(cfg.UnparsedArgs) != 0 {
		t.Errorf("存在未被识别的参数：%v", cfg.UnparsedArgs)
	}
}

func TestParseConfigBoolFlagExplicitFalse(t *testing.T) {
	old := os.Args
	t.Cleanup(func() { os.Args = old })
	os.Args = []string{"report.exe", "-demo", "-fake_when_empty=false", "-report_include_biz", "0"}

	cfg := parseConfig()

	if cfg.FakeWhenEmpty {
		t.Error("-fake_when_empty=false 应解析为假")
	}
	if cfg.IncludeBiz {
		t.Error("-report_include_biz 0 应解析为假")
	}
	if !cfg.Demo {
		t.Error("-demo 未生效")
	}
}

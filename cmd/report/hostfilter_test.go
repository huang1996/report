package main

// hostfilter_test.go —— 服务器巡检排除名单：解析校验、行过滤，以及报告内不留痕

import (
	"strings"
	"testing"
	"time"
)

func TestValidIPs(t *testing.T) {
	ok, bad := validIPs([]string{
		" 10.0.0.1 ", "10.0.0.1", "10.0.0.2", "::1", "fe80::1", "", "192.168.1.0/24", "web-01",
	})
	wantOK := []string{"10.0.0.1", "10.0.0.2", "::1", "fe80::1"}
	if len(ok) != len(wantOK) {
		t.Fatalf("合法 IP 数 = %d，期望 %d：%v", len(ok), len(wantOK), ok)
	}
	for i, w := range wantOK {
		if ok[i] != w {
			t.Errorf("第 %d 个合法 IP = %q，期望 %q", i, ok[i], w)
		}
	}
	// 网段与主机名都不接受：网段含义不明确，主机名可能对应多台
	if len(bad) != 2 || bad[0] != "192.168.1.0/24" || bad[1] != "web-01" {
		t.Errorf("非法项 = %v，期望 [192.168.1.0/24 web-01]", bad)
	}
	if ok, bad := validIPs(nil); len(ok) != 0 || len(bad) != 0 {
		t.Errorf("空配置应返回空，实得 %v / %v", ok, bad)
	}
}

func TestExcludeHosts(t *testing.T) {
	rows := []HostRow{
		{IP: "10.0.0.1"}, {IP: "10.0.0.2"}, {IP: "10.0.0.3"}, {IP: "10.0.0.1"},
	}
	kept, hit, missing := excludeHosts(rows, []string{"10.0.0.1", "10.9.9.9"})
	if len(kept) != 2 || kept[0].IP != "10.0.0.2" || kept[1].IP != "10.0.0.3" {
		t.Errorf("保留行不符：%v", kept)
	}
	// 同一 IP 多行只记一次
	if len(hit) != 1 || hit[0] != "10.0.0.1" {
		t.Errorf("命中列表 = %v，期望 [10.0.0.1]", hit)
	}
	if len(missing) != 1 || missing[0] != "10.9.9.9" {
		t.Errorf("未命中列表 = %v，期望 [10.9.9.9]", missing)
	}

	// 空名单：原样返回，不做任何拷贝外处理
	kept, hit, missing = excludeHosts(rows, nil)
	if len(kept) != len(rows) || hit != nil || missing != nil {
		t.Errorf("空名单应原样返回，实得 %d 行 / %v / %v", len(kept), hit, missing)
	}
}

// TestExcludeHostsInReport 排除后：表格与正文都不再出现该 IP、主机数减少，且报告不写排除说明
func TestExcludeHostsInReport(t *testing.T) {
	old := log
	log = &Logger{level: 3}
	defer func() { log = old }()

	all := demoRows()
	total := len(all)
	excluded := []string{all[0].IP, all[1].IP}
	kept, hit, missing := excludeHosts(all, excluded)
	if len(hit) != 2 || len(missing) != 0 {
		t.Fatalf("命中 / 未命中 = %v / %v", hit, missing)
	}

	dir := t.TempDir()
	now := time.Date(2026, 9, 23, 0, 0, 0, 0, time.Local)
	out, err := BuildReport(&BuildInput{
		Cfg:  &Config{ReportDir: dir, Out: JoinPath(dir, "r.docx"), ChartTop: 5, HeaderText: "统筹运维项目"},
		Rows: kept,
		Waf:  demoWaf(),
		WS:   now.AddDate(0, 0, -4),
		WE:   now,
	})
	if err != nil {
		t.Fatalf("生成报告失败：%v", err)
	}

	// 表格与正文里都不该再出现被排除的主机；报告内不写排除说明，只呈现统计结果
	doc := docxPart(t, out, "word/document.xml")
	for _, tb := range reTable.FindAllString(doc, -1) {
		for _, ip := range excluded {
			if strings.Contains(tb, ip) {
				t.Errorf("表格里仍出现被排除的主机 %s", ip)
			}
		}
	}
	text := docText(t, out)
	for _, ip := range excluded {
		if strings.Contains(text, ip) {
			t.Errorf("报告正文仍出现被排除的主机 %s", ip)
		}
	}
	if strings.Contains(text, "排除") {
		t.Error("报告里不应出现排除说明")
	}
	if !strings.Contains(text, sprintf("%d 台主机", total-len(excluded))) {
		t.Errorf("巡检对象应为 %d 台主机，实际文本未找到", total-len(excluded))
	}
}

// TestFilterBizByIPs 排除 IP 上的业务组件同样跳过：探活按地址主机名、实例按实例名里的地址
func TestFilterBizByIPs(t *testing.T) {
	b := &BizData{
		Probes: []BizProbe{
			{Name: "裸 IP", Addr: "172.16.1.2"},
			{Name: "带端口", Addr: "172.16.1.3:8443"},
			{Name: "域名", Addr: "https://www.example.com/portal"},
		},
		Comps: []BizComponent{
			{Name: "Redis", Inst: BizInstance{Raw: "河湖-redis-172.16.1.2:6379", Addr: "172.16.1.2:6379"}},
			{Name: "PostgreSQL", Inst: BizInstance{Raw: "河湖-pg-172.16.2.1:5432", Addr: "172.16.2.1:5432"}},
			{Name: "MySQL", Inst: BizInstance{Raw: "河湖-mysql-db.example.com:3306", Addr: "db.example.com:3306"}},
		},
	}
	if n := filterBizByIPs(b, []string{"172.16.1.2"}); n != 2 {
		t.Errorf("应跳过 2 个组件（1 探活 + 1 实例），实得 %d", n)
	}
	if len(b.Probes) != 2 || b.Probes[0].Name != "带端口" || b.Probes[1].Name != "域名" {
		t.Errorf("剩余探活不符：%+v", b.Probes)
	}
	if len(b.Comps) != 2 || b.Comps[0].Name != "PostgreSQL" || b.Comps[1].Name != "MySQL" {
		t.Errorf("剩余实例不符：%+v", b.Comps)
	}
	// 域名类组件不受 IP 名单影响；空数据与空名单都安全
	if n := filterBizByIPs(nil, []string{"10.0.0.1"}); n != 0 {
		t.Error("nil 业务数据应返回 0")
	}
	if n := filterBizByIPs(b, nil); n != 0 {
		t.Error("空名单应返回 0")
	}
}

// TestFilterBizByIPsRefreshesFindings 过滤后必须重算巡检发现，否则风险台账会残留被排除实例的条目
func TestFilterBizByIPsRefreshesFindings(t *testing.T) {
	b := &BizData{Comps: []BizComponent{
		{
			Name: "Redis", Status: bizWarn,
			Inst: BizInstance{Raw: "河湖-redis-172.16.1.2:6379", Addr: "172.16.1.2:6379"},
			Rows: []BizRow{{Label: "内存碎片率", Value: "2.57", Thresh: "＜ 1.5", Status: bizWarn}},
		},
		{
			Name: "PostgreSQL", Status: bizNormal,
			Inst: BizInstance{Raw: "河湖-pg-172.16.2.1:5432", Addr: "172.16.2.1:5432"},
			Rows: []BizRow{{Label: "连接数", Value: "12", Thresh: "趋势观察", Status: bizNormal}},
		},
	}}
	b.Findings = bizFindings(b) // 采集阶段已算过一次
	if len(bizWarnings(b.Findings)) != 1 {
		t.Fatalf("过滤前应有 1 条发现，实得 %v", b.Findings)
	}
	if n := filterBizByIPs(b, []string{"172.16.1.2"}); n != 1 {
		t.Fatalf("应跳过 1 个组件，实得 %d", n)
	}
	if len(bizWarnings(b.Findings)) != 0 {
		t.Errorf("过滤后不应残留被排除实例的发现：%+v", b.Findings)
	}
}

// bizWarnings 统计等级为关注/告警的发现（与风险台账口径一致）
func bizWarnings(fs []BizFinding) []BizFinding {
	var out []BizFinding
	for _, f := range fs {
		if f.Level == bizWarn || f.Level == bizAlert {
			out = append(out, f)
		}
	}
	return out
}

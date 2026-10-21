package main

// biz_test.go —— 业务巡检：实例解析、状态判定、章节渲染与「留空」行为

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestParseBizInstance(t *testing.T) {
	cases := []struct {
		in                        string
		system, typ, addr, rawTag string
	}{
		{"天空地森林防火-mysql-10.82.1.2:13306", "天空地森林防火", "mysql", "10.82.1.2:13306", ""},
		{"智慧民政-redis-cache-01:6379", "智慧民政", "redis", "cache-01:6379", ""},
		{"only-system", "only", "system", "", ""},
		{"sys-nginx", "sys", "nginx", "", ""},
	}
	for _, c := range cases {
		got := parseBizInstance(c.in)
		if got.System != c.system || got.Type != c.typ || got.Addr != c.addr {
			t.Errorf("parseBizInstance(%q) = {%q,%q,%q}，期望 {%q,%q,%q}",
				c.in, got.System, got.Type, got.Addr, c.system, c.typ, c.addr)
		}
		if got.Raw != c.in {
			t.Errorf("Raw 应保留原值，实得 %q", got.Raw)
		}
	}
}

func TestBizProbeStatus(t *testing.T) {
	cases := []struct {
		name string
		p    BizProbe
		want string
	}{
		{"全部正常", BizProbe{Avail: 100, Avg: 30, Max: 80, Std: 6}, bizNormal},
		{"响应偏慢", BizProbe{Avail: 100, Avg: 600, Max: 900, Std: 30}, bizWarn},
		{"标准差偏大", BizProbe{Avail: 100, Avg: 100, Max: 400, Std: 260}, bizWarn},
		{"可用率不足", BizProbe{Avail: 98, Avg: 30, Max: 80, Std: 5}, bizAlert},
		{"最大响应过高", BizProbe{Avail: 100, Avg: 400, Max: 6000, Std: 50}, bizAlert},
	}
	for _, c := range cases {
		if got := bizProbeStatus(c.p); got != c.want {
			t.Errorf("%s：状态 = %q，期望 %q", c.name, got, c.want)
		}
	}
}

func TestBizWorstRanking(t *testing.T) {
	if got := bizWorst(bizNormal, bizWarn, bizAlert); got != bizAlert {
		t.Errorf("三项混合应取告警，实得 %q", got)
	}
	if got := bizWorst(bizNormal, bizWarn); got != bizWarn {
		t.Errorf("正常+关注应取关注，实得 %q", got)
	}
	if got := bizWorst(bizNormal); got != bizNormal {
		t.Errorf("仅正常应为正常，实得 %q", got)
	}
}

func TestBizStdText(t *testing.T) {
	rows := []BizRow{
		{Label: "连接使用率", Thresh: "＜ 80%"},
		{Label: "QPS", Thresh: "趋势观察"},
		{Label: "内存使用", Thresh: "占系统内存 ＜ 70%"},
	}
	got := bizStdText(rows)
	for _, want := range []string{"连接使用率 ＜ 80%", "QPS 仅作趋势观察", "内存使用 占系统内存 ＜ 70%", "状态取上述各项中最差的一项"} {
		if !strings.Contains(got, want) {
			t.Errorf("判断标准缺少 %q，实得：%s", want, got)
		}
	}
	// 全部无阈值时退化为「无固定阈值」说明
	allTrend := bizStdText([]BizRow{{Label: "活跃连接", Thresh: "趋势观察"}, {Label: "空闲连接", Thresh: "—"}})
	if !strings.Contains(allTrend, "无固定阈值") {
		t.Errorf("全为观测值时应给出无固定阈值说明，实得：%s", allTrend)
	}
}

func TestBizFindings(t *testing.T) {
	b := &BizData{
		Comps: []BizComponent{
			demoBizComp("mysql", "MySQL", "数据库", "sys-mysql-10.0.0.1:3306", []BizRow{
				{Label: "磁盘临时表（/s）", Value: "1.13", Thresh: "＜ 1", Status: bizWarn, Key: true},
				{Label: "连接使用率", Value: "7.29%", Thresh: "＜ 80%", Status: bizNormal, Key: true},
			}),
		},
		Probes: []BizProbe{{System: "sys", Name: "api", Addr: "http://10.0.0.2:8080", Avail: 100, Avg: 30, Status: bizNormal}},
	}
	fs := bizFindings(b)
	if len(fs) != 1 {
		t.Fatalf("应产出 1 条发现，实得 %d 条：%+v", len(fs), fs)
	}
	if fs[0].Level != bizWarn || !strings.Contains(fs[0].Desc, "磁盘临时表") {
		t.Errorf("发现内容不符：%+v", fs[0])
	}
	if fs[0].Advice == "" {
		t.Error("发现项应带处置建议")
	}
}

func TestBizFindingsAllNormal(t *testing.T) {
	b := &BizData{
		Comps: []BizComponent{demoBizComp("nginx", "Nginx", "中间件", "sys-nginx-10.0.0.3:80", []BizRow{
			{Label: "活跃连接", Value: "10", Thresh: "趋势观察", Status: bizNormal, Key: true},
		})},
	}
	fs := bizFindings(b)
	if len(fs) != 1 || fs[0].Level != bizNormal {
		t.Fatalf("全部正常时应给一条正向结论，实得 %+v", fs)
	}
}

// buildBizDocx 用演示资源数据 + 指定业务数据生成报告
func buildBizDocx(t *testing.T, biz *BizData, includeBiz bool) string {
	t.Helper()
	old := log
	log = &Logger{level: 3}
	defer func() { log = old }()

	dir := t.TempDir()
	now := time.Date(2026, 10, 21, 0, 0, 0, 0, time.Local)
	cfg := &Config{ReportDir: dir, Out: JoinPath(dir, "report.docx"), ChartTop: 5,
		HeaderText: "统筹运维项目", IncludeBiz: includeBiz}
	out, err := BuildReport(&BuildInput{
		Cfg: cfg, Rows: demoRows(), Biz: biz, Waf: demoWaf(),
		WS: now.AddDate(0, 0, -4), WE: now,
	})
	if err != nil {
		t.Fatalf("生成报告失败：%v", err)
	}
	return out
}

// docText 按文档顺序拼出所有段落文本（含表格内文本）
func docText(t *testing.T, path string) string {
	t.Helper()
	doc := docxPart(t, path, "word/document.xml")
	var b strings.Builder
	for _, m := range rePara.FindAllString(doc, -1) {
		for _, tm := range reText.FindAllStringSubmatch(m, -1) {
			b.WriteString(tm[1])
			b.WriteString("\n")
		}
	}
	return b.String()
}

// TestBizChapterRendered 打开开关且有数据时：业务巡检章节完整渲染，且位于 WAF 之前
func TestBizChapterRendered(t *testing.T) {
	out := buildBizDocx(t, demoBiz(), true)
	text := docText(t, out)

	for _, want := range []string{"4、业务巡检", "4.1 业务组件概览", "4.2 应用探活",
		"4.3 MySQL 数据库", "4.4 PostgreSQL 数据库", "4.5 Redis 缓存", "4.6 Nginx 中间件",
		"4.3.1 详细分析：天空地森林防火-mysql-10.82.1.2:13306",
		"关键指标", "业务组件概览"} {
		if !strings.Contains(text, want) {
			t.Errorf("业务巡检章节缺少 %q", want)
		}
	}
	idxBiz := strings.Index(text, "4、业务巡检")
	idxWaf := strings.Index(text, "Web 应用防火墙安全巡检")
	if idxBiz < 0 || idxWaf < 0 {
		t.Fatalf("未找到业务巡检或 WAF 章节标题（biz=%d, waf=%d）", idxBiz, idxWaf)
	}
	if idxBiz > idxWaf {
		t.Error("业务巡检章节应排在 WAF 巡检之前")
	}
}

// TestBizChapterBlankWhenNoData 开关打开但无任何探活 / 中间件数据时，章节保留但内容留空
func TestBizChapterBlankWhenNoData(t *testing.T) {
	out := buildBizDocx(t, nil, true)
	text := docText(t, out)
	if !strings.Contains(text, "4、业务巡检") {
		t.Fatal("开关打开时章节标题应存在")
	}
	if strings.Contains(text, "业务组件概览") || strings.Contains(text, "应用探活关键指标") {
		t.Error("无数据时不应渲染业务巡检内容")
	}
	// WAF 章节仍应正常衔接编号
	if !strings.Contains(text, "5、Web 应用防火墙安全巡检") {
		t.Error("业务巡检章节占位后，WAF 章节应从 5 开始")
	}
}

// TestBizChapterAbsentByDefault 开关关闭时不出现业务巡检章节（默认行为不变）
func TestBizChapterAbsentByDefault(t *testing.T) {
	out := buildBizDocx(t, demoBiz(), false)
	text := docText(t, out)
	if strings.Contains(text, "业务巡检") {
		t.Error("未打开 -report_include_biz 时不应出现业务巡检章节")
	}
	if !strings.Contains(text, "4、Web 应用防火墙安全巡检") {
		t.Error("跳过业务巡检时 WAF 章节应为 4")
	}
}

// TestBizInstanceNaming 报告中实例名保持「业务系统简称-类型-地址」原样展示
func TestBizInstanceNaming(t *testing.T) {
	out := buildBizDocx(t, demoBiz(), true)
	text := docText(t, out)
	for _, want := range []string{
		"天空地森林防火-mysql-10.82.1.2:13306",
		"天空地森林防火-redis-10.82.1.2:26739",
		"天空地森林防火-nginx-10.82.1.2:9527",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("实例名 %q 未按约定展示", want)
		}
	}
}

// TestBizFigureNumbering 资源图表 5 张之后依次是：探活图 6 → 各实例趋势图 → WAF 饼图，编号不得跳号
func TestBizFigureNumbering(t *testing.T) {
	out := buildBizDocx(t, demoBiz(), true)
	text := docText(t, out)
	if !strings.Contains(text, "图 6　各探活目标") {
		t.Errorf("业务探活图表应为图 6（资源图表 5 张之后），文档中未找到")
	}
	for _, want := range []struct{ no, title string }{
		{"图 7　", "QPS 与连接使用率趋势"},
		{"图 8　", "各库事务提交速率趋势"},
		{"图 9　", "吞吐量与内存占用趋势"},
	} {
		if !strings.Contains(text, want.no) || !strings.Contains(text, want.title) {
			t.Errorf("未找到实例趋势图 %s%s", want.no, want.title)
		}
	}
	if !strings.Contains(text, "图 10 / 表") {
		t.Errorf("WAF 饼图应顺延为图 10，文档中未找到")
	}
}

// TestBizChartNumbersSequential 图表编号从 1 起连续（资源 5 张 + 探活 1 张 + 实例趋势图 + WAF 1 张）
func TestBizChartNumbersSequential(t *testing.T) {
	out := buildBizDocx(t, demoBiz(), true)
	text := docText(t, out)
	var nums []int
	for _, ln := range strings.Split(text, "\n") {
		ln = strings.TrimSpace(ln)
		idx := strings.Index(ln, "图 ")
		if idx < 0 {
			continue
		}
		rest := ln[idx+len("图 "):]
		n, err := strconv.Atoi(strings.TrimSpace(strings.SplitN(rest, "　", 2)[0]))
		if err != nil {
			// 「图 N / 表 M」形态
			n, err = strconv.Atoi(strings.TrimSpace(strings.SplitN(rest, " ", 2)[0]))
			if err != nil {
				continue
			}
		}
		nums = append(nums, n)
	}
	if len(nums) < 6 {
		t.Fatalf("图表数量异常：%v", nums)
	}
	for i, n := range nums {
		if n != i+1 {
			t.Fatalf("图表编号不连续：第 %d 个为 %d，完整序列 %v", i+1, n, nums)
		}
	}
}

// TestBizComponentCharts 各类型的趋势图配置：MySQL 双轴阈值、Redis 内存填充、PG 堆叠分库
func TestBizComponentCharts(t *testing.T) {
	b := demoBiz()
	for _, c := range b.Comps {
		if len(c.Charts) == 0 {
			continue
		}
		bc := c.Charts[0]
		if len(bc.Series) < 2 && c.Kind != "postgresql" {
			t.Errorf("%s 趋势图序列数不足：%d", c.Kind, len(bc.Series))
		}
		for _, sr := range bc.Series {
			if len(sr.Points) < 2 {
				t.Errorf("%s 趋势图序列 %q 采样点不足", c.Kind, sr.Name)
			}
		}
		switch c.Kind {
		case "mysql":
			if bc.WarnLine != 80 || bc.WarnAxis != 1 {
				t.Errorf("MySQL 趋势图应带 80%% 关注线且位于右轴，实得 %v/%d", bc.WarnLine, bc.WarnAxis)
			}
			if bc.Series[1].Axis != 1 {
				t.Error("MySQL 连接使用率应走右轴")
			}
		case "redis":
			if !bc.Series[1].Fill {
				t.Error("Redis 内存占用应做面积填充")
			}
			if bc.Series[1].Axis != 1 {
				t.Error("Redis 内存占用应走右轴")
			}
		case "postgresql":
			if !bc.Stacked {
				t.Error("PostgreSQL 各库事务提交速率应为堆叠填充")
			}
			if len(bc.Series) < 2 {
				t.Errorf("PostgreSQL 堆叠图应含多个库，实得 %d 个", len(bc.Series))
			}
		}
	}
}

// TestBizKeyTable 关键指标表：表头为 实例 + 5 个关键指标 + 状态，且表下有判断标准
func TestBizKeyTable(t *testing.T) {
	out := buildBizDocx(t, demoBiz(), true)
	doc := docxPart(t, out, "word/document.xml")
	found := false
	for _, tb := range reTable.FindAllString(doc, -1) {
		h := tableHeader(tb)
		if strings.HasPrefix(h, "实例|") && strings.HasSuffix(h, "|状态") && strings.Count(h, "|") == 6 {
			found = true
			if cols := strings.Split(h, "|"); len(cols) != 7 {
				t.Errorf("关键指标表应为「实例 + 5 指标 + 状态」共 7 列，实得 %d 列：%s", len(cols), h)
			}
		}
	}
	if !found {
		t.Error("未找到「实例 + 5 指标 + 状态」的关键指标表")
	}
	text := docText(t, out)
	if !strings.Contains(text, "判断标准：连接使用率") {
		t.Error("MySQL 关键指标表下缺少判断标准")
	}
}

// TestBizConclusion 总体结论的业务要点：覆盖系统、探活、实例越线摘要，并给出汇总状态
func TestBizConclusion(t *testing.T) {
	text, level := bizConclusion(demoBiz())
	for _, want := range []string{"业务巡检覆盖", "应用探活", "MySQL", "越线项"} {
		if !strings.Contains(text, want) {
			t.Errorf("业务结论缺少 %q，实得：%s", want, text)
		}
	}
	if level != bizWarn {
		t.Errorf("演示数据含关注项，汇总状态应为「关注」，实得 %q", level)
	}
}

// TestOverallConclusionIncludesBiz 第 2 章总体结论要带业务巡检要点；开关关闭时不得出现
func TestOverallConclusionIncludesBiz(t *testing.T) {
	on := docText(t, buildBizDocx(t, demoBiz(), true))
	if !strings.Contains(on, "业务巡检覆盖") {
		t.Error("开启业务巡检时，总体结论应包含业务要点")
	}
	if !strings.Contains(on, "综合评级") {
		t.Error("总体结论缺少综合评级行")
	}

	off := docText(t, buildBizDocx(t, demoBiz(), false))
	if strings.Contains(off, "业务巡检覆盖") {
		t.Error("未开启业务巡检时，总体结论不应出现业务要点")
	}
}

// TestBizConclusionAllNormal 全部正常时给正向结论，状态为正常
func TestBizConclusionAllNormal(t *testing.T) {
	b := &BizData{
		Probes: []BizProbe{{System: "A", Name: "探活1", Avail: 100, Avg: 8, Status: bizNormal}},
		Comps: []BizComponent{{Kind: "mysql", Name: "MySQL", Inst: parseBizInstance("A-mysql-1.1.1.1:3306"),
			Status: bizNormal, Rows: []BizRow{{Label: "连接使用率", Value: "5%", Status: bizNormal}}}},
	}
	text, level := bizConclusion(b)
	if level != bizNormal {
		t.Errorf("全部正常时状态应为正常，实得 %q", level)
	}
	if !strings.Contains(text, "均在参考阈值内") {
		t.Errorf("全部正常时应给出正向结论，实得：%s", text)
	}
}

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

// bizCertDemo 生成「只有探活数据」的业务数据，状态按真实规则推导
func bizCertDemo(probes []BizProbe) *BizData {
	b := &BizData{Probes: probes}
	for i := range b.Probes {
		b.Probes[i].Status = bizProbeStatus(b.Probes[i])
	}
	b.Findings = bizFindings(b)
	return b
}

// TestBizProbeCertStatus 证书剩余有效期判定：＜30 天关注、已过期告警、无证书信息不影响状态
func TestBizProbeCertStatus(t *testing.T) {
	cases := []struct {
		name string
		p    BizProbe
		want string
	}{
		{"无证书信息", BizProbe{Avail: 100, Avg: 30}, bizNormal},
		{"剩余 186 天", BizProbe{Avail: 100, Avg: 30, CertExpire: 1, CertDays: 186}, bizNormal},
		{"剩余 30 天", BizProbe{Avail: 100, Avg: 30, CertExpire: 1, CertDays: 30}, bizNormal},
		{"剩余 13 天", BizProbe{Avail: 100, Avg: 30, CertExpire: 1, CertDays: 13}, bizWarn},
		{"当日到期", BizProbe{Avail: 100, Avg: 30, CertExpire: 1, CertDays: 0}, bizWarn},
		{"已过期", BizProbe{Avail: 100, Avg: 30, CertExpire: 1, CertDays: -2}, bizAlert},
		{"响应异常叠加证书临期", BizProbe{Avail: 98, Avg: 30, CertExpire: 1, CertDays: 13}, bizAlert},
	}
	for _, c := range cases {
		if got := bizProbeStatus(c.p); got != c.want {
			t.Errorf("%s：状态 = %q，期望 %q", c.name, got, c.want)
		}
	}
}

// TestBizCertText 证书到期列文案：默认只给日期，临期补剩余天数，过期标明
func TestBizCertText(t *testing.T) {
	day := time.Date(2026, 11, 3, 0, 0, 0, 0, time.Local).Unix()
	cases := []struct {
		name string
		p    BizProbe
		want string
	}{
		{"无证书", BizProbe{}, "—"},
		{"余量充足", BizProbe{CertExpire: day, CertDays: 186}, "2026-11-03"},
		{"临期", BizProbe{CertExpire: day, CertDays: 12}, "2026-11-03（12 天）"},
		{"已过期", BizProbe{CertExpire: day, CertDays: -1}, "2026-11-03（已过期）"},
	}
	for _, c := range cases {
		if got := bizCertText(c.p); got != c.want {
			t.Errorf("%s：证书列文案 = %q，期望 %q", c.name, got, c.want)
		}
	}
}

// TestBizProbeCertColumn 有证书数据时增加「证书到期」列，临期单元格按「关注」色着色
func TestBizProbeCertColumn(t *testing.T) {
	cert := time.Now().AddDate(0, 0, 12)
	b := bizCertDemo([]BizProbe{
		{System: "天空地森林防火", Name: "门户网站", Addr: "https://www.a.gov.cn/portal", Avail: 100, Avg: 20, Max: 60, Std: 4,
			CertExpire: cert.Unix(), CertDays: 12},
		{System: "天空地森林防火", Name: "内网接口", Addr: "http://10.0.0.2:8080/api", Avail: 100, Avg: 10, Max: 30, Std: 2},
	})
	out := buildBizDocx(t, b, true)
	text := docText(t, out)
	if !strings.Contains(text, "证书到期") {
		t.Error("有证书数据时应渲染「证书到期」列")
	}
	if !strings.Contains(text, cert.Format("2006-01-02")) {
		t.Errorf("证书到期列缺少到期日 %s，实得：\n%s", cert.Format("2006-01-02"), text)
	}
	if !strings.Contains(text, "（12 天）") {
		t.Error("剩余有效期 ＜30 天时应给出天数提示")
	}
	if !strings.Contains(text, bizWarn) {
		t.Error("证书临期目标的状态应降为关注")
	}
	// 临期单元格本身要着色（statusColor[关注] = C55A11）
	doc := docxPart(t, out, "word/document.xml")
	idx := strings.Index(doc, "（12 天）")
	if idx < 0 {
		t.Fatal("文档中未找到证书临期单元格")
	}
	from := idx - 600
	if from < 0 {
		from = 0
	}
	if !strings.Contains(doc[from:idx], "C55A11") {
		t.Error("证书临期单元格未着色提示")
	}
}

// TestBizProbeCertColumnHidden 所有目标都没有证书时间时，该列不出现
func TestBizProbeCertColumnHidden(t *testing.T) {
	b := bizCertDemo([]BizProbe{
		{System: "A", Name: "接口1", Addr: "http://10.0.0.1:80", Avail: 100, Avg: 12, Max: 40, Std: 3},
		{System: "A", Name: "接口2", Addr: "http://10.0.0.2:80", Avail: 100, Avg: 15, Max: 45, Std: 3},
	})
	text := docText(t, buildBizDocx(t, b, true))
	if strings.Contains(text, "证书到期") {
		t.Error("所有目标均无证书时间时不应渲染「证书到期」列")
	}
	if strings.Contains(text, "剩余有效期") {
		t.Error("无证书数据时判断标准不应提及证书")
	}
}

// TestBizFindingsCert 证书临期 / 过期各自成为一条独立发现，并带续签建议
func TestBizFindingsCert(t *testing.T) {
	cert := time.Now().AddDate(0, 0, 9)
	warn := bizCertDemo([]BizProbe{{System: "A", Name: "门户", Addr: "https://a.gov.cn", Avail: 100, Avg: 20,
		CertExpire: cert.Unix(), CertDays: 9}})
	fs := bizFindings(warn)
	if len(fs) != 1 {
		t.Fatalf("证书临期应产出 1 条发现（响应指标正常），实得 %d 条：%+v", len(fs), fs)
	}
	if fs[0].Level != bizWarn || !strings.Contains(fs[0].Desc, "证书即将到期") {
		t.Errorf("临期发现内容不符：%+v", fs[0])
	}
	if !strings.Contains(fs[0].Basis, "剩余 9 天") || !strings.Contains(fs[0].Advice, "续签") {
		t.Errorf("临期发现缺少剩余天数或续签建议：%+v", fs[0])
	}

	expired := bizCertDemo([]BizProbe{{System: "A", Name: "门户", Addr: "https://a.gov.cn", Avail: 100, Avg: 20,
		CertExpire: cert.Unix(), CertDays: -3}})
	fe := bizFindings(expired)
	if len(fe) != 1 || fe[0].Level != bizAlert || !strings.Contains(fe[0].Desc, "已过期") {
		t.Fatalf("过期证书应为告警级发现，实得 %+v", fe)
	}
}

// TestBizConclusionCert 总体结论要点带上证书临期摘要
func TestBizConclusionCert(t *testing.T) {
	b := bizCertDemo([]BizProbe{
		{System: "A", Name: "门户", Addr: "https://a.gov.cn", Avail: 100, Avg: 20, CertExpire: 1, CertDays: 12},
		{System: "A", Name: "接口", Addr: "http://10.0.0.2:80", Avail: 100, Avg: 10},
	})
	text, level := bizConclusion(b)
	if !strings.Contains(text, "证书 30 天内到期") || !strings.Contains(text, "最紧剩余 12 天") {
		t.Errorf("总体结论缺少证书临期摘要，实得：%s", text)
	}
	if level != bizWarn {
		t.Errorf("证书临期时汇总状态应为关注，实得 %q", level)
	}
}

// TestBizSQLServerKind SQL Server 的配置：发现指标、5 个关键指标、性能计数器表达式与趋势图
func TestBizSQLServerKind(t *testing.T) {
	var k *bizKind
	for i := range bizKinds {
		if bizKinds[i].ID == "sqlserver" {
			k = &bizKinds[i]
		}
	}
	if k == nil {
		t.Fatal("bizKinds 中缺少 sqlserver")
	}
	if k.Discover != "sqlserver_up" {
		t.Errorf("发现指标应为 sqlserver_up，实得 %q", k.Discover)
	}
	if k.Name != "SQL Server" || k.Category != "数据库" {
		t.Errorf("名称 / 分类不符：%q / %q", k.Name, k.Category)
	}

	var keys []string
	labels := map[string]bool{}
	for _, m := range k.Metrics {
		if m.Expr == "" && m.PairExpr[0] == "" {
			t.Errorf("%s 缺表达式", m.Label)
		}
		if m.Fmt == nil || m.Judge == nil {
			t.Errorf("%s 缺格式化或判定函数", m.Label)
		}
		if !strings.Contains(m.Expr, "{{inst}}") && m.PairExpr[0] == "" {
			t.Errorf("%s 表达式未按实例过滤：%s", m.Label, m.Expr)
		}
		if m.Key {
			keys = append(keys, m.Label)
		}
		labels[m.Label] = true
	}
	// 关键指标表一行 5 个指标，多于此会撑破页面
	if len(keys) != 5 {
		t.Errorf("关键指标应为 5 个，实得 %d 个：%v", len(keys), keys)
	}
	for _, want := range []string{"用户连接数", "批请求（次/s）", "数据缓存命中率", "阻塞进程数", "读延迟（ms/次）"} {
		if !labels[want] {
			t.Errorf("缺少指标 %q", want)
		}
	}
	// 备份时长只覆盖 msdb 里的 SQL 备份，存储快照 / 备份一体机场景会误报，已按用户要求整行去掉
	if labels["完整备份距今"] {
		t.Error("完整备份距今已移除，不应再出现在指标表里")
	}
}

// TestBizKindsStructure 所有组件类型的配置自检：发现指标非空、关键指标 ≤5、表达式按实例过滤
func TestBizKindsStructure(t *testing.T) {
	seen := map[string]bool{}
	for _, k := range bizKinds {
		if k.ID == "" || k.Name == "" || k.Category == "" || k.Discover == "" {
			t.Errorf("%q 配置不完整：%+v", k.ID, k)
		}
		if seen[k.ID] {
			t.Errorf("组件 ID 重复：%s", k.ID)
		}
		seen[k.ID] = true
		if len(k.Metrics) == 0 {
			t.Errorf("%s 未配置指标", k.ID)
			continue
		}
		nKey := 0
		for _, m := range k.Metrics {
			if m.Key {
				nKey++
			}
			if m.Expr == "" && m.PairExpr[0] == "" {
				t.Errorf("%s / %s 缺表达式", k.ID, m.Label)
			}
			if m.Thresh == "" {
				t.Errorf("%s / %s 缺参考阈值文案", k.ID, m.Label)
			}
			if m.Fmt == nil || m.Judge == nil {
				t.Errorf("%s / %s 缺格式化或判定函数", k.ID, m.Label)
			}
		}
		if nKey > 5 {
			t.Errorf("%s 关键指标 %d 个，超过 5 个会撑破关键指标表", k.ID, nKey)
		}
	}
}

// TestBizEvalInstantParsesValue 瞬时查询返回的是 value（不是 values）。
// 只解析 values 会让所有 Instant 指标（SQL Server 存活 / ZooKeeper 存活 / Redis 持久化状态等）整行静默丢失。
func TestBizEvalInstantParsesValue(t *testing.T) {
	withQuietLogger(t)
	stub := &n9eStub{statuses: []int{200}, body: `{"status":"success","data":{"result":[{"metric":{},"value":[1758000000,"1"]}]}}`}
	cli, _ := newStubClient(t, stub, 1)
	mean, _, ok := bizEval(cli, "sqlserver_up", bizMetric{Label: "实例存活", Instant: true}, 0, 1758000000, 300)
	if !ok || mean != 1 {
		t.Fatalf("瞬时查询应解析出 1，实得 %v（ok=%v）", mean, ok)
	}

	// 区间查询仍走 values，取均值
	stub2 := &n9eStub{statuses: []int{200}}
	cli2, _ := newStubClient(t, stub2, 1)
	mean2, peak2, ok2 := bizEval(cli2, "x", bizMetric{}, 0, 1758000000, 300)
	if !ok2 || mean2 != 12.5 || peak2 != 12.5 {
		t.Fatalf("区间查询应解析出 12.5，实得 %v / %v（ok=%v）", mean2, peak2, ok2)
	}
}

// TestPromSeriesSamples 样本序列归一化：区间取 values、瞬时取 value、空则 nil
func TestPromSeriesSamples(t *testing.T) {
	if got := (promSeries{Values: [][]interface{}{{float64(1), "2"}}}).samples(); len(got) != 1 {
		t.Errorf("区间查询应原样返回 values，实得 %v", got)
	}
	if got := (promSeries{Value: []interface{}{float64(1), "2"}}).samples(); len(got) != 1 || len(got[0]) != 2 {
		t.Errorf("瞬时查询应把 value 归一成单元素序列，实得 %v", got)
	}
	if got := (promSeries{}).samples(); got != nil {
		t.Errorf("无样本应返回 nil，实得 %v", got)
	}
}

// TestCertNameMatchesHost 证书域名与访问域名的匹配规则
func TestCertNameMatchesHost(t *testing.T) {
	cases := []struct {
		cert, host string
		want       bool
		desc       string
	}{
		{"www.a.com", "www.a.com", true, "完全相同"},
		{"WWW.A.COM.", "www.a.com", true, "忽略大小写与末尾点"},
		{"www.a.com", "a.com", true, "同站不同层级"},
		{"a.com", "www.a.com", true, "反向层级"},
		{"*.a.com", "api.a.com", true, "通配符覆盖一级子域"},
		{"*.a.com", "a.com", false, "通配符不覆盖裸域"},
		{"*.a.com", "x.y.a.com", false, "通配符只覆盖一级"},
		{"www.a.com", "10.0.0.9", false, "IP 访问域名证书"},
		{"10.0.0.9", "10.0.0.9", true, "IP 对 IP"},
		{"www.a.com", "www.b.com", false, "不同站点"},
		{"", "www.a.com", true, "CN 缺失时按一致处理"},
		{"www.a.com", "", true, "主机缺失时按一致处理"},
	}
	for _, c := range cases {
		if got := certNameMatchesHost(c.cert, c.host); got != c.want {
			t.Errorf("%s：certNameMatchesHost(%q,%q) = %v，期望 %v", c.desc, c.cert, c.host, got, c.want)
		}
	}
}

// TestCertHostOf 从探活地址里取主机名
func TestCertHostOf(t *testing.T) {
	cases := map[string]string{
		"https://www.a.com/portal":     "www.a.com",
		"https://www.a.com:8443/x?y=1": "www.a.com",
		"HTTP://user:pwd@www.a.com/x":  "www.a.com",
		"https://10.0.0.9:443":         "10.0.0.9",
		"https://[::1]:8443/x":         "::1",
		"www.a.com":                    "www.a.com",
	}
	for in, want := range cases {
		if got := certHostOf(in); got != want {
			t.Errorf("certHostOf(%q) = %q，期望 %q", in, got, want)
		}
	}
}

// TestBizProbeCertMismatchIgnored 证书域名与访问域名不一致时不参与判定：
// 状态仍为正常、不产生巡检发现、表格里只给到期日并标注「域名不符」。
func TestBizProbeCertMismatchIgnored(t *testing.T) {
	cert := time.Now().AddDate(0, 0, 15)
	b := bizCertDemo([]BizProbe{{
		System: "天空地森林防火", Name: "指挥调度", Addr: "https://10.0.0.9:443/command",
		Avail: 100, Avg: 20, Max: 60, Std: 3,
		CertExpire: cert.Unix(), CertName: "www.a.gov.cn", CertDays: 15,
	}})
	if got := b.Probes[0].Status; got != bizNormal {
		t.Errorf("域名不一致时状态应为正常，实得 %q", got)
	}
	for _, f := range bizFindings(b) {
		if strings.Contains(f.Desc, "证书") {
			t.Errorf("域名不一致不应产生证书类发现：%+v", f)
		}
	}
	text, _ := bizConclusion(b)
	if strings.Contains(text, "证书") {
		t.Errorf("域名不一致时总体结论不应提证书：%s", text)
	}

	out := buildBizDocx(t, b, true)
	txt := docText(t, out)
	if !strings.Contains(txt, "（域名不符）") {
		t.Error("证书临期但域名不一致时，到期列应标注「域名不符」")
	}
	if strings.Contains(txt, "（15 天）") {
		t.Error("域名不一致时不应给出剩余天数提示")
	}
	// 该单元格不应着色（statusColor[关注] = C55A11）
	doc := docxPart(t, out, "word/document.xml")
	idx := strings.Index(doc, "（域名不符）")
	if idx < 0 {
		t.Fatal("文档中未找到「域名不符」单元格")
	}
	from := idx - 600
	if from < 0 {
		from = 0
	}
	if strings.Contains(doc[from:idx], "C55A11") {
		t.Error("域名不一致的证书单元格不应按「关注」着色")
	}
}

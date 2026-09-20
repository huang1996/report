package main

// build.go —— 合并版巡检周报组装：格式与主体结构以「云资源巡检周报」为主，
// Safeline WAF 巡检数据作为独立章节融入（数据不缺失）。

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type BuildInput struct {
	Cfg      *Config
	Rows     []HostRow
	Waf      *WafData // nil 表示本期未接入 WAF 数据
	WafNote  string   // WAF 数据获取异常时的说明
	WS, WE   time.Time
	Source   string // 数据来源描述
}

func reportTitle(cfg *Config, rows []HostRow) string {
	if cfg.Title != "" {
		return cfg.Title
	}
	projects := projectSet(rows)
	if len(projects) == 1 {
		return projects[0] + "巡检周报"
	}
	return "云资源巡检周报"
}

func projectSet(rows []HostRow) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range rows {
		p := strings.TrimSpace(r.Project)
		if p == "" || p == "—" {
			continue
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

func engineerSet(rows []HostRow) string {
	seen := map[string]bool{}
	var out []string
	for _, r := range rows {
		e := strings.TrimSpace(r.Engineer)
		if e == "" || e == "—" {
			continue
		}
		if !seen[e] {
			seen[e] = true
			out = append(out, e)
		}
	}
	sort.Strings(out)
	if len(out) == 0 {
		return "—"
	}
	return strings.Join(out, "、")
}

func chartImages(cfg *Config, rows []HostRow) ([][]byte, error) {
	var out [][]byte
	top := cfg.ChartTop
	pick := func(key func(*HostRow) float64) []HostRow {
		rs := make([]HostRow, len(rows))
		copy(rs, rows)
		sort.SliceStable(rs, func(i, j int) bool { return key(&rs[i]) > key(&rs[j]) })
		if top > 0 && len(rs) > top {
			rs = rs[:top]
		}
		return rs
	}
	lbl := func(rs []HostRow) []string {
		out := make([]string, len(rs))
		for i, r := range rs {
			out[i] = shortIP(r.IP)
		}
		return out
	}

	// 图 1 CPU 当前 vs 峰值
	r1 := pick(func(r *HostRow) float64 { return r.CPUPeak })
	cpuTH := TH["CPU"]
	img1 := DrawGroupedBarChart("图 1　各主机 CPU 当前使用率与峰值使用率对比", lbl(r1),
		[]BarSeries{
			{Name: "当前使用率", Color: "8FBEDA", Values: col(r1, func(r *HostRow) float64 { return r.CPU }), Fmt: "%.2f%%"},
			{Name: "峰值使用率", Color: "2E75B6", Values: col(r1, func(r *HostRow) float64 { return r.CPUPeak }), Fmt: "%.2f%%"},
		}, cpuTH[warnIdx], sprintf("预警线 %.0f%%", cpuTH[warnIdx]), "使用率 (%)", 0)
	b, err := encodePNG(img1)
	if err != nil {
		return nil, err
	}
	out = append(out, b)

	// 图 2 内存 当前 vs 峰值
	r2 := pick(func(r *HostRow) float64 { return r.MemPeak })
	img2 := DrawGroupedBarChart("图 2　各主机内存当前使用率与峰值使用率对比", lbl(r2),
		[]BarSeries{
			{Name: "当前使用率", Color: "8FBEDA", Values: col(r2, func(r *HostRow) float64 { return r.Mem }), Fmt: "%.2f%%"},
			{Name: "峰值使用率", Color: "2E75B6", Values: col(r2, func(r *HostRow) float64 { return r.MemPeak }), Fmt: "%.2f%%"},
		}, cpuTH[warnIdx], sprintf("预警线 %.0f%%", cpuTH[warnIdx]), "使用率 (%)", 0)
	b, err = encodePNG(img2)
	if err != nil {
		return nil, err
	}
	out = append(out, b)

	// 图 3 磁盘使用率与磁盘 IO
	r3 := pick(func(r *HostRow) float64 { return r.Disk })
	img3 := DrawGroupedBarChart("图 3　各主机磁盘使用率与磁盘 IO 对比", lbl(r3),
		[]BarSeries{
			{Name: "磁盘使用率", Color: "7F7F7F", Values: col(r3, func(r *HostRow) float64 { return r.Disk }), Fmt: "%.2f%%"},
			{Name: "磁盘 IO", Color: "ED7D31", Values: col(r3, func(r *HostRow) float64 { return r.DiskIO }), Fmt: "%.2f%%"},
		}, 0, "", "使用率 (%)", 0)
	b, err = encodePNG(img3)
	if err != nil {
		return nil, err
	}
	out = append(out, b)

	// 图 4 峰谷差
	r4 := pick(func(r *HostRow) float64 { return r.CPUGap })
	img4 := DrawGroupedBarChart("图 4　各主机 CPU / 内存峰谷差", lbl(r4),
		[]BarSeries{
			{Name: "CPU 峰谷差", Color: "C00000", Values: col(r4, func(r *HostRow) float64 { return r.CPUGap }), Fmt: "%.1fpp"},
			{Name: "内存峰谷差", Color: "ED7D31", Values: col(r4, func(r *HostRow) float64 { return r.MemGap }), Fmt: "%.1fpp"},
		}, 0, "", "峰谷差 (百分点)", 0)
	b, err = encodePNG(img4)
	if err != nil {
		return nil, err
	}
	out = append(out, b)

	// 图 5 资源总览
	r5 := pick(func(r *HostRow) float64 { return r.Disk })
	img5 := DrawGroupedBarChart("图 5　各主机 CPU / 内存 / 磁盘 使用率总览", lbl(r5),
		[]BarSeries{
			{Name: "CPU 使用率", Color: "2E75B6", Values: col(r5, func(r *HostRow) float64 { return r.CPU }), Fmt: "%.1f%%"},
			{Name: "内存使用率", Color: "ED7D31", Values: col(r5, func(r *HostRow) float64 { return r.Mem }), Fmt: "%.1f%%"},
			{Name: "磁盘使用率", Color: "7F7F7F", Values: col(r5, func(r *HostRow) float64 { return r.Disk }), Fmt: "%.1f%%"},
		}, 80, "警告线 80%", "使用率 (%)", 0)
	b, err = encodePNG(img5)
	if err != nil {
		return nil, err
	}
	out = append(out, b)
	return out, nil
}

func col(rs []HostRow, f func(*HostRow) float64) []float64 {
	out := make([]float64, len(rs))
	for i := range rs {
		out[i] = f(&rs[i])
	}
	return out
}

// BuildReport 组装并保存 docx，返回输出路径
func BuildReport(in *BuildInput) (string, error) {
	cfg := in.Cfg
	rows := in.Rows
	sortHosts(rows)
	title := reportTitle(cfg, rows)
	period := in.WS.Format("2006-01-02") + " ~ " + in.WE.Format("2006-01-02")
	if in.WS.Format("2006-01-02") == in.WE.Format("2006-01-02") {
		period = in.WS.Format("2006-01-02")
	}
	_, week := in.WE.ISOWeek()
	projects := projectSet(rows)
	engineers := engineerSet(rows)

	fnd := buildFindings(rows, analyze(rows))

	var charts [][]byte
	var chartErr error
	if len(rows) > 0 {
		charts, chartErr = chartImages(cfg, rows)
		if chartErr != nil {
			log.Warnf("图表生成失败：%v（报告将继续，但缺少资源图表）", chartErr)
		}
	}

	d := NewDocx()
	d.HeaderText = cfg.HeaderText
	d.Title(title)
	// 生成日期与统计周期结束日期保持一致（报告口径统一按巡检周期末日）
	d.MetaLine(fmt.Sprintf("巡检周期：%s　|　第 %d 周　|　生成日期：%s", period, week, in.WE.Format("2006-01-02")))

	// 一、报告信息
	d.Heading(1, "1、报告信息")
	objDesc := "—"
	if len(rows) > 0 {
		objDesc = sprintf("%d 台主机 / %d 个业务系统", len(rows), maxInt(1, len(projects)))
	} else if in.Waf != nil {
		objDesc = sprintf("%d 个 WAF 防护应用", len(in.Waf.Apps))
	}
	srcDesc := in.Source
	if in.Waf != nil {
		if srcDesc != "" {
			srcDesc += " + Safeline WAF 数据库"
		} else {
			srcDesc = "Safeline WAF 数据库"
		}
	}
	d.Table([]string{"项目", "内容", "项目", "内容"},
		[][]string{
			{"报告名称", title, "巡检周期", period},
			{"巡检对象", objDesc, "运维工程师", engineers},
		},
		[]float64{2.7, 5.8, 2.7, 4.4}, nil, 9.5, false)

	// 二、本周总体结论
	d.Heading(1, "2、本周总体结论")
	overall := "正常"
	overallDesc := "各项指标均在安全区间"
	if len(rows) > 0 {
		overall = analyze(rows).Overall
		if overall == "告警" {
			overallDesc = "存在超阈值项，需立即处置"
		} else if overall == "关注" {
			overallDesc = "存在潜在风险项，需重点跟进"
		}
	}
	d.BodyRuns(
		Run{Text: "综合评级：", Size: 10.5, Bold: true, Color: navy},
		Run{Text: " " + overall + " ", Size: 11, Bold: true, Color: statusColor[overall]},
		Run{Text: "（" + overallDesc + "）", Size: 10.5})
	for _, c := range fnd.Concl {
		d.Bullet(c)
	}
	if in.Waf != nil {
		t := in.Waf.Total
		d.Bullet(fmt.Sprintf("Web 应用防火墙运行平稳：总访问次数 %s，总拦截次数 %s，黑名单拦截次数 %s，未拦截攻击次数 %s，拦截率 %s%%。",
			comma(t.VisitTotal), comma(t.BlockedTotal), comma(t.BlacklistBlocked), comma(t.Unblocked), t.BlockRate))
	} else if in.WafNote != "" {
		d.Bullet("WAF 安全巡检：" + in.WafNote)
	}

	d.Heading(2, "2.1 关键指标概览")
	if len(rows) > 0 {
		m := analyze(rows).Agg
		kpi := [][]string{
			{"CPU 使用率", sprintf("%.2f%%", m["CPU"].Cur), sprintf("%.2f%%", m["CPU"].Peak), sprintf("%.2f%%", m["CPU"].Mx), thLabel("CPU", m["CPU"].Lv), m["CPU"].Lv},
			{"内存使用率", sprintf("%.2f%%", m["内存"].Cur), sprintf("%.2f%%", m["内存"].Peak), sprintf("%.2f%%", m["内存"].Mx), thLabel("内存", m["内存"].Lv), m["内存"].Lv},
			{"磁盘使用率", sprintf("%.2f%%", m["磁盘"].Cur), sprintf("%.2f%%", m["磁盘"].Peak), sprintf("%.2f%%", m["磁盘"].Mx), thLabel("磁盘", m["磁盘"].Lv), m["磁盘"].Lv},
			{"网络带宽（单机）", sprintf("%.1f Mb/s", m["网络"].Cur), sprintf("%.1f Mb/s", m["网络"].Peak), sprintf("%.0f Mb/s", m["网络"].Mx), thAbsLabel(NET_TH, m["网络"].Lv, " Mb/s"), m["网络"].Lv},
			{"并发连接数（合计）", comma(int64(m["连接数"].Cur)), "—", "—", thAbsLabel(CONN_TH, m["连接数"].Lv, " 条"), m["连接数"].Lv},
		}
		d.Table([]string{"关键指标", "当前均值", "峰值均值", "最高值", "告警阈值", "状态"}, kpi,
			[]float64{3.6, 2.4, 2.4, 2.4, 2.6, 1.8}, map[int]bool{5: true}, 9.5, false)
		d.Caption("表 1　关键资源指标汇总（CPU/内存/磁盘：≥75% 提示、≥80% 警告、≥90% 严重、≥95% 紧急；内存＜20% 判定可优化；" +
			"带宽按 1 Gb 出口、连接数为合计值）。磁盘使用率取各主机分区中最紧张者；网络带宽为出入向实测流量合计；并发连接数仅含已采集该指标的 Linux 主机。")
	} else {
		d.Body("本期未采集到主机资源数据。")
	}

	// 三、资源水位分析
	if len(rows) > 0 && len(charts) >= 5 {
		d.Heading(1, "3、资源水位分析")
		topNote := ""
		if cfg.ChartTop > 0 && len(rows) > cfg.ChartTop {
			topNote = sprintf("（仅展示指标最高的 %d 台，共 %d 台）", cfg.ChartTop, len(rows))
			d.BodyRuns(Run{Text: "为便于阅读，下列图表按各图对应指标从高到低排序，最多展示 " + itoa(cfg.ChartTop) +
				" 台主机；完整数据见第 4 章明细表。", Size: 9, Color: grey})
		}
		notes := []string{
			"横轴为 IP 末两段" + topNote + "；峰值高于当前值越多，说明负载波动越剧烈",
			"内存峰值反映高峰时段压力，是 OOM 风险的直接指标" + topNote,
			"磁盘使用率反映容量占用，磁盘 IO 反映读写压力，二者需分开评估" + topNote,
			"峰谷差超过 40 个百分点时，均值监控会严重低估真实压力" + topNote,
			"以主机 IP 末两段为横轴对比三项资源，用于识别高水位与闲置主机" + topNote,
		}
		subs := []string{"3.1 CPU 使用率：当前 vs 峰值", "3.2 内存使用率：当前 vs 峰值",
			"3.3 磁盘使用率与磁盘 IO", "3.4 负载波动分析（峰谷差）", "3.5 资源容量使用概览"}
		for i, c := range charts[:5] {
			d.Heading(2, subs[i])
			if err := d.Image(c, 16.2); err != nil {
				log.Warnf("插入图 %d 失败：%v", i+1, err)
			}
			d.Caption(fmt.Sprintf("图 %d　%s", i+1, notes[i]))
		}
	}

	// 四、巡检明细
	if len(rows) > 0 {
		d.Heading(1, "4、巡检明细")
		var det [][]string
		for i := range rows {
			r := &rows[i]
			det = append(det, []string{r.IP,
				sprintf("%.2f%%", r.CPU), sprintf("%.2f%%", r.CPUPeak),
				sprintf("%.2f%%", r.Mem), sprintf("%.2f%%", r.MemPeak),
				sprintf("%.2f%%", r.Disk), sprintf("%.2f%%", diskPeakOf(r)), r.Status})
		}
		d.Table([]string{"IP 地址", "CPU当前", "CPU峰值", "内存当前", "内存峰值", "磁盘当前", "磁盘峰值", "状态"},
			det, []float64{3.2, 2.0, 2.0, 2.0, 2.0, 2.0, 2.0, 1.4}, map[int]bool{7: true}, 9, false)
		d.Caption("表 2　资源巡检明细（CPU / 内存 / 磁盘，均为周期内均值与峰值；资源规格与归属见下表）")

		multiProj := len(projects) > 1
		headers := []string{"IP 地址", "主机角色"}
		widths := []float64{2.7, 3.3}
		if multiProj {
			headers = append(headers, "业务系统")
			widths = append(widths, 3.1)
		}
		headers = append(headers, "CPU规格", "内存容量", "磁盘容量", "操作系统", "运维工程师")
		widths = append(widths, 1.3, 1.5, 1.5, 3.0, 1.5)
		var own [][]string
		for i := range rows {
			r := &rows[i]
			row := []string{r.IP, orDash(r.Role)}
			if multiProj {
				row = append(row, orDash(r.Project))
			}
			row = append(row, sprintf("%.0f 核", r.Cores),
				sprintf("%.0f GB", r.MemTotalGB), sprintf("%.0f GB", r.DiskCapGB),
				orDash(r.OS), orDash(r.Engineer))
			own = append(own, row)
		}
		// 操作系统列（文本较长，默认会被判为左对齐，此处强制居中）
		// hideEmptyCols=false：即使个别主机取不到操作系统（显示「—」），列也不隐藏
		osIdx := 5
		if multiProj {
			osIdx = 6
		}
		d.TableCenterCols(map[int]bool{osIdx: true}, headers, own, widths, nil, 9, false)
		d.Caption("表 3　主机归属与资源规格")

		var perf [][]string
		for i := range rows {
			r := &rows[i]
			conn := "—"
			if !r.NoConn {
				conn = comma(int64(r.Conn))
			}
			perf = append(perf, []string{r.IP, sprintf("%.2f%%", r.DiskIO), conn,
				sprintf("%.0f Mb/s", r.Net), sprintf("%.1f pp", r.CPUGap),
				sprintf("%.1f pp", r.MemGap), orDash(r.Engineer)})
		}
		d.Table([]string{"IP 地址", "磁盘 IO", "连接数", "网络使用率", "CPU峰谷差", "内存峰谷差", "运维工程师"},
			perf, []float64{3.4, 2.2, 2.1, 2.7, 2.4, 2.4, 2.2}, nil, 9, true)
		d.Caption("表 4　性能与网络指标明细（网络使用率为出入向流量合计；连接数取自 netstat_tcp_inuse，Windows 主机未采集该指标，以「—」表示）")
	}

	// 五、业务巡检（内容留空，由人工填写；REPORT_INCLUDE_BIZ 控制是否添加，默认不添加）
	// 后续章节编号动态计算，跳过业务巡检时自动前移
	ch := 5
	if cfg.IncludeBiz {
		d.Heading(1, sprintf("%d、业务巡检", ch))
		d.Blank(12)
		ch++
	}

	// Web 应用防火墙安全巡检（融合自 safeline-report）
	if in.Waf != nil {
		buildWafChapter(d, in.Waf, ch)
		ch++
	} else if in.WafNote != "" {
		d.Heading(1, sprintf("%d、Web 应用防火墙安全巡检", ch))
		d.Body(in.WafNote)
		ch++
	}

	// 风险研判与优化建议
	d.Heading(1, sprintf("%d、风险研判与优化建议", ch))
	var risks [][]string
	for i, r := range fnd.Risks {
		risks = append(risks, []string{sprintf("R%d", i+1), r.Desc, r.Level, r.Suggest})
	}
	// WAF 风险项
	if in.Waf != nil && in.Waf.Total.Unblocked > 0 {
		risks = append(risks, []string{sprintf("R%d", len(risks)+1),
			fmt.Sprintf("WAF 本期存在 %d 条未拦截攻击记录（拦截率 %s%%），存在攻击穿透风险，需核查规则策略与回源链路",
				in.Waf.Total.Unblocked, in.Waf.Total.BlockRate),
			"警告", suggest("警告")})
	}
	if len(risks) == 0 {
		risks = append(risks, []string{"R1", "本期未发现需要处置的风险项", "低", suggest("低")})
	}
	d.Table([]string{"编号", "风险描述", "等级", "建议措施"}, risks,
		[]float64{1.3, 6.6, 1.4, 7.1}, map[int]bool{2: true}, 9, false)
	d.Caption("表 5　风险台账（等级：紧急 / 严重 / 警告 / 提示 / 低；各等级对应固定处置措施，见建议措施列）")

	// 下周巡检重点
	d.Heading(1, sprintf("%d、下周巡检重点", ch+1))
	var plans [][]string
	for _, p := range fnd.Plans {
		plans = append(plans, []string{p.No, p.Item})
	}
	if in.Waf != nil && in.Waf.Total.Unblocked > 0 {
		plans = append(plans, []string{itoa(len(plans) + 1), "复核 WAF 拦截策略与黑名单，分析并处置未拦截攻击记录"})
	}
	if len(plans) == 0 {
		plans = append(plans, []string{"1", "常规巡检"})
	}
	d.Table([]string{"序号", "巡检 / 跟进事项"}, plans, []float64{0.9, 15.7}, nil, 9, false)
	d.Caption("表 6　下周重点工作计划")

	if err := os.MkdirAll(cfg.ReportDir, 0o755); err != nil {
		return "", err
	}
	outPath := cfg.Out
	if outPath == "" {
		outPath = JoinPath(cfg.ReportDir, sprintf("%s_%s_%s.docx",
			title, in.WS.Format("20060102"), in.WE.Format("20060102")))
	}
	if parent := filepath.Dir(outPath); parent != "" && parent != "." {
		if err := os.MkdirAll(parent, 0o755); err != nil {
			return "", err
		}
	}
	// 文件被占用时加时间戳改存
	if fileExists(outPath) {
		if err := tryWrite(outPath, d); err != nil {
			alt := strings.TrimSuffix(outPath, ".docx") + "_" + time.Now().Format("150405") + ".docx"
			log.Warnf("%s 正被占用（可能已在其他程序中打开），改存为：%s", outPath, alt)
			outPath = alt
		}
	}
	if err := tryWrite(outPath, d); err != nil {
		return "", err
	}
	return outPath, nil
}

func tryWrite(path string, d *Docx) error {
	_ = os.Remove(path)
	return d.Save(path)
}

// buildWafChapter 六、Web 应用防火墙安全巡检
// buildWafChapter Web 应用防火墙安全巡检（章节号动态，含 n.1~n.4 小节）
func buildWafChapter(d *Docx, w *WafData, ch int) {
	h1 := sprintf("%d、Web 应用防火墙安全巡检", ch)
	h2 := func(s string) string { return sprintf("%d.%s", ch, s) }
	d.Heading(1, h1)

	// n.1 防护应用概览
	d.Heading(2, h2("1 防护应用概览"))
	t := w.Total
	p := "100.00"
	if t.Unblocked > 0 {
		p = t.BlockRate
	}
	d.BodyRuns(
		Run{Text: "报告周期内 WAF 总体运行平稳，总访问次数为 ", Size: 10.5},
		Run{Text: comma(t.VisitTotal), Size: 10.5, Bold: true, Color: navy},
		Run{Text: "，总拦截次数为 ", Size: 10.5},
		Run{Text: comma(t.BlockedTotal), Size: 10.5, Bold: true, Color: navy},
		Run{Text: "，黑名单拦截次数为 ", Size: 10.5},
		Run{Text: comma(t.BlacklistBlocked), Size: 10.5, Bold: true, Color: navy},
		Run{Text: "，未拦截攻击次数为 ", Size: 10.5},
		Run{Text: comma(t.Unblocked), Size: 10.5, Bold: true, Color: navy},
		Run{Text: "，拦截率为 ", Size: 10.5},
		Run{Text: p + "%", Size: 10.5, Bold: true, Color: navy},
		Run{Text: "。", Size: 10.5},
	)
	if len(w.Apps) == 0 {
		d.Body("暂无防护应用。")
	} else {
		var rows [][]string
		for _, a := range w.Apps {
			rows = append(rows, []string{itoa(int(a.ID)), orDash(a.Name), orDash(a.Domains), orDash(a.Ports),
				comma(a.Requests), comma(a.Blocked)})
		}
		d.Table([]string{"应用序号", "应用名称", "域名", "开放端口", "请求次数", "拦截次数"},
			rows, []float64{1.6, 3.4, 4.6, 2.2, 2.2, 2.2}, nil, 9, false)
		d.Caption("表 7　WAF 防护应用清单及周期内访问 / 拦截统计")
	}

	// n.2 访问数据统计
	d.Heading(2, h2("2 访问数据统计"))
	d.Heading(3, h2("2.1 按地理区域统计访问数据"))
	if len(w.Geos) == 0 {
		d.Body("本周暂无访问数据。")
	} else {
		top := w.Geos[0]
		d.Body(fmt.Sprintf("本周访问数据主要来自 %s %s（%s），访问次数为 %s，具体数据可参看下表。",
			orDash(top.Province), orDash(top.City), orDash(top.Country), comma(top.Count)))
		geos := w.Geos
		if len(geos) > 30 { // 仅展示访问次数前 30 的地区
			geos = geos[:30]
		}
		var rows [][]string
		for _, g := range geos {
			rows = append(rows, []string{orDash(g.Country), orDash(g.Province), orDash(g.City), comma(g.Count)})
		}
		d.Table([]string{"国家代号", "省份", "城市", "访问次数"}, rows,
			[]float64{3.0, 4.0, 4.0, 4.0}, nil, 9, true)
		cap := "表 8　按地理区域统计的访问数据"
		if len(w.Geos) > 30 {
			cap += fmt.Sprintf("（共 %d 个地区，此处展示访问次数前 30）", len(w.Geos))
		}
		d.Caption(cap)
	}
	d.Heading(3, h2("2.2 按访问 IP 统计访问数据 TOP10"))
	if len(w.AccessIPs) == 0 {
		d.Body("本周暂无访问数据。")
	} else {
		d.Body(fmt.Sprintf("本周主要访问 IP 为 %s，访问次数为 %s，具体数据可参看下表。", w.AccessIPs[0].IP, comma(w.AccessIPs[0].Count)))
		var rows [][]string
		for _, s := range w.AccessIPs {
			rows = append(rows, []string{s.IP, s.AttackType, comma(s.Count)})
		}
		d.TableCentered([]string{"访问 IP", "访问类型", "访问次数"}, rows, []float64{6.0, 5.0, 5.0}, nil, 9, false)
		d.Caption("表 9　按访问 IP 统计的访问数据 TOP10")
	}

	// n.3 攻击数据统计
	d.Heading(2, h2("3 攻击数据统计"))
	d.Heading(3, h2("3.1 按攻击方式统计数据"))
	if len(w.AttackTys) == 0 {
		d.Body("本周暂无攻击数据，您的 WAF 很安全。")
	} else {
		d.Body(fmt.Sprintf("本周的主要攻击类型为「%s」，该类型总计攻击 %s 次，具体数据如下图表所示。",
			w.AttackTys[0].Type, comma(w.AttackTys[0].Count)))
		items := make([]PieItem, len(w.AttackTys))
		for i, at := range w.AttackTys {
			items[i] = PieItem{Label: at.Type, Value: float64(at.Count)}
		}
		pie := DrawPieChart("攻击类型统计图", items)
		if b, err := encodePNG(pie); err == nil {
			if err := d.Image(b, 14.5); err != nil {
				log.Warnf("插入攻击类型统计图失败：%v", err)
			}
		} else {
			log.Warnf("攻击类型统计图渲染失败：%v", err)
		}
		var rows [][]string
		for _, at := range w.AttackTys {
			rows = append(rows, []string{at.Type, comma(at.Count)})
		}
		d.Table([]string{"攻击类型", "攻击次数"}, rows, []float64{8.0, 8.0}, nil, 9, false)
		d.Caption("图 6 / 表 10　攻击类型统计")
	}
	d.Heading(3, h2("3.2 按攻击 IP 统计攻击数据 TOP10"))
	if len(w.AttackIPs) == 0 {
		d.Body("本周暂无攻击数据，您的 WAF 很安全。")
	} else {
		d.Body(fmt.Sprintf("本周的攻击主要来自 %s，攻击类型为「%s」，总计攻击 %s 次，具体数据参看下表。",
			w.AttackIPs[0].IP, w.AttackIPs[0].AttackType, comma(w.AttackIPs[0].Count)))
		var rows [][]string
		for _, s := range w.AttackIPs {
			rows = append(rows, []string{s.IP, s.AttackType, comma(s.Count)})
		}
		d.TableCentered([]string{"攻击 IP", "攻击类型", "攻击次数"}, rows, []float64{6.0, 5.0, 5.0}, nil, 9, false)
		d.Caption("表 11　按攻击 IP 统计的攻击数据 TOP10")
	}

	// n.4 未拦截攻击明细
	d.Heading(2, h2("4 未拦截攻击明细"))
	if len(w.NotBlocked) == 0 {
		d.Body("本周暂无未拦截攻击，所有攻击都被拒之门外。")
	} else {
		d.Body(fmt.Sprintf("本周有 %d 条攻击未被拦截，我们将对其进行分析和拦截处理，具体数据参看下表。", len(w.NotBlocked)))
		var rows [][]string
		for _, r := range w.NotBlocked {
			rows = append(rows, []string{orDash(r.App), orDash(r.SrcIP), orDash(r.Host), orDash(r.Path),
				orDash(r.Port), orDash(r.Country), orDash(r.Province), orDash(r.City), orDash(r.AttackType), orDash(r.Time)})
		}
		d.Table([]string{"被攻击应用", "源 IP", "目标主机", "请求路径", "目标端口",
			"国家代码", "省份", "城市", "攻击类型", "攻击时间"},
			rows, []float64{2.4, 2.0, 2.0, 2.6, 1.4, 1.3, 1.3, 1.3, 1.7, 2.4}, nil, 8.5, true)
		d.Caption("表 12　未拦截攻击明细")
	}
}

// ---- 小工具 ----
func diskPeakOf(r *HostRow) float64 {
	if r.DiskPeak > 0 {
		return r.DiskPeak
	}
	return r.Disk
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func itoa(n int) string { return sprintf("%d", n) }

func comma(n int64) string {
	s := itoa(int(n))
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	if len(s) > 3 {
		var parts []string
		for len(s) > 3 {
			parts = append([]string{s[len(s)-3:]}, parts...)
			s = s[:len(s)-3]
		}
		parts = append([]string{s}, parts...)
		s = strings.Join(parts, ",")
	}
	if neg {
		s = "-" + s
	}
	return s
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func JoinPath(dir, name string) string {
	if strings.HasSuffix(dir, "/") || strings.HasSuffix(dir, "\\") {
		return dir + name
	}
	return dir + string(os.PathSeparator) + name
}

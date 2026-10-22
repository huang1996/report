package main

// biz_build.go —— 业务巡检章节渲染（格式与资源巡检 / WAF 章节保持一致：三线表 + 表号 + 题注）

import (
	"math"
	"strings"
)

// bizSub 小节内的加粗小标题（关键指标 / 详细分析 / 分库明细等）
func bizSub(d *Docx, text string) {
	d.BodyRuns(Run{Text: text, Size: 10.5, Bold: true, Color: navy})
}

// bizNote 表下判断标准（小字灰色，与题注区分）
func bizNote(d *Docx, text string) {
	d.BodyRuns(Run{Text: text, Size: 9, Color: grey})
}

// bizStdText 由关键指标的参考阈值拼出判断标准
func bizStdText(rows []BizRow) string {
	var parts []string
	trendOnly := 0
	for _, r := range rows {
		switch {
		case r.Thresh == "" || r.Thresh == "—" || r.Thresh == "趋势观察":
			parts = append(parts, r.Label+" 仅作趋势观察")
			trendOnly++
		case r.Thresh == "参考最大连接数":
			parts = append(parts, r.Label+" 按最大连接数的实际余量判断")
		case strings.HasPrefix(r.Thresh, "占系统内存"):
			parts = append(parts, r.Label+" "+r.Thresh)
		default:
			parts = append(parts, r.Label+" "+r.Thresh)
		}
	}
	if trendOnly == len(rows) {
		return "判断标准：本表指标均为观测值，无固定阈值，按趋势与业务基线判断；状态取各项中最差的一项。"
	}
	return "判断标准：" + strings.Join(parts, "；") + "；状态取上述各项中最差的一项。"
}

// bizKeyWidths 关键指标表列宽（随关键指标数量等比压缩到页内）
func bizKeyWidths(n int) []float64 {
	w := []float64{4.4}
	for i := 0; i < n; i++ {
		w = append(w, 2.2)
	}
	w = append(w, 1.2)
	tot := 0.0
	for _, x := range w {
		tot += x
	}
	if tot > pageWidthCm {
		for i := range w {
			w[i] = w[i] * pageWidthCm / tot
		}
	}
	return w
}

// bizCompSummary 一句话结论：最差实例与触发项
func bizCompSummary(comps []BizComponent) string {
	if len(comps) == 0 {
		return ""
	}
	worst := comps[0]
	for _, c := range comps {
		if bizRank(c.Status) < bizRank(worst.Status) {
			worst = c
		}
	}
	var hits []string
	for _, r := range worst.Rows {
		if r.Status != bizNormal {
			hits = append(hits, sprintf("%s %s", r.Label, r.Value))
			if len(hits) == 2 {
				break
			}
		}
	}
	if len(hits) == 0 {
		return sprintf("共 %d 个实例，各项指标均在参考阈值内，运行正常。", len(comps))
	}
	return sprintf("共 %d 个实例，其中 %d 个存在越线项；最差实例 %s 状态为%s（%s）。",
		len(comps), bizAbnormal(comps), worst.Inst.Raw, worst.Status, strings.Join(hits, "、"))
}

func bizAbnormal(comps []BizComponent) int {
	n := 0
	for _, c := range comps {
		if c.Status != bizNormal {
			n++
		}
	}
	return n
}

// buildBizChapter 渲染业务巡检章节，返回插入的图表数量（供后续章节的图号衔接）
func buildBizChapter(d *Docx, b *BizData, ch int, tn *tableNo, figNo int) int {
	figs := 0
	d.Heading(1, sprintf("%d、业务巡检", ch))
	h2 := func(no int, title string) { d.Heading(2, sprintf("%d.%d %s", ch, no, title)) }

	systems := b.Systems()
	sysText := "—"
	if len(systems) > 0 {
		sysText = strings.Join(systems, "、")
	}
	d.BodyRuns(
		Run{Text: "报告周期内，对 ", Size: 10.5},
		Run{Text: sysText, Size: 10.5, Bold: true, Color: navy},
		Run{Text: sprintf(" 业务系统下的 %d 个应用探活目标、%d 个中间件与数据库实例开展巡检，",
			len(b.Probes), len(b.Comps)), Size: 10.5},
		Run{Text: "巡检项覆盖可用性、资源容量、吞吐性能与可靠性四个维度。", Size: 10.5},
	)

	sec := 0
	nextSec := func() int { sec++; return sec }

	// ---------------- 业务组件概览 ----------------
	h2(nextSec(), "业务组件概览")
	var ov [][]string
	if len(b.Probes) > 0 {
		normal := 0
		var worstP BizProbe
		worstP = b.Probes[0]
		avail := 0.0
		for _, p := range b.Probes {
			if p.Status == bizNormal {
				normal++
			}
			if bizRank(p.Status) < bizRank(worstP.Status) || (p.Status == worstP.Status && p.Avg > worstP.Avg) {
				worstP = p
			}
			avail += p.Avail
		}
		avail /= float64(len(b.Probes))
		ov = append(ov, []string{
			"应用探活（HTTP）",
			sprintf("%d 个目标", len(b.Probes)),
			sprintf("%d/%d", normal, len(b.Probes)),
			sprintf("整体可用率 %.2f%%，最慢 %s 平均 %.1f ms", avail, worstP.Name, worstP.Avg),
			bizWorst(statusesOfProbes(b.Probes)...),
		})
	}
	for _, k := range bizKinds {
		comps := b.CompsByKind(k.ID)
		if len(comps) == 0 {
			continue
		}
		normal := len(comps) - bizAbnormal(comps)
		worst := comps[0]
		for _, c := range comps {
			if bizRank(c.Status) < bizRank(worst.Status) {
				worst = c
			}
		}
		var core []string
		for _, r := range worst.KeyRows() {
			core = append(core, sprintf("%s %s", r.Label, r.Value))
			if len(core) == 2 {
				break
			}
		}
		ov = append(ov, []string{
			k.Name,
			sprintf("%d 个实例", len(comps)),
			sprintf("%d/%d", normal, len(comps)),
			strings.Join(core, "、"),
			bizWorst(statusesOfComps(comps)...),
		})
	}
	d.TableCentered([]string{"组件类型", "实例数", "正常实例", "核心指标（最差实例）", "状态"}, ov,
		[]float64{3.2, 2.2, 2.0, 7.4, 1.4}, map[int]bool{4: true}, 9, false)
	d.Caption(sprintf("表 %d　业务组件概览", tn.next()))
	bizNote(d, "判断标准：正常实例 = 状态为「正常」的实例数 / 该类型实例总数；状态取该类型下最差实例的状态；"+
		"核心指标列取最差实例的对应数值，用于横向定位问题类型。")

	// ---------------- 应用探活 ----------------
	if len(b.Probes) > 0 {
		h2(nextSec(), "应用探活")
		p0 := b.Probes[0]
		slowest := b.Probes[0]
		minAvg, maxAvg := p0.Avg, p0.Avg
		availSum := 0.0
		hasCert := false
		certWarn := 0
		for _, p := range b.Probes {
			if p.Avg < minAvg {
				minAvg = p.Avg
			}
			if p.Avg > maxAvg {
				maxAvg = p.Avg
				slowest = p
			}
			availSum += p.Avail
			if p.CertExpire > 0 {
				hasCert = true
			}
			if bizProbeCertStatus(p) != bizNormal {
				certWarn++
			}
		}
		intro := sprintf("报告周期内 %d 个探活目标整体可用率 %.2f%%，平均响应时间 %.1f ~ %.1f ms，"+
			"响应标准差均处于合理区间；最慢目标为 %s。",
			len(b.Probes), availSum/float64(len(b.Probes)), minAvg, maxAvg, slowest.Name)
		if hasCert {
			if certWarn > 0 {
				intro += sprintf("其中 %d 个目标配置了 HTTPS 证书，%d 个剩余有效期不足 %d 天，需尽快安排续签。",
					certCount(b.Probes), certWarn, bizCertWarnDays)
			} else {
				intro += sprintf("其中 %d 个目标配置了 HTTPS 证书，均未临近到期。", certCount(b.Probes))
			}
		}
		d.Body(intro)

		headers := []string{"业务 / 用途", "服务地址", "可用率", "平均响应（ms）", "最大响应（ms）", "响应标准差（ms）"}
		widths := []float64{4.4, 2.9, 1.8, 2.1, 2.1, 2.1}
		if hasCert {
			// 多一列「证书到期」，各列等比让出宽度，整表仍为页面可用宽度
			headers = append(headers, "证书到期")
			widths = []float64{3.5, 2.3, 1.5, 1.9, 1.9, 2.0, 2.3}
		}
		headers = append(headers, "状态")
		widths = append(widths, 1.2)

		certCol := -1
		if hasCert {
			certCol = len(headers) - 2
		}
		var rows [][]string
		cellColors := map[int]map[int]string{}
		for ri, p := range b.Probes {
			row := []string{
				sprintf("%s-%s", p.System, p.Name),
				bizAddr(p.Addr),
				sprintf("%.2f%%", p.Avail),
				sprintf("%.1f", p.Avg),
				sprintf("%.0f", p.Max),
				sprintf("%.1f", p.Std),
			}
			if hasCert {
				row = append(row, bizCertText(p))
				// 证书临期 / 过期：单元格单独着色提示（状态列同时降档）
				if cs := bizProbeCertStatus(p); cs != bizNormal {
					cellColors[ri] = map[int]string{certCol: statusColor[cs]}
				}
			}
			row = append(row, p.Status)
			rows = append(rows, row)
		}
		if len(cellColors) > 0 {
			d.SetCellColors(cellColors)
		}
		d.TableCentered(headers, rows, widths, map[int]bool{len(headers) - 1: true}, 9, false)
		d.Caption(sprintf("表 %d　应用探活关键指标", tn.next()))

		note := "判断标准：可用率 ≥ 99.9%、平均响应 ＜ 500 ms、最大响应 ＜ 2000 ms、响应标准差 ＜ 200 ms 为正常；"
		if hasCert {
			note += sprintf("证书剩余有效期 ＜ %d 天标为关注、已过期标为告警（http 目标无证书信息，该列显示「—」）；", bizCertWarnDays)
		}
		d.BodyRuns(Run{Text: note + "任一指标越线即标为关注，状态取各项中最差的一项。", Size: 9, Color: grey})

		if figNo > 0 {
			var series []LineSeries
			for i, p := range b.Probes {
				if len(p.Points) < 2 {
					continue
				}
				xs := make([]float64, len(p.Points))
				ys := make([]float64, len(p.Points))
				for j, pt := range p.Points {
					xs[j], ys[j] = float64(pt.TS), pt.V
				}
				series = append(series, LineSeries{Name: cutRunes(p.Name, 12), Color: bizChartColors[i%len(bizChartColors)], Xs: xs, Ys: ys})
			}
			if len(series) > 0 {
				img := DrawLineChart(sprintf("图 %d　各探活目标响应时间趋势", figNo), series,
					LineChartOpts{WarnLine: 500, WarnText: "关注线 500 ms", YLabel: "响应时间（ms）"})
				if blob, err := encodePNG(img); err == nil {
					if err := d.Image(blob, 15.5); err != nil {
						log.Warnf("插入业务探活图表失败：%v", err)
					} else {
						figs++
						d.Caption(sprintf("图 %d　各探活目标响应时间趋势（按采样点连线，数值单位 ms）", figNo))
					}
				} else {
					log.Warnf("业务探活图表渲染失败：%v", err)
				}
			}
		}
	}

	// ---------------- 数据库 / 缓存 / 中间件（每类组件一个二级章节）----------------
	for _, k := range bizKinds {
		ks := b.CompsByKind(k.ID)
		if len(ks) == 0 {
			continue
		}
		sec++
		h2(sec, k.Name+bizCatSuffix(k.Category))
		d.Body(bizCompSummary(ks))

		// 关键指标：一行一个实例
		bizSub(d, "关键指标")
		keyLabels := ks[0].KeyRows()
		headers := []string{"实例"}
		for _, r := range keyLabels {
			headers = append(headers, r.Label)
		}
		headers = append(headers, "状态")
		var krows [][]string
		for _, c := range ks {
			row := []string{c.Inst.Raw}
			for _, r := range c.KeyRows() {
				row = append(row, r.Value)
			}
			row = append(row, c.Status)
			krows = append(krows, row)
		}
		d.TableCentered(headers, krows, bizKeyWidths(len(keyLabels)),
			map[int]bool{len(headers) - 1: true}, 9, false)
		d.Caption(sprintf("表 %d　%s 关键指标", tn.next(), k.Name))
		bizNote(d, bizStdText(keyLabels))

		// 详细分析：每个实例一个三级标题
		for i, c := range ks {
			d.Heading(3, sprintf("%d.%d.%d 详细分析：%s", ch, sec, i+1, c.Inst.Raw))
			d.Body(bizDetailSummary(c))
			var rows [][]string
			for _, r := range c.Rows {
				rows = append(rows, []string{r.Label, r.Value, r.Peak, r.Thresh, r.Status})
			}
			d.TableCentered([]string{"指标", "周期均值", "峰值", "参考阈值", "状态"}, rows,
				[]float64{4.4, 3.6, 3.0, 3.6, 1.8}, map[int]bool{4: true}, 9, false)
			d.Caption(sprintf("表 %d　%s 运行指标", tn.next(), c.Inst.Raw))

			// 趋势图（MySQL 连接数与使用率 / Redis 吞吐与内存 / PostgreSQL 分库事务提交速率）
			for _, bc := range c.Charts {
				var sers []LineSeries
				for si, sr := range bc.Series {
					if len(sr.Points) < 2 {
						continue
					}
					xs := make([]float64, len(sr.Points))
					ys := make([]float64, len(sr.Points))
					for j, pt := range sr.Points {
						xs[j], ys[j] = float64(pt.TS), pt.V
					}
					name := cutRunes(sr.Name, 14)
					if sr.Axis == 1 {
						name += "（右轴）"
					}
					sers = append(sers, LineSeries{
						Name: name, Color: bizChartColors[si%len(bizChartColors)],
						Axis: sr.Axis, Fill: sr.Fill, Xs: xs, Ys: ys,
					})
				}
				if len(sers) == 0 {
					continue
				}
				fig := figNo + figs
				img := DrawLineChart(sprintf("图 %d　%s", fig, bc.Title), sers, LineChartOpts{
					WarnLine: bc.WarnLine, WarnText: bc.WarnText, WarnAxis: bc.WarnAxis,
					YLabel: bc.YLabel, YLabelRight: bc.YLabelRight,
					YMax: bc.YMax, YMaxRight: bc.YMaxRight, Stacked: bc.Stacked,
				})
				blob, err := encodePNG(img)
				if err != nil {
					log.Warnf("业务巡检图表渲染失败（%s）：%v", bc.Title, err)
					continue
				}
				if err := d.Image(blob, 15.5); err != nil {
					log.Warnf("插入业务巡检图表失败（%s）：%v", bc.Title, err)
					continue
				}
				figs++
				d.Caption(sprintf("图 %d　%s　%s（按采样点连线）", fig, c.Inst.Raw, bc.Title))
			}
		}
	}

	return figs
}

// bizConclusion 生成「总体结论」里的业务巡检要点，返回结论文字与汇总状态。
// 文字形如：业务巡检覆盖 天空地森林防火：应用探活 6 个目标全部可用（平均响应 4.3～31.3 ms）；
// MySQL、PostgreSQL、Redis 共 3 个实例，其中 2 个存在越线项（MySQL 磁盘临时表（/s） 1.13、…）。
func bizConclusion(b *BizData) (string, string) {
	if b == nil || !b.HasData() {
		return "", bizNormal
	}
	var levels, segs []string

	if len(b.Probes) > 0 {
		normal := 0
		minAvg, maxAvg := math.Inf(1), 0.0
		var bad []string
		var certNames []string
		minCert := 9999
		for _, p := range b.Probes {
			levels = append(levels, p.Status)
			if p.Status == bizNormal {
				normal++
			} else {
				bad = append(bad, p.Name)
			}
			if bizProbeCertStatus(p) != bizNormal {
				certNames = append(certNames, p.Name)
				if p.CertDays < minCert {
					minCert = p.CertDays
				}
			}
			if p.Avg < minAvg {
				minAvg = p.Avg
			}
			if p.Avg > maxAvg {
				maxAvg = p.Avg
			}
		}
		if len(bad) == 0 {
			segs = append(segs, sprintf("应用探活 %d 个目标全部可用（平均响应 %.1f～%.1f ms）",
				len(b.Probes), minAvg, maxAvg))
		} else {
			segs = append(segs, sprintf("应用探活 %d 个目标中 %d 个各项指标正常，需关注 %s",
				len(b.Probes), normal, bizCutList(bad, 3)))
		}
		if len(certNames) > 0 {
			tail := sprintf("最紧剩余 %d 天", minCert)
			if minCert < 0 {
				tail = "已有证书过期"
			}
			segs = append(segs, sprintf("%d 个目标 HTTPS 证书 %d 天内到期（%s，%s）",
				len(certNames), bizCertWarnDays, bizCutList(certNames, 2), tail))
		}
	}

	if len(b.Comps) > 0 {
		var kinds []string
		seen := map[string]bool{}
		var abnormal int
		var detail []string
		for _, c := range b.Comps {
			levels = append(levels, c.Status)
			if !seen[c.Name] {
				seen[c.Name] = true
				kinds = append(kinds, c.Name)
			}
			if c.Status == bizNormal {
				continue
			}
			abnormal++
			for _, r := range c.Rows {
				if r.Status != bizNormal && len(detail) < 2 {
					detail = append(detail, sprintf("%s %s %s", c.Name, r.Label, r.Value))
				}
			}
		}
		if abnormal == 0 {
			segs = append(segs, sprintf("%s 共 %d 个实例，各项指标均在参考阈值内",
				strings.Join(kinds, "、"), len(b.Comps)))
		} else {
			segs = append(segs, sprintf("%s 共 %d 个实例，其中 %d 个存在越线项（%s）",
				strings.Join(kinds, "、"), len(b.Comps), abnormal, strings.Join(detail, "、")))
		}
	}

	if len(segs) == 0 {
		return "", bizNormal
	}
	prefix := ""
	if systems := b.Systems(); len(systems) > 0 {
		prefix = sprintf("业务巡检覆盖 %s：", bizCutList(systems, 2))
	}
	return prefix + strings.Join(segs, "；") + "。", bizWorst(levels...)
}

// bizCutList 列表截断：超过 n 项时只留前 n 项并加「等」
func bizCutList(items []string, n int) string {
	if len(items) <= n {
		return strings.Join(items, "、")
	}
	return strings.Join(items[:n], "、") + "等"
}

// bizDetailSummary 单实例结论句
func bizDetailSummary(c BizComponent) string {
	var hits []string
	for _, r := range c.Rows {
		if r.Status != bizNormal {
			hits = append(hits, sprintf("%s %s（参考阈值 %s）", r.Label, r.Value, r.Thresh))
		}
	}
	if len(hits) == 0 {
		return sprintf("该实例 %d 项指标均在参考阈值内，运行正常。", len(c.Rows))
	}
	return sprintf("该实例 %d 项指标中 %d 项越线：%s。", len(c.Rows), len(hits), strings.Join(hits, "；"))
}

func statusesOfProbes(ps []BizProbe) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Status
	}
	return out
}

func statusesOfComps(cs []BizComponent) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Status
	}
	return out
}

// bizAddr 服务地址只保留 host:port（完整 URL 过长会撑破表格）
func bizAddr(addr string) string {
	s := addr
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.Index(s, "/"); i >= 0 {
		s = s[:i]
	}
	return s
}

// bizCertText 证书到期列：默认只给到期日，临期补剩余天数、过期标明「已过期」作为提示
func bizCertText(p BizProbe) string {
	if p.CertExpire <= 0 {
		return "—"
	}
	day := bizCertDate(p.CertExpire)
	switch {
	case p.CertDays < 0:
		return sprintf("%s（已过期）", day)
	case p.CertDays < bizCertWarnDays:
		return sprintf("%s（%d 天）", day, p.CertDays)
	}
	return day
}

// certCount 配置了 HTTPS 证书（有到期时间）的探活目标数
func certCount(ps []BizProbe) int {
	n := 0
	for _, p := range ps {
		if p.CertExpire > 0 {
			n++
		}
	}
	return n
}

// bizChartColors 折线配色（与资源图表同一套色板）
var bizChartColors = []string{"2E75B6", "4F8A5B", "E36C09", "8064A2", "C0504D", "00A0A0"}

// bizCatSuffix 分类后缀：5.3 MySQL 数据库 / 5.5 Redis 缓存 / 5.6 Nginx 中间件
func bizCatSuffix(cat string) string {
	switch cat {
	case "数据库":
		return " 数据库"
	case "缓存":
		return " 缓存"
	}
	return " 中间件"
}

// cutRunes 截断到 n 个字符（图例过长会互相重叠）
func cutRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

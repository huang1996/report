package main

// analyze.go —— 指标分级、汇总与结论生成（口径与原 Python 脚本一致）

import "fmt"

// 五档分级（CPU/内存/磁盘）：正常 <75 → 提示 ≥75 → 警告 ≥80 → 严重 ≥90 → 紧急 ≥95
var (
	LV        = [4]string{"提示", "警告", "严重", "紧急"}
	TH        = map[string][4]float64{"CPU": {75, 80, 90, 95}, "内存": {75, 80, 90, 95}, "磁盘": {75, 80, 90, 95}}
	thText    = "≥75% 提示 / ≥80% 警告 / ≥90% 严重 / ≥95% 紧急"
	NET_TH    = [4]float64{200, 500, 800, 950}       // Mb/s，按 1 Gb 出口带宽估算
	CONN_TH   = [4]float64{800, 2000, 5000, 10000}   // 并发连接数（合计）
	IDLE_TH   = 20.0                                 // 低于该使用率 → 可优化
	WAVE_TH   = 40.0                                 // 峰谷差大于该值（百分点）视为负载波动异常
	warnIdx   = 1                                    // 图表预警线取「警告」档（≥80%）
	lvlOrder  = map[string]int{"紧急": 0, "危险": 1, "严重": 1, "预警": 2, "告警": 3, "警告": 3, "提示": 4, "可优化": 5, "低": 6, "正常": 7}
	fixedSugg = map[string]string{
		"提示": "核查监控曲线，记录指标基线，纳入日常巡检持续观察，暂不执行变更操作",
		"警告": "持续观察主机资源，登录主机定位高占用进程，排查定时任务 / 内存泄漏，评估参数调优",
		"严重": "持续观察资源情况，清理临时文件、重启异常进程等或者分流业务负载。避免高负载导致业务宕机。",
		"紧急": "通知相关负责人可能出现宕机风险；如长期保持95%以上，建议启动扩容程序，或者调整业务负载做分流。",
		"低":  "建议进行资源优化缩容。",
	}
)

// 状态 → 颜色（十六进制，供 docx 使用）
var statusColor = map[string]string{
	"紧急": "9C0006", "危险": "C00000", "告警": "C00000", "严重": "C00000", "高": "C00000", "超阈值": "C00000",
	"预警": "C55A11", "关注": "C55A11", "警告": "C55A11", "中": "C55A11", "处理中": "C55A11",
	"提示": "BF8F00", "可优化": "2E5CA6",
	"正常": "2E7530", "低": "2E7530", "已闭环": "2E7530", "已完成": "2E7530", "良好": "2E7530",
}

func level(name string, v float64) string {
	th := TH[name]
	for i := 3; i >= 0; i-- {
		if v >= th[i] {
			return LV[i]
		}
	}
	return "正常"
}

func levelAbs(th [4]float64, v float64) string {
	for i := 3; i >= 0; i-- {
		if v >= th[i] {
			return LV[i]
		}
	}
	return "正常"
}

func memLevel(v float64) string {
	if v < IDLE_TH {
		return "可优化"
	}
	return level("内存", v)
}

func suggest(lv string) string {
	if s, ok := fixedSugg[lv]; ok {
		return s
	}
	return fixedSugg["提示"]
}

func hostStatus(r *HostRow) string {
	diskPeak := r.DiskPeak
	if diskPeak == 0 {
		diskPeak = r.Disk
	}
	lv := []string{level("CPU", r.CPUPeak), memLevel(r.MemPeak), level("磁盘", diskPeak)}
	for _, x := range lv {
		if x == "紧急" || x == "严重" {
			return "告警"
		}
	}
	for _, x := range lv {
		if x == "警告" || x == "提示" {
			return "关注"
		}
	}
	return "正常"
}

func thLabel(name, lv string) string {
	if lv == "可优化" {
		return fmt.Sprintf("＜%.0f%%", IDLE_TH)
	}
	th := TH[name]
	for i, l := range LV {
		if l == lv {
			return fmt.Sprintf("≥%.0f%%", th[i])
		}
	}
	return "—"
}

func thAbsLabel(th [4]float64, lv, unit string) string {
	for i, l := range LV {
		if l == lv {
			return fmt.Sprintf("≥%.0f%s", th[i], unit)
		}
	}
	return "—"
}

// MetricAgg 单项指标的汇总水位
type MetricAgg struct {
	Cur, Peak, Mx float64
	Lv            string
}

type Analysis struct {
	Agg     map[string]MetricAgg
	Overall string
}

func analyze(rows []HostRow) *Analysis {
	n := float64(len(rows))
	a := &Analysis{Agg: map[string]MetricAgg{}}
	if len(rows) == 0 {
		a.Overall = "正常"
		return a
	}
	sum := func(f func(*HostRow) float64) float64 {
		s := 0.0
		for i := range rows {
			s += f(&rows[i])
		}
		return s / n
	}
	maxOf := func(f func(*HostRow) float64) float64 {
		m := f(&rows[0])
		for i := range rows {
			if v := f(&rows[i]); v > m {
				m = v
			}
		}
		return m
	}

	for _, key := range []string{"CPU", "内存"} {
		curF, pkF := func(r *HostRow) float64 { return r.CPU }, func(r *HostRow) float64 { return r.CPUPeak }
		lvF := func(r *HostRow) float64 { return r.CPUPeak }
		if key == "内存" {
			curF, pkF, lvF = func(r *HostRow) float64 { return r.Mem }, func(r *HostRow) float64 { return r.MemPeak }, func(r *HostRow) float64 { return r.MemPeak }
		}
		mx := maxOf(lvF)
		lv := level(key, mx)
		if key == "内存" {
			lv = memLevel(mx)
		}
		a.Agg[key] = MetricAgg{Cur: sum(curF), Peak: sum(pkF), Mx: mx, Lv: lv}
	}
	diskPeakOf := func(r *HostRow) float64 {
		if r.DiskPeak > 0 {
			return r.DiskPeak
		}
		return r.Disk
	}
	a.Agg["磁盘"] = MetricAgg{Cur: sum(func(r *HostRow) float64 { return r.Disk }),
		Peak: sum(diskPeakOf), Mx: maxOf(diskPeakOf), Lv: level("磁盘", maxOf(diskPeakOf))}
	// 网络带宽：阈值按单机 1 Gb 网卡估算，按单机口径统计
	netPeakOf := func(r *HostRow) float64 {
		if r.NetPeak > 0 {
			return r.NetPeak
		}
		return r.Net
	}
	a.Agg["网络"] = MetricAgg{Cur: sum(func(r *HostRow) float64 { return r.Net }),
		Peak: sum(netPeakOf), Mx: maxOf(netPeakOf), Lv: levelAbs(NET_TH, maxOf(netPeakOf))}
	// 并发连接数：按站点合计口径
	connSum := 0.0
	for i := range rows {
		connSum += rows[i].Conn
	}
	a.Agg["连接数"] = MetricAgg{Cur: connSum, Lv: levelAbs(CONN_TH, connSum)}

	overall := "正常"
	for i := range rows {
		rows[i].Status = hostStatus(&rows[i])
		rows[i].CPUGap = rows[i].CPUPeak - rows[i].CPU
		rows[i].MemGap = rows[i].MemPeak - rows[i].Mem
		if rows[i].Status == "告警" {
			overall = "告警"
		} else if rows[i].Status == "关注" && overall != "告警" {
			overall = "关注"
		}
	}
	a.Overall = overall
	return a
}

// Risk 风险台账行
type Risk struct {
	Level, Desc, Suggest string
}

// Plan 下周计划行
type Plan struct {
	No, Item string
}

// Findings 结论 / 风险 / 计划
type Findings struct {
	Concl []string
	Risks []Risk
	Plans []Plan
}

func buildFindings(rows []HostRow, a *Analysis) *Findings {
	f := &Findings{}
	if len(rows) == 0 {
		return f
	}
	m := a.Agg

	f.Concl = append(f.Concl, fmt.Sprintf(
		"本次巡检共采集 %d 台主机资源数据，CPU 平均使用率 %.2f%%、内存平均使用率 %.2f%%、磁盘平均使用率 %.2f%%，整体水位%s。",
		len(rows), m["CPU"].Cur, m["内存"].Cur, m["磁盘"].Cur,
		map[bool]string{true: "偏低", false: "处于合理区间"}[m["CPU"].Cur < 30]))

	if m["CPU"].Lv != "正常" {
		f.Concl = append(f.Concl, fmt.Sprintf("CPU 峰值最高达到 %.2f%%，达到「%s」级别（%s），存在短时突发负载压力。", m["CPU"].Mx, m["CPU"].Lv, thText))
	}
	if m["内存"].Lv != "正常" && m["内存"].Lv != "可优化" {
		f.Concl = append(f.Concl, fmt.Sprintf("内存峰值最高达到 %.2f%%，达到「%s」级别（%s），需关注高峰时段内存回收与释放情况。", m["内存"].Mx, m["内存"].Lv, thText))
	}
	if m["磁盘"].Lv != "正常" {
		f.Concl = append(f.Concl, fmt.Sprintf("磁盘使用率最高达到 %.2f%%，达到「%s」级别（%s），需关注容量增长趋势。", m["磁盘"].Mx, m["磁盘"].Lv, thText))
	}

	gap := 0.0
	for i := range rows {
		if rows[i].CPUGap > gap {
			gap = rows[i].CPUGap
		}
	}
	if gap >= WAVE_TH {
		f.Concl = append(f.Concl, fmt.Sprintf("CPU 峰谷差最大达 %.2f 个百分点（当前值与峰值严重背离），呈典型「潮汐型」负载特征，推测存在定时批处理、备份或集中访问等瞬时压力。", gap))
	}
	var idle []HostRow
	for _, r := range rows {
		if r.CPU < IDLE_TH && r.Mem < IDLE_TH && r.Disk < IDLE_TH {
			idle = append(idle, r)
		}
	}
	if len(idle) > 0 {
		f.Concl = append(f.Concl, fmt.Sprintf("其中 %d 台主机三项资源当前使用率均低于 %.0f%%，存在明显的资源闲置与成本优化空间。", len(idle), IDLE_TH))
	}

	// 风险与建议
	var raw []Risk
	addRisk := func(key string, isPeak bool) {
		lv := m[key].Lv
		if lv == "正常" || lv == "可优化" {
			return
		}
		val := m[key].Mx
		var desc string
		if key == "磁盘" {
			desc = fmt.Sprintf("磁盘使用率最高达 %.2f%%，达到「%s」级别，剩余可用空间持续收窄，存在写满导致业务中断的风险", val, lv)
		} else if isPeak {
			desc = fmt.Sprintf("%s 峰值最高达 %.2f%%，达到「%s」级别，业务高峰存在响应变慢、请求堆积甚至服务不可用的风险", key, val, lv)
		} else {
			desc = fmt.Sprintf("%s使用率达 %.2f%%，达到「%s」级别", key, val, lv)
		}
		raw = append(raw, Risk{Level: lv, Desc: desc, Suggest: suggest(lv)})
	}
	addRisk("CPU", true)
	addRisk("内存", true)
	addRisk("磁盘", false)

	if gap >= WAVE_TH {
		raw = append(raw, Risk{Level: "严重",
			Desc:    fmt.Sprintf("CPU 峰谷差最大达 %.2f 个百分点，当前值与峰值严重背离，呈「潮汐型」负载特征，按均值配置的监控会严重低估真实压力", gap),
			Suggest: suggest("严重")})
	}
	if len(idle) > 0 {
		totDisk, used := 0.0, 0.0
		for _, r := range idle {
			totDisk += r.DiskCapGB
			used += r.DiskCapGB * r.Disk / 100
		}
		pct := 0.0
		if totDisk > 0 {
			pct = used / totDisk * 100
		}
		raw = append(raw, Risk{Level: "低",
			Desc:    fmt.Sprintf("%d 台主机三项资源当前使用率均低于 %.0f%%（磁盘已用 %.1f GB / 总量 %.1f GB，利用率 %.2f%%），资源配置与实际需求不匹配，存在成本浪费", len(idle), IDLE_TH, used, totDisk, pct),
			Suggest: suggest("低")})
	}
	if len(raw) == 0 {
		raw = append(raw, Risk{Level: "低", Desc: "本期未发现超阈值项，各项资源水位均处于安全区间", Suggest: suggest("低")})
	}

	// 按等级排序后编号
	for i := 0; i < len(raw); i++ {
		for j := i + 1; j < len(raw); j++ {
			if lvlOrder[raw[j].Level] < lvlOrder[raw[i].Level] {
				raw[i], raw[j] = raw[j], raw[i]
			}
		}
	}
	f.Risks = raw

	// 下周计划（保留 2 条，重新编号）
	plans := []Plan{
		{"1", "峰值负载成因专项排查（Top 进程 / 慢查询 / 定时任务）"},
		{"2", "补充 1 分钟粒度采样，验证峰值持续时长"},
	}
	if len(idle) > 0 {
		plans = append(plans, Plan{"3", "输出资源降配 / 弹性伸缩评估方案"})
	}
	plans = append(plans,
		Plan{"4", "全量资产台账与 CMDB 一致性核对"},
		Plan{"5", "备份恢复演练与应急预案复核"})
	for i := range plans {
		plans[i].No = fmt.Sprintf("%d", i+1)
	}
	f.Plans = plans[:2]
	return f
}

func shortIP(ip string) string {
	p := splitDots(ip)
	if len(p) == 4 {
		return p[2] + "." + p[3]
	}
	return ip
}

func splitDots(s string) []string {
	var out []string
	cur := ""
	for _, c := range s {
		if c == '.' {
			out = append(out, cur)
			cur = ""
		} else {
			cur += string(c)
		}
	}
	out = append(out, cur)
	return out
}

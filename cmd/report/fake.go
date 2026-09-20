package main

// fake.go —— 统计周期内无数据时的伪造数据支持（由 -fake_when_empty 开启）
//
// 用途：巡检周期内 n9e 指标缺失（采集中断、数据源刚接入无历史数据、周期尚未产生数据等），
// 仍能生成结构完整的报告。
//
// 做法：
//   1. 先查询该数据源「最新数据」作为基准（取最近 1 小时）；
//   2. 以基准主机列表为母本，对各项使用率类指标做随机增减，生成该周期的伪造数据；
//   3. 基准查询失败或未取到有效数据时不做伪造，该周期仍按「无可用数据」处理。
//
// 说明：基准数据仅保存在内存中，不写入磁盘（每次运行都会重新查询）。
// 一次运行中的多个周期（例如按历史区间补齐二十多份周报）共用同一份内存基准，
// 避免每个周期都重复查询同一数据源。
//
// 约束：仅作用于 n9e 资源巡检指标。硬件规格（核数 / 内存容量 / 磁盘容量）与归属信息
// （网络分区 / 业务系统 / 主机角色 / 操作系统 / 工程师）保持基准原值，不做伪造。
// WAF（safeline）章节的数据与判定完全不受影响。

import (
	"fmt"
	"math/rand/v2"
	"time"
)

const (
	fakeBaseWindow = 3600 // 基准数据取样窗口（秒）：取数据源最近 1 小时
	fakeCurAmp     = 0.15 // 当前值随机增减幅度上限（±15%）
	fakePeakAmp    = 0.25 // 峰值随机增减幅度上限（±25%）
)

// n9eHasData 判断 n9e 采集结果是否可用：
// 无主机，或所有主机各项指标与采样率均为 0（无有效样本），都视为「本周期无数据」
func n9eHasData(rows []HostRow) bool {
	for i := range rows {
		r := &rows[i]
		if r.SampleRate > 0 || r.CPU > 0 || r.Mem > 0 || r.Disk > 0 ||
			r.Net > 0 || r.DiskIO > 0 || r.Conn > 0 {
			return true
		}
	}
	return false
}

// fakeBaseline 伪造基准：某数据源最近一次采集到的主机指标快照（仅存内存，不落盘）
type fakeBaseline struct {
	Base       string    // n9e 地址
	CapturedAt time.Time // 基准采集时刻
	Hosts      []HostRow // 基准主机指标
}

// fakeBaselineCache 本次运行内的基准缓存，按「n9e 地址 + 数据源」区分。
// 只活在进程内存里，不写文件：一次运行补齐多个周期时共用同一份基准。
var fakeBaselineCache = map[string]*fakeBaseline{}

func fakeBaselineKey(base, ds string) string { return base + "|" + ds }

// captureFakeBaseline 采集伪造基准：查询该数据源最近 1 小时的数据。
// 实时查询失败或没有有效数据时返回错误，由调用方按「本周期无可用数据」处理。
func captureFakeBaseline(c *N9EClient, cfg *Config, ds string) (*fakeBaseline, error) {
	if b, ok := fakeBaselineCache[fakeBaselineKey(c.base, ds)]; ok {
		log.Infof("沿用本次运行已采集的伪造基准：%d 台主机（采集于 %s）",
			len(b.Hosts), b.CapturedAt.Format("2006-01-02 15:04:05"))
		return b, nil
	}
	now := time.Now()
	ws := now.Add(-fakeBaseWindow * time.Second)
	rows, err := ReadN9E(c, ws.Unix(), now.Unix(), cfg.Step, identOpts(cfg))
	if err != nil {
		return nil, fmt.Errorf("基准查询失败：%w", err)
	}
	if !n9eHasData(rows) {
		return nil, fmt.Errorf("基准查询未取到有效数据（窗口 %s ~ %s）",
			ws.Format("2006-01-02 15:04"), now.Format("2006-01-02 15:04"))
	}
	b := &fakeBaseline{Base: c.base, CapturedAt: now, Hosts: rows}
	fakeBaselineCache[fakeBaselineKey(c.base, ds)] = b
	log.Infof("伪造基准采集完成：%d 台主机（基准窗口 %s ~ %s）",
		len(rows), ws.Format("2006-01-02 15:04"), now.Format("2006-01-02 15:04"))
	return b, nil
}

// fakeRows 生成该周期的伪造资源巡检数据（仅 n9e 指标）
func fakeRows(cfg *Config, c *N9EClient, ds string) ([]HostRow, error) {
	b, err := captureFakeBaseline(c, cfg, ds)
	if err != nil {
		return nil, err
	}
	rows := jitterRows(b.Hosts)
	if len(rows) == 0 {
		return nil, fmt.Errorf("基准中没有主机，无法伪造数据")
	}
	return rows, nil
}

// jitterRows 以基准主机列表为母本，对使用率类指标做随机增减。
// 规格与归属信息（核数 / 容量 / 分区 / 业务系统 / 角色 / 操作系统 / 工程师）保持基准原值。
func jitterRows(base []HostRow) []HostRow {
	out := make([]HostRow, 0, len(base))
	for i := range base {
		b := base[i]
		r := b
		r.CPU = jitterPct(b.CPU, fakeCurAmp)
		r.Mem = jitterPct(b.Mem, fakeCurAmp)
		r.Disk = jitterPct(b.Disk, fakeCurAmp)
		r.DiskIO = jitterPct(b.DiskIO, fakeCurAmp)
		r.CPUPeak = peakPct(b.CPUPeak, r.CPU)
		r.MemPeak = peakPct(b.MemPeak, r.Mem)
		r.DiskPeak = peakPct(b.DiskPeak, r.Disk)
		r.Net = jitterPos(b.Net, fakeCurAmp)
		r.NetPeak = peakPos(b.NetPeak, r.Net)
		if !b.NoConn {
			r.Conn = jitterPos(b.Conn, fakeCurAmp)
		}
		// 伪造数据按完整采样处理，避免触发「采样点不足」告警
		r.SampleRate = 0.95 + rand.Float64()*0.05
		out = append(out, r)
	}
	return out
}

// ---- 随机增减 ----

// jitterPct 百分比类指标：基准值为 0 时保持 0，否则在 ±amp 幅度内随机增减。
// 向上留出余量（越接近 100 上行空间越小），避免抖动后被夹到 100 这种一眼假的值
func jitterPct(v, amp float64) float64 {
	if v <= 0 {
		return 0
	}
	d := rand.Float64()*2 - 1
	if d >= 0 {
		return clampPct(v * (1 + d*headRoom(v, amp)))
	}
	return clampPct(v * (1 + d*amp))
}

// headRoom 计算向上抖动幅度：不超过 amp，且不超过「到 100% 的余量」的 90%
func headRoom(v, amp float64) float64 {
	if v <= 0 {
		return 0
	}
	room := (100 - v) / v * 0.9
	if room > amp {
		room = amp
	}
	if room < 0 {
		room = 0
	}
	return room
}

// jitterPos 非负数值指标（网络流量 Mb/s、连接数）
func jitterPos(v, amp float64) float64 {
	if v <= 0 {
		return 0
	}
	if nv := v * (1 + (rand.Float64()*2-1)*amp); nv > 0 {
		return nv
	}
	return 0
}

// peakPct 百分比类峰值：在基准峰值上抖动，且保证不低于当前值、不超过 100
func peakPct(basePeak, cur float64) float64 {
	v := jitterPct(basePeak, fakePeakAmp)
	if v < cur {
		v = bumpUpPct(cur, fakePeakAmp)
	}
	if v < cur {
		v = cur
	}
	return v
}

// bumpUpPct 在 cur 之上小幅抬升（同样受 headRoom 限制，不会顶到 100）
func bumpUpPct(cur, amp float64) float64 {
	if cur <= 0 {
		return cur
	}
	return clampPct(cur * (1 + rand.Float64()*headRoom(cur, amp)))
}

// peakPos 非负数值类峰值：在基准峰值上抖动，且保证不低于当前值
func peakPos(basePeak, cur float64) float64 {
	v := jitterPos(basePeak, fakePeakAmp)
	if v < cur {
		v = cur * (1 + rand.Float64()*fakePeakAmp)
	}
	if v < cur {
		v = cur
	}
	return v
}

func clampPct(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

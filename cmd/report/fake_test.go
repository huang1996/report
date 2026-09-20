package main

// fake_test.go —— 伪造数据逻辑单测：随机增减边界、规格不被篡改、基准不落盘、失败即放弃

import (
	"os"
	"testing"
	"time"
)

// withQuietLogger 测试期把日志级别压到 ERROR，避免 nil logger 或噪声输出
func withQuietLogger(t *testing.T) {
	t.Helper()
	old := log
	log = &Logger{level: 3}
	t.Cleanup(func() { log = old })
}

func sampleBaselineRows() []HostRow {
	return []HostRow{
		{Ident: "0-0-10.0.0.1-生产区-业务系统A-Web服务器-张三", IP: "10.0.0.1", Section: "生产区",
			Project: "业务系统A", Role: "Web服务器", Engineer: "张三", OS: "Kylin V10",
			Cores: 8, MemTotalGB: 32, DiskCapGB: 500,
			CPU: 23.1, CPUPeak: 61.2, Mem: 46.5, MemPeak: 72.3, Disk: 55.1, DiskPeak: 68.4,
			DiskIO: 12.4, Net: 88.3, NetPeak: 210.5, Conn: 432, SampleRate: 1},
		{Ident: "0-0-10.0.0.2-生产区-业务系统A-缓存服务器-张三", IP: "10.0.0.2", Section: "生产区",
			Project: "业务系统A", Role: "缓存服务器", Engineer: "张三", OS: "Ubuntu 22.04",
			Cores: 4, MemTotalGB: 16, DiskCapGB: 100,
			CPU: 8.9, CPUPeak: 22.1, Mem: 74.5, MemPeak: 80.2, Disk: 12.3, DiskPeak: 12.3,
			DiskIO: 0, Net: 30.5, NetPeak: 88.9, NoConn: true, SampleRate: 1},
	}
}

func TestN9eHasData(t *testing.T) {
	if n9eHasData(nil) {
		t.Error("无主机应视为无数据")
	}
	if n9eHasData([]HostRow{{IP: "10.0.0.1"}}) {
		t.Error("指标全零应视为无数据")
	}
	if !n9eHasData([]HostRow{{SampleRate: 0.8}}) {
		t.Error("采样率非零应视为有数据")
	}
	if !n9eHasData(sampleBaselineRows()) {
		t.Error("基准样例应视为有数据")
	}
}

func TestJitterRowsBoundsAndSpecs(t *testing.T) {
	base := sampleBaselineRows()
	rows := jitterRows(base)
	if len(rows) != len(base) {
		t.Fatalf("主机数应保持 %d，实际 %d", len(base), len(rows))
	}
	for i := range rows {
		r, b := rows[i], base[i]
		// 规格与归属信息必须保持基准原值
		if r.Ident != b.Ident || r.IP != b.IP || r.Section != b.Section || r.Project != b.Project ||
			r.Role != b.Role || r.Engineer != b.Engineer || r.OS != b.OS ||
			r.Cores != b.Cores || r.MemTotalGB != b.MemTotalGB || r.DiskCapGB != b.DiskCapGB {
			t.Errorf("第 %d 台主机的规格/归属信息被改动：%+v", i, r)
		}
		// 百分比类指标必须落在 [0,100]
		for name, v := range map[string]float64{
			"CPU": r.CPU, "内存": r.Mem, "磁盘": r.Disk, "磁盘IO": r.DiskIO,
			"CPU峰值": r.CPUPeak, "内存峰值": r.MemPeak, "磁盘峰值": r.DiskPeak,
		} {
			if v < 0 || v > 100 {
				t.Errorf("第 %d 台 %s=%v 超出 [0,100]", i, name, v)
			}
		}
		// 峰值不得低于当前值
		if r.CPUPeak < r.CPU || r.MemPeak < r.Mem || r.DiskPeak < r.Disk {
			t.Errorf("第 %d 台峰值低于当前值：CPU %v/%v 内存 %v/%v 磁盘 %v/%v",
				i, r.CPU, r.CPUPeak, r.Mem, r.MemPeak, r.Disk, r.DiskPeak)
		}
		if r.Net < 0 || r.NetPeak < r.Net {
			t.Errorf("第 %d 台网络流量异常：%v/%v", i, r.Net, r.NetPeak)
		}
		if b.NoConn && r.Conn != 0 {
			t.Errorf("第 %d 台本无连接数指标，却被伪造出 %v", i, r.Conn)
		}
		if !b.NoConn && r.Conn <= 0 {
			t.Errorf("第 %d 台连接数被清零", i)
		}
		if r.SampleRate <= 0.9 || r.SampleRate > 1 {
			t.Errorf("第 %d 台伪造数据采样率异常：%v", i, r.SampleRate)
		}
	}
}

func TestJitterRowsKeepsZero(t *testing.T) {
	rows := jitterRows([]HostRow{{IP: "10.0.0.9"}})
	r := rows[0]
	if r.CPU != 0 || r.Mem != 0 || r.Disk != 0 || r.DiskIO != 0 || r.Net != 0 || r.CPUPeak != 0 {
		t.Errorf("基准为 0 的指标不应被伪造出非零值：%+v", r)
	}
}

func TestJitterRowsVaries(t *testing.T) {
	base := sampleBaselineRows()
	constant := true
	for i := 0; i < 30; i++ {
		r := jitterRows(base)[0]
		if r.CPU != base[0].CPU || r.Mem != base[0].Mem || r.Net != base[0].Net {
			constant = false
			break
		}
	}
	if constant {
		t.Error("连续 30 次伪造的指标均与基准完全相同，随机增减未生效")
	}
}

func TestJitterPctHeadRoom(t *testing.T) {
	// 高水位指标不应被抖到整 100%（余量限制），否则一眼假
	for i := 0; i < 500; i++ {
		if v := jitterPct(97, fakePeakAmp); v >= 100 {
			t.Fatalf("97 抖动到 %v，顶到了 100", v)
		}
		if v := peakPct(96, 95); v >= 100 {
			t.Fatalf("峰值抖动到 %v，顶到了 100", v)
		}
	}
	// 低水位仍应保持完整 ±amp 幅度
	for i := 0; i < 200; i++ {
		if v := jitterPct(20, fakeCurAmp); v < 17 || v > 23 {
			t.Fatalf("20 抖动到 %v，超出 ±15%% 幅度", v)
		}
	}
}

// resetFakeBaselineCache 清空进程内基准缓存，保证测试之间互不干扰
func resetFakeBaselineCache() {
	fakeBaselineCache = map[string]*fakeBaseline{}
}

// unreachableN9E 指向一个必然连不上的地址，用于模拟「基准查询失败」
func unreachableN9E(ds string) *N9EClient {
	return NewN9EClient("http://127.0.0.1:1", ds, "", "", "", 2*time.Second, false)
}

// TestCaptureFakeBaselineQueryFailed 基准查询失败即返回错误，不再回退到任何本地缓存
func TestCaptureFakeBaselineQueryFailed(t *testing.T) {
	withQuietLogger(t)
	resetFakeBaselineCache()
	cfg := &Config{Step: 300}
	if _, err := captureFakeBaseline(unreachableN9E("3"), cfg, "3"); err == nil {
		t.Error("基准查询失败时应返回错误，由调用方按无数据跳过")
	}
}

// TestCaptureFakeBaselineNoLocalFile 基准不落盘：进程内缓存命中，且磁盘上不产生任何文件
func TestCaptureFakeBaselineNoLocalFile(t *testing.T) {
	withQuietLogger(t)
	resetFakeBaselineCache()
	dir := t.TempDir()
	cfg := &Config{ReportDir: dir, Step: 300}
	cli := NewN9EClient("http://n9e:17000", "7", "", "", "", 2*time.Second, false)
	// 直接注入进程内缓存，模拟「首个周期已采集完成」
	fakeBaselineCache[fakeBaselineKey(cli.base, "7")] = &fakeBaseline{
		Base: cli.base, CapturedAt: time.Now(), Hosts: sampleBaselineRows(),
	}
	b, err := captureFakeBaseline(cli, cfg, "7")
	if err != nil {
		t.Fatalf("应命中进程内基准，实际报错：%v", err)
	}
	if len(b.Hosts) != len(sampleBaselineRows()) {
		t.Errorf("基准主机数不符：%d", len(b.Hosts))
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读取输出目录失败：%v", err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("基准数据不应写入磁盘，输出目录却出现文件：%v", names)
	}
	// 不同数据源的基准互不串用
	if _, err := captureFakeBaseline(unreachableN9E("8"), cfg, "8"); err == nil {
		t.Error("未采集过的数据源不应命中缓存")
	}
}

// TestFakeRowsNoBaseline 无有效基准时不做伪造，直接返回错误
func TestFakeRowsNoBaseline(t *testing.T) {
	withQuietLogger(t)
	resetFakeBaselineCache()
	cfg := &Config{Step: 300}
	if _, err := fakeRows(cfg, unreachableN9E("5"), "5"); err == nil {
		t.Error("基准查询失败时不应产出伪造数据")
	}
}

// TestFakeRowsFromBaseline 有基准时，伪造结果可直接进入分析流程
func TestFakeRowsFromBaseline(t *testing.T) {
	withQuietLogger(t)
	resetFakeBaselineCache()
	cfg := &Config{Step: 300}
	cli := unreachableN9E("5")
	fakeBaselineCache[fakeBaselineKey(cli.base, "5")] = &fakeBaseline{
		Base: cli.base, CapturedAt: time.Now(), Hosts: sampleBaselineRows(),
	}
	rows, err := fakeRows(cfg, cli, "5")
	if err != nil {
		t.Fatalf("伪造数据失败：%v", err)
	}
	if !n9eHasData(rows) {
		t.Error("伪造出的数据应被视为有效数据")
	}
	a := analyze(rows)
	if a.Overall == "" {
		t.Error("伪造数据经 analyze 后综合评级为空")
	}
}

package main

// build_docx_test.go —— 报告成品（docx）的结构与排版回归测试。
//
// 这类问题（表号跳号/重号、表格对齐被误改）单看代码不容易发现，
// 必须回到「生成出来的 docx」上断言。本测试用内置样例数据跑一遍完整组装，
// 再解析 document.xml 核对表号连续性与各表格单元格对齐方式。

import (
	"archive/zip"
	"io"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

var (
	reTable = regexp.MustCompile(`(?s)<w:tbl>.*?</w:tbl>`)
	reRow   = regexp.MustCompile(`(?s)<w:tr>.*?</w:tr>`)
	reCell  = regexp.MustCompile(`(?s)<w:tc>.*?</w:tc>`)
	reAlign = regexp.MustCompile(`<w:jc w:val="(\w+)"/>`)
	reText  = regexp.MustCompile(`(?s)<w:t(?: [^>]*)?>(.*?)</w:t>`)
	reCapNo = regexp.MustCompile(`表 (\d+)　`)
	rePara  = regexp.MustCompile(`(?s)<w:p>.*?</w:p>`)
	reGrid  = regexp.MustCompile(`<w:gridCol w:w="(\d+)"/>`)
)

// tableGrid 取表格各列宽度（dxa，1cm = 567）
func tableGrid(t *testing.T, tb string) []float64 {
	t.Helper()
	var out []float64
	for _, m := range reGrid.FindAllStringSubmatch(tb, -1) {
		n, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatalf("解析列宽失败：%q", m[1])
		}
		out = append(out, float64(n))
	}
	return out
}

// tableRows 取表格的数据行（跳过表头）各单元格文本
func tableRows(tb string) [][]string {
	rows := reRow.FindAllString(tb, -1)
	var out [][]string
	for i, row := range rows {
		if i == 0 {
			continue
		}
		var vals []string
		for _, tc := range reCell.FindAllString(row, -1) {
			var b strings.Builder
			for _, m := range reText.FindAllStringSubmatch(tc, -1) {
				b.WriteString(m[1])
			}
			vals = append(vals, b.String())
		}
		out = append(out, vals)
	}
	return out
}

// docxPart 读取 docx 中指定部件的内容
func docxPart(t *testing.T, path, name string) string {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("打开 docx 失败：%v", err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("读取 %s 失败：%v", name, err)
		}
		defer rc.Close()
		b, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("读取 %s 失败：%v", name, err)
		}
		return string(b)
	}
	t.Fatalf("docx 中不存在部件 %s", name)
	return ""
}

// tableHeader 取表格表头行的各单元格文本
func tableHeader(tb string) string {
	rows := reRow.FindAllString(tb, -1)
	if len(rows) == 0 {
		return ""
	}
	var cells []string
	for _, tc := range reCell.FindAllString(rows[0], -1) {
		var b strings.Builder
		for _, m := range reText.FindAllStringSubmatch(tc, -1) {
			b.WriteString(m[1])
		}
		cells = append(cells, b.String())
	}
	return strings.Join(cells, "|")
}

// tableAligns 统计表格内所有单元格的对齐方式
func tableAligns(tb string) map[string]int {
	out := map[string]int{}
	for _, tc := range reCell.FindAllString(tb, -1) {
		if m := reAlign.FindStringSubmatch(tc); m != nil {
			out[m[1]]++
		} else {
			out["(none)"]++
		}
	}
	return out
}

// buildDemoDocx 用内置样例数据生成一份完整报告，返回 docx 路径
func buildDemoDocx(t *testing.T) string {
	t.Helper()
	old := log
	log = &Logger{level: 3} // 只放行 ERROR，避免测试输出噪音；不落盘
	defer func() { log = old }()

	dir := t.TempDir()
	now := time.Date(2026, 9, 23, 0, 0, 0, 0, time.Local)
	cfg := &Config{
		ReportDir:  dir,
		Out:        JoinPath(dir, "report.docx"),
		ChartTop:   5,
		HeaderText: "统筹运维项目",
	}
	out, err := BuildReport(&BuildInput{
		Cfg: cfg, Rows: demoRows(), Waf: demoWaf(),
		WS: now.AddDate(0, 0, -4), WE: now, Source: "单元测试",
	})
	if err != nil {
		t.Fatalf("生成报告失败：%v", err)
	}
	return out
}

// TestHeaderCarriesVersion 页眉必须带程序版本号，格式「<页眉>-0.1.x」
func TestHeaderCarriesVersion(t *testing.T) {
	out := buildDemoDocx(t)
	hdr := docxPart(t, out, "word/header1.xml")
	var b strings.Builder
	for _, m := range reText.FindAllStringSubmatch(hdr, -1) {
		b.WriteString(m[1])
	}
	got := strings.TrimSpace(b.String())
	ver := strings.TrimLeft(strings.TrimSpace(AppVersion), "vV")
	if want := "统筹运维项目-" + ver; got != want {
		t.Errorf("页眉 = %q，期望 %q", got, want)
	}
}

// TestTableNumbersSequential 表号必须与「在文档中出现的先后」一致，连续且不重号。
func TestTableNumbersSequential(t *testing.T) {
	out := buildDemoDocx(t)
	doc := docxPart(t, out, "word/document.xml")

	// 按文档顺序收集所有「表 N　」题注
	var got []int
	for _, m := range rePara.FindAllString(doc, -1) {
		var b strings.Builder
		for _, tm := range reText.FindAllStringSubmatch(m, -1) {
			b.WriteString(tm[1])
		}
		if cap := reCapNo.FindStringSubmatch(b.String()); cap != nil {
			n := 0
			for _, ch := range cap[1] {
				n = n*10 + int(ch-'0')
			}
			got = append(got, n)
		}
	}
	if len(got) == 0 {
		t.Fatal("未解析到任何表号题注")
	}
	for i, n := range got {
		if n != i+1 {
			t.Errorf("第 %d 个表号 = %d，期望 %d（全部：%v）", i+1, n, i+1, got)
		}
	}
}

// TestTableAlignment 对齐规则：
//   - 「风险研判与优化建议」（风险台账）与「下周巡检重点」两节的表格保留默认对齐
//     （长文本左对齐，便于阅读）；
//   - 其余表格全部单元格居中。
func TestTableAlignment(t *testing.T) {
	out := buildDemoDocx(t)
	doc := docxPart(t, out, "word/document.xml")
	tables := reTable.FindAllString(doc, -1)
	if len(tables) < 10 {
		t.Fatalf("表格数 = %d，明显偏少，疑似组装中断", len(tables))
	}

	sawRisk, sawPlan, sawDisk := false, false, false
	for i, tb := range tables {
		head := tableHeader(tb)
		aligns := tableAligns(tb)

		switch {
		case strings.HasPrefix(head, "编号|风险描述"):
			sawRisk = true
			if aligns["left"] == 0 {
				t.Errorf("风险台账（第 %d 个表）应保留长文本左对齐，实得 %v", i+1, aligns)
			}
		case strings.HasPrefix(head, "序号|巡检"):
			sawPlan = true
			if aligns["left"] == 0 {
				t.Errorf("下周巡检重点（第 %d 个表）应保留长文本左对齐，实得 %v", i+1, aligns)
			}
		default:
			if aligns["left"] > 0 || aligns["(none)"] > 0 {
				t.Errorf("第 %d 个表（表头 %q）应整表居中，实得 %v", i+1, head, aligns)
			}
			if aligns["center"] == 0 {
				t.Errorf("第 %d 个表（表头 %q）没有任何居中单元格", i+1, head)
			}
		}
		if strings.HasPrefix(head, "IP 地址|挂载点|文件系统") {
			sawDisk = true
		}
	}
	if !sawRisk {
		t.Error("未找到风险台账表格，测试前提已失效")
	}
	if !sawPlan {
		t.Error("未找到下周巡检重点工作表格，测试前提已失效")
	}
	if !sawDisk {
		t.Error("未找到「磁盘分区使用明细」表格（表头应为 IP 地址|挂载点|文件系统|容量|已用|使用率|状态）")
	}
}

// TestDiskPartsTableContent 分区明细表的内容与过滤：伪分区不得出现，容量与使用率须成对出现。
func TestDiskPartsTableContent(t *testing.T) {
	out := buildDemoDocx(t)
	doc := docxPart(t, out, "word/document.xml")

	var diskTb string
	for _, tb := range reTable.FindAllString(doc, -1) {
		if strings.HasPrefix(tableHeader(tb), "IP 地址|挂载点|文件系统") {
			diskTb = tb
			break
		}
	}
	if diskTb == "" {
		t.Fatal("未找到磁盘分区使用明细表")
	}

	var paths []string
	for i, row := range reRow.FindAllString(diskTb, -1) {
		if i == 0 {
			continue
		}
		cells := reCell.FindAllString(row, -1)
		if len(cells) < 6 {
			t.Fatalf("分区明细行只有 %d 列", len(cells))
		}
		var vals []string
		for _, tc := range cells {
			var b strings.Builder
			for _, m := range reText.FindAllStringSubmatch(tc, -1) {
				b.WriteString(m[1])
			}
			vals = append(vals, b.String())
		}
		paths = append(paths, vals[1])
		// 文件系统列不得出现内存文件系统
		if isPseudoFSType(vals[2]) {
			t.Errorf("分区明细出现了伪文件系统：%v", vals)
		}
		if !strings.HasSuffix(vals[5], "%") {
			t.Errorf("使用率列格式异常：%v", vals)
		}
	}
	if len(paths) == 0 {
		t.Fatal("分区明细表只有表头，没有数据行")
	}
	// 样例数据里应能看到真实分区；伪分区（/run、/dev、/sys）一律不得出现
	for _, p := range paths {
		if strings.HasPrefix(p, "/run") || strings.HasPrefix(p, "/dev") || strings.HasPrefix(p, "/sys") {
			t.Errorf("分区明细出现伪挂载点 %q", p)
		}
	}
}

// isPseudoFSType 判断文件系统类型是否属于不应展示的伪文件系统
func isPseudoFSType(fs string) bool {
	return pseudoFSRe.MatchString(strings.ToLower(strings.TrimSpace(fs)))
}

// TestHostOwnTableColumns 表 3「主机归属与资源规格」的列构成与列宽：
//   - CPU规格 之后必须有 CPU架构 列（架构取值 amd64 / arm64 等）；
//   - CPU规格 列宽固定 1.5cm；
//   - 操作系统列吸收页面剩余宽度，使整表铺满页面可用宽度（pageWidthCm）。
func TestHostOwnTableColumns(t *testing.T) {
	out := buildDemoDocx(t)
	doc := docxPart(t, out, "word/document.xml")

	var tb string
	for _, x := range reTable.FindAllString(doc, -1) {
		if strings.HasPrefix(tableHeader(x), "IP 地址|主机角色|CPU规格|CPU架构|") {
			tb = x
			break
		}
	}
	if tb == "" {
		t.Fatal("未找到表 3（表头应为 IP 地址|主机角色|CPU规格|CPU架构|内存容量|磁盘容量|操作系统）")
	}
	if got := tableHeader(tb); got != "IP 地址|主机角色|CPU规格|CPU架构|内存容量|磁盘容量|操作系统" {
		t.Errorf("表 3 表头 = %q，与预期列构成不符", got)
	}

	grid := tableGrid(t, tb)
	if len(grid) != 7 {
		t.Fatalf("表 3 列数 = %d，期望 7（grid=%v）", len(grid), grid)
	}
	// 1cm = 567 dxa。CPU规格（第 3 列，下标 2）固定 1.5cm
	if want := 1.5 * 567; grid[2] < want-2 || grid[2] > want+2 {
		t.Errorf("CPU规格列宽 = %.0f dxa，期望约 %.0f dxa（1.5cm）", grid[2], want)
	}
	// 整表应铺满页面可用宽度（各列取整后允许数 dxa 的误差）
	sum := 0.0
	for _, w := range grid {
		sum += w
	}
	if want := pageWidthCm * 567; sum < want-6 || sum > want+6 {
		t.Errorf("表 3 总宽 = %.0f dxa，期望约 %.0f dxa（%.1fcm，操作系统列吸收剩余宽度）", sum, want, pageWidthCm)
	}
	// 操作系统列应明显宽于其余文本列（吸收剩余宽度）
	if grid[6] <= grid[2] {
		t.Errorf("操作系统列宽 = %.0f，应吸收剩余宽度并宽于 CPU规格列（%.0f）", grid[6], grid[2])
	}

	// 数据行：CPU架构列（第 4 列，下标 3）不得为空串，且应出现多种架构取值
	archs := map[string]int{}
	rows := tableRows(tb)
	if len(rows) == 0 {
		t.Fatal("表 3 没有数据行")
	}
	for _, r := range rows {
		if len(r) != 7 {
			t.Fatalf("表 3 数据行列数 = %d，期望 7：%v", len(r), r)
		}
		archs[r[3]]++
	}
	for a := range archs {
		if a == "" {
			t.Errorf("存在 CPU 架构为空的数据行（应显示「—」占位）：%v", archs)
		}
	}
	if archs["amd64"] == 0 || archs["arm64"] == 0 {
		t.Errorf("样例数据应同时包含 amd64 与 arm64 架构，实得 %v", archs)
	}
}

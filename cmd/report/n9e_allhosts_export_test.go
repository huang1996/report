package main

// n9e_allhosts_export_test.go —— 按当前处理逻辑遍历 n9e 全部数据源，
// 把每台主机的「IP / 主机角色 / CPU规格 / 内存容量 / 磁盘容量 / 操作系统」
// （即报告中表 3 的字段）导出为 xlsx，便于人工核对 ident 解析结果。
//
// 默认跳过（需要连真实 n9e）；执行方式：
//
//	go test ./cmd/report/ -run TestExportAllHostsToExcel -v -timeout 40m -count=1 \
//	  -args -n9e_base=https://n9e.lzsmartcity.com -insecure -out=输出/全部主机清单.xlsx
//
// 可用 -only-ds=18,56 只跑指定数据源，-days=7 控制回溯天数。
// 注意加 -count=1：Go 默认会缓存上一次的成功结果，不加则可能直接复用缓存而不真正联网。

import (
	"archive/zip"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// 测试参数：用独立的 FlagSet + flag.Parse 读取 `go test -args` 之后的部分。
// 参数名与主程序保持一致，便于复用使用直觉（n9e_base / insecure / step ...）。
var (
	flagN9EBase    = flag.String("n9e_base", "", "n9e 服务地址")
	flagInsecure   = flag.Bool("insecure", false, "跳过 HTTPS 证书校验")
	flagOut        = flag.String("out", "", "导出 xlsx 路径（留空用默认）")
	flagOnlyDS     = flag.String("only-ds", "", "只处理指定数据源 ID，逗号分隔；留空=全部")
	flagDays       = flag.Int("days", 7, "回溯天数")
	flagStep       = flag.Int("step", 300, "采样步长（秒）")
	flagEngineerTl = flag.String("ident_engineer_tail", "auto", "【已弃用】不影响解析结果，仅为兼容保留")
	flagLayout     = flag.String("layout", "auto", "ident 命名规则")
	flagN9EUser    = flag.String("n9e_user", "", "n9e 登录账号")
	flagN9EPass    = flag.String("n9e_pass", "", "n9e 登录密码")
	flagN9EToken   = flag.String("n9e_token", "", "n9e 个人令牌")
	flagSkipNoData = flag.Bool("skip-no-data", true, "无数据的数据源直接跳过（不报错）")
)

// exportedHost 一行导出记录（在 HostRow 基础上附加来源数据源，便于跨源核对）
type exportedHost struct {
	DS   int    // 数据源 ID
	Name string // 数据源名称
	HostRow
}

// TestMain 解析 `go test -args` 之后的自定义参数（标准 flag.Parse 只认到 -args 为止）
func TestMain(m *testing.M) {
	flag.Parse()
	os.Exit(m.Run())
}

func TestExportAllHostsToExcel(t *testing.T) {
	if *flagN9EBase == "" {
		t.Skip("未提供 -n9e_base，跳过（需连真实 n9e）")
	}
	setupLogger("INFO") // 包级 log 只在 main 里初始化，测试中必须先建

	base := strings.TrimRight(*flagN9EBase, "/")
	cli := NewN9EClient(base, "", *flagN9EToken, *flagN9EUser, *flagN9EPass, 30*time.Second, *flagInsecure)

	// 1. 列出全部数据源
	all, err := cli.ListDatasources()
	if err != nil {
		t.Fatalf("列举数据源失败：%v", err)
	}
	only := map[int]bool{}
	for _, s := range strings.Split(*flagOnlyDS, ",") {
		if s = strings.TrimSpace(s); s != "" {
			if n, err := strconv.Atoi(s); err == nil {
				only[n] = true
			}
		}
	}
	if len(only) > 0 {
		t.Logf("仅处理指定数据源：%v", only)
	}

	end := time.Now().Unix()
	start := end - int64(*flagDays)*86400

	// 2. 逐数据源按现有逻辑采集
	var hosts []exportedHost
	var okDS, skipDS, failDS []string
	for _, ds := range all {
		id := dsID(ds.ID)
		if len(only) > 0 && !only[id] {
			continue
		}
		dsName := strings.TrimSpace(ds.Name)
		cli.ds = strconv.Itoa(id)
		opts := IdentOpts{
			// 不带关键字过滤：导出该数据源下的全部主机
			Filter:       "",
			Layout:       *flagLayout,
			EngineerTail: *flagEngineerTl,
		}
		// 数据源较多，单个源网络抖动不应影响整跑：失败重试一次
		var rows []HostRow
		var err error
		for attempt := 1; attempt <= 2; attempt++ {
			rows, err = ReadN9E(cli, start, end, *flagStep, opts)
			if err == nil || strings.Contains(err.Error(), "未返回任何主机数据") {
				break
			}
			if attempt < 2 {
				t.Logf("ds%-3d 第 %d 次采集失败（%v），2s 后重试", id, attempt, err)
				time.Sleep(2 * time.Second)
			}
		}
		if err != nil {
			msg := fmt.Sprintf("#%d %s：%v", id, dsName, err)
			// 无数据 / 网络类失败 → 记为跳过（清单导出不因此判失败），其余视为真错误
			if strings.Contains(err.Error(), "未返回任何主机数据") ||
				strings.Contains(err.Error(), "timeout") ||
				strings.Contains(err.Error(), "connection") ||
				strings.Contains(err.Error(), "HTTP 5") {
				skipDS = append(skipDS, msg)
				t.Logf("跳过 ds%-3d %s（%v）", id, truncRunes(dsName, 30), err)
				continue
			}
			failDS = append(failDS, msg)
			t.Errorf("采集失败 %s", msg)
			continue
		}
		sortHosts(rows)
		okDS = append(okDS, fmt.Sprintf("#%d %s（%d 台）", id, dsName, len(rows)))
		for _, r := range rows {
			hosts = append(hosts, exportedHost{DS: id, Name: dsName, HostRow: r})
		}
		t.Logf("ds%-3d %-28s 主机 %d 台", id, truncRunes(dsName, 26), len(rows))
	}

	// 3. 写 xlsx（相对路径以仓库根为基准，避免落到包目录下）
	out := *flagOut
	if out == "" {
		out = filepath.Join("输出", fmt.Sprintf("全部主机清单_%s.xlsx", time.Now().Format("20060102_1504")))
	}
	if !filepath.IsAbs(out) {
		out = filepath.Join(repoRoot(), out)
	}
	if err := writeHostsXLSX(out, hosts); err != nil {
		t.Fatalf("写 xlsx 失败：%v", err)
	}

	t.Logf("===== 汇总 =====")
	t.Logf("成功数据源 %d 个，跳过 %d 个，失败 %d 个；主机合计 %d 台",
		len(okDS), len(skipDS), len(failDS), len(hosts))
	for _, s := range okDS {
		t.Logf("  OK   %s", s)
	}
	for _, s := range skipDS {
		t.Logf("  SKIP %s", s)
	}
	for _, s := range failDS {
		t.Logf("  FAIL %s", s)
	}
	t.Logf("已导出：%s", out)

	// 4. 自检：核对 ident 解析结果。
	//    注意区分「代码缺陷」与「数据客观情况」——后者只记录、不判失败：
	//      · ident 本身没有角色段（主机名即项目名，如 000011-192.168.4.6-泸州智慧治理平台）；
	//      · 无 IP 段的数据源（租户-项目-角色，真实 IP 来自 system_info.host_ip）。
	//    同一主机的历史残留 ident 已由 dedupeByIdent 在采集阶段剔除，故此处 IP 必须规范。
	var badIP, noRole, noSpec, inferredEng int
	var badIPList, noRoleList []string
	for _, h := range hosts {
		// IP 必须能由「ident 中的 IP 段」或「host_ip 标签」给出；两者都拿不到才算真缺陷
		if !ipRe.MatchString(h.IP) {
			badIP++
			badIPList = append(badIPList, sprintf("ds%d ident=%q IP=%q", h.DS, h.Ident, h.IP))
			continue
		}
		// 工程师不得由 ident 推断 —— 必须恒为占位值（手动指定在 main 里统一覆盖，此处不涉及）
		if h.Engineer != "" && h.Engineer != "—" {
			inferredEng++
			t.Logf("工程师仍被推断：ds%d ident=%q Engineer=%q", h.DS, h.Ident, h.Engineer)
		}
		if h.Role == "" || h.Role == "—" {
			noRole++
			noRoleList = append(noRoleList, sprintf("ds%d %s", h.DS, h.Ident))
			continue
		}
		if h.Cores == 0 {
			noSpec++
			t.Logf("未取到核数：ds%d %s", h.DS, h.Ident)
		}
	}
	t.Logf("自检：非法 IP %d 台 / 角色缺失 %d 台 / 核数缺失 %d 台 / 工程师被误推断 %d 台",
		badIP, noRole, noSpec, inferredEng)
	if inferredEng > 0 {
		t.Errorf("有 %d 台主机的运维工程师由 ident 推断而来，应恒为「—」（见 parseIdent 注释）", inferredEng)
	}
	if badIP > 0 {
		t.Logf("—— 非法 IP 明细（ident 内无 IP 段且 host_ip 缺失；含服务端历史残留 ident）——")
		for _, s := range badIPList {
			t.Logf("   %s", s)
		}
	}
	if noRole > 0 {
		t.Logf("—— 角色缺失明细（多数为 ident 本身不含角色段，属数据源命名约定）——")
		for i, s := range noRoleList {
			if i >= 15 {
				t.Logf("   ...（共 %d 条，完整明细见导出的 xlsx）", len(noRoleList))
				break
			}
			t.Logf("   %s", s)
		}
	}
	if badIP > 0 {
		t.Errorf("存在 %d 台主机既无 IP 段也无可回填的 host_ip，请核对上列 ident", badIP)
	}
}

// ---- xlsx 生成（纯手写 OOXML，不引入第三方依赖）----

// writeHostsXLSX 把主机清单写成单 sheet 的 xlsx
func writeHostsXLSX(path string, hosts []exportedHost) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	zw := zip.NewWriter(f)

	type item struct {
		name string
		body string
	}
	files := []item{
		{"[Content_Types].xml", xlsxContentTypes},
		{"_rels/.rels", xlsxRootRels},
		{"xl/workbook.xml", xlsxWorkbook},
		{"xl/_rels/workbook.xml.rels", xlsxWorkbookRels},
		{"xl/styles.xml", xlsxStyles},
		{"xl/worksheets/sheet1.xml", buildSheetXML(hosts)},
	}
	for _, it := range files {
		w, err := zw.Create(it.name)
		if err != nil {
			return err
		}
		if _, err := w.Write([]byte(it.body)); err != nil {
			return err
		}
	}
	return zw.Close()
}

// buildSheetXML 生成 sheet1.xml：第 1 行表头，随后每台主机一行。
// 列：数据源ID / 数据源名称 / IP 地址 / 主机角色 / CPU规格 / 内存容量 / 磁盘容量 / 操作系统 / ident（辅助核对）
func buildSheetXML(hosts []exportedHost) string {
	headers := []string{"数据源ID", "数据源名称", "IP 地址", "主机角色", "CPU规格", "内存容量", "磁盘容量", "操作系统", "ident（核对用）"}

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n")
	b.WriteString(`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">`)
	// sheetViews 需在 sheetData 之前：冻结首行（表头）
	b.WriteString(`<sheetViews><sheetView tabSelected="1" workbookViewId="0">` +
		`<pane ySplit="1" topLeftCell="A2" activePane="bottomLeft" state="frozen"/>` +
		`<selection pane="bottomLeft" activeCell="A2" sqref="A2"/>` +
		`</sheetView></sheetViews>`)
	// 列宽：ident 与数据源名称较长
	b.WriteString(`<cols>`)
	for i, w := range []float64{9, 30, 16, 22, 10, 12, 12, 34, 46} {
		fmt.Fprintf(&b, `<col min="%d" max="%d" width="%g" customWidth="1"/>`, i+1, i+1, w)
	}
	b.WriteString(`</cols><sheetData>`)

	// 表头（style 1 = 加粗居中 + 底色 + 边框）
	b.WriteString(`<row r="1" ht="20" customHeight="1">`)
	for ci, h := range headers {
		fmt.Fprintf(&b, `<c r="%s1" s="1" t="inlineStr"><is><t>%s</t></is></c>`, colName(ci), esc(h))
	}
	b.WriteString(`</row>`)

	for ri, h := range hosts {
		r := ri + 2
		fmt.Fprintf(&b, `<row r="%d">`, r)
		cells := []struct {
			v  string
			st int
		}{
			{strconv.Itoa(h.DS), 0},
			{h.Name, 0},
			{h.IP, 0},                             // IP：文本，避免被识别成数字
			{orDash(h.Role), 0},                   // 角色
			{sprintf("%.0f 核", h.Cores), 0},       // CPU 规格
			{sprintf("%.0f GB", h.MemTotalGB), 0}, // 内存容量
			{sprintf("%.0f GB", h.DiskCapGB), 0},  // 磁盘容量
			{orDash(h.OS), 0},                     // 操作系统
			{h.Ident, 0},                          // ident 核对
		}
		for ci, c := range cells {
			fmt.Fprintf(&b, `<c r="%s%d" s="%d" t="inlineStr"><is><t xml:space="preserve">%s</t></is></c>`,
				colName(ci), r, c.st, esc(c.v))
		}
		b.WriteString(`</row>`)
	}
	b.WriteString(`</sheetData>`)
	// 冻结首行 + 自动筛选，便于按数据源/角色筛。
	// 注意 OOXML 元素顺序：autoFilter 在 sheetData 之后，冻结写在 sheetView/pane 里。
	fmt.Fprintf(&b, `<autoFilter ref="A1:%s%d"/>`, colName(len(headers)-1), len(hosts)+1)
	b.WriteString(`</worksheet>`)
	return b.String()
}

// colName 把 0 基列号转为 Excel 列名（0→A，25→Z，26→AA）
func colName(i int) string {
	name := ""
	for i >= 0 {
		name = string(rune('A'+i%26)) + name
		i = i/26 - 1
	}
	return name
}

func truncRunes(s string, n int) string {
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n-1]) + "…"
}

// repoRoot 返回仓库根目录（测试运行时 CWD 是包目录 cmd/report，需上溯两级）
func repoRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	// 包目录下存在 go 源文件；若 CWD 已是根目录则直接用
	if _, err := os.Stat(filepath.Join(wd, "go.mod")); err == nil {
		return wd
	}
	return filepath.Dir(filepath.Dir(wd))
}

const xlsxContentTypes = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
<Default Extension="xml" ContentType="application/xml"/>
<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>
<Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>
<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>
</Types>`

const xlsxRootRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>
</Relationships>`

const xlsxWorkbook = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
<sheets><sheet name="全部主机" sheetId="1" r:id="rId1"/></sheets>
</workbook>`

const xlsxWorkbookRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>
<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>
</Relationships>`

// xlsxStyles：0=普通文本，1=表头（加粗、居中、浅灰底、边框）
const xlsxStyles = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
<fonts count="2">
<font><sz val="11"/><name val="等线"/></font>
<font><b/><sz val="11"/><name val="等线"/></font>
</fonts>
<fills count="3">
<fill><patternFill patternType="none"/></fill>
<fill><patternFill patternType="gray125"/></fill>
<fill><patternFill patternType="solid"><fgColor rgb="FFD9E1F2"/><bgColor indexed="64"/></patternFill></fill>
</fills>
<borders count="2">
<border><left/><right/><top/><bottom/><diagonal/></border>
<border><left style="thin"><color rgb="FFBFBFBF"/></left><right style="thin"><color rgb="FFBFBFBF"/></right><top style="thin"><color rgb="FFBFBFBF"/></top><bottom style="thin"><color rgb="FFBFBFBF"/></bottom><diagonal/></border>
</borders>
<cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs>
<cellXfs count="2">
<xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0" applyAlignment="1"><alignment vertical="center"/></xf>
<xf numFmtId="0" fontId="1" fillId="2" borderId="1" xfId="0" applyFont="1" applyFill="1" applyBorder="1" applyAlignment="1"><alignment horizontal="center" vertical="center"/></xf>
</cellXfs>
</styleSheet>`

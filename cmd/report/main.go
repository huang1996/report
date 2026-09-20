package main

// main.go —— 合并版巡检周报生成器入口
// 数据源：夜莺监控 n9e（资源巡检，报告主体）+ Safeline WAF PostgreSQL（安全巡检章节）
// 输出：单个 docx；支持 WebDAV 上传；支持 -now 立即执行或每日定时执行。

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"os"
	"strconv"
	"strings"
	"time"
)

func sprintf(format string, args ...interface{}) string {
	return fmt.Sprintf(format, args...)
}

func openFile(p string) (*os.File, error) { return os.Open(p) }

// encodePNG 把图片编码为 PNG 字节流
func encodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func main() {
	cfg := parseConfig()

	if cfg.ShowVersion {
		fmt.Printf("%s %s\n", AppName, Version())
		return
	}

	setupLogger(cfg.LogLevel)
	log.Infof("%s %s 启动", AppName, Version())
	log.Debugf("配置加载完成：n9e=%s ds=%s project=%s db=%s",
		cfg.Base, cfg.DS, cfg.Project, maskURL(cfg.DatabaseURL))

	fontSrc, err := LoadChartFont(fontCandidates)
	if err != nil {
		log.Errorf("%v（图表文字将无法渲染）", err)
	} else {
		log.Infof("图表字体：%s", fontSrc)
	}

	if cfg.ListDS {
		runListDS(cfg)
		return
	}

	if cfg.Now {
		windows := resolveWindows(cfg, time.Now())
		if len(windows) == 0 {
			return
		}
		for _, w := range windows {
			runOnce(cfg, w.ws, w.we)
		}
		return
	}

	// 定时模式：每天 REPORT_TIME 触发一次（与原 safeline-report 相同的常驻方式）
	if len(cfg.RunWeekdays) > 0 {
		names := []string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}
		var desc []string
		for _, wd := range cfg.RunWeekdays {
			if wd >= 0 && wd <= 6 {
				desc = append(desc, names[wd])
			}
		}
		log.Infof("进入定时模式，每天 %s 触发，仅在 %s 生成报告（可用 -now 立即执行一次）",
			cfg.ReportTime, strings.Join(desc, "、"))
	} else {
		log.Infof("进入定时模式，每天 %s 自动生成巡检周报（可用 -now 立即执行一次）", cfg.ReportTime)
	}
	for {
		next := nextRun(cfg.ReportTime)
		log.Infof("下次执行时间：%s", next.Format("2006-01-02 15:04:05"))
		time.Sleep(time.Until(next))
		if len(cfg.RunWeekdays) > 0 {
			wd := int(time.Now().Weekday())
			allowed := false
			for _, d := range cfg.RunWeekdays {
				if d == wd {
					allowed = true
					break
				}
			}
			if !allowed {
				log.Infof("今天不是指定的生成日期，跳过本次触发。")
				continue
			}
		}
		for _, w := range resolveWindows(cfg, time.Now()) {
			runOnce(cfg, w.ws, w.we)
		}
	}
}

type window struct {
	ws, we time.Time
}

// resolveWindows 周期窗口解析（与原 Python 脚本口径一致）
func resolveWindows(cfg *Config, now time.Time) []window {
	loc := time.Local
	day := func(d time.Time) time.Time {
		return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc)
	}
	dayEnd := func(d time.Time) time.Time {
		return time.Date(d.Year(), d.Month(), d.Day(), 23, 59, 59, 0, loc)
	}
	parseDay := func(s string, eod bool) (time.Time, error) {
		t, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(s), loc)
		if err != nil {
			return t, fmt.Errorf("无法识别日期：%s（应为 YYYY-MM-DD）", s)
		}
		if eod {
			return dayEnd(t), nil
		}
		return day(t), nil
	}

	var s, e time.Time
	switch {
	case cfg.Period == "range":
		if cfg.Start == "" || cfg.End == "" {
			log.Errorf("-period range 需要同时指定 -start 和 -end。")
			return nil
		}
		var err error
		if s, err = parseDay(cfg.Start, false); err != nil {
			log.Errorf("%v", err)
			return nil
		}
		if e, err = parseDay(cfg.End, true); err != nil {
			log.Errorf("%v", err)
			return nil
		}
		log.Infof("range 模式：按 %s ~ %s 整段生成一份报告，不分周期。", cfg.Start, cfg.End)
	case cfg.Period == "days":
		// 滚动周期：最近 N 个完整天（昨天往前推 N 天），每天运行窗口都会滚动
		n := cfg.Days
		if n <= 0 {
			n = 7
		}
		s = day(now.AddDate(0, 0, -n))
		e = dayEnd(now.AddDate(0, 0, -1))
		log.Infof("days 模式：取最近 %d 个完整天（%s ~ %s，不含今天）。", n, s.Format("2006-01-02"), e.Format("2006-01-02"))
	case cfg.Start != "" || cfg.End != "":
		var err error
		if cfg.End != "" {
			if e, err = parseDay(cfg.End, true); err != nil {
				log.Errorf("%v", err)
				return nil
			}
		} else {
			e = now
		}
		if cfg.Start != "" {
			if s, err = parseDay(cfg.Start, false); err != nil {
				log.Errorf("%v", err)
				return nil
			}
		} else {
			s = day(e).AddDate(0, 0, -cfg.Days)
		}
	default:
		today := day(now)
		var beg time.Time
		if cfg.Period == "week" {
			// 自然周（周一 ~ 周日）
			beg = today.AddDate(0, 0, -int(today.Weekday()+6)%7)
		} else if cfg.Period == "fri" {
			// 周五 ~ 周四周：取最近一个已完整的周五 ~ 本周四
			offset := (int(today.Weekday()) - 4 + 7) % 7 // 距最近一个周四的天数
			if offset == 0 {
				offset = 7 // 今天就是周四，取上一个周四
			}
			beg = today.AddDate(0, 0, -(offset + 6))
		} else {
			// 巡检周（周六 ~ 周五）：取当前所在的巡检周「上周六 ~ 这周五」
			// 注意 Go 的 Weekday() 周日=0（Python weekday() 周一=0），距上周六的天数 = (Weekday+1)%7
			beg = today.AddDate(0, 0, -int(today.Weekday()+1)%7)
		}
		s = day(beg)
		e = dayEnd(beg.AddDate(0, 0, 6))
	}
	// 结束时间晚于当前时间时，以当前时间为准：
	// 未来时段尚无数据，保留会稀释采样率统计、且瞬时指标查询为空
	if e.After(now) {
		log.Infof("结束时间晚于当前时间，已按当前时间截断：%s", now.Format("2006-01-02 15:04:05"))
		e = now
	}
	if !s.Before(e) {
		log.Errorf("起始时间必须早于结束时间。")
		return nil
	}

	if cfg.Period == "range" || cfg.Period == "fri" || cfg.Period == "days" {
		return []window{{s, e}}
	}
	// 跨周拆分：check 按巡检周（周六起始），week 按自然周（周一起始）
	weekStart := 6 // Sunday=0, Saturday=6
	if cfg.Period == "week" {
		weekStart = 1
	}
	var segs []window
	cur := day(s)
	for !cur.After(day(e)) {
		back := (int(cur.Weekday()) - weekStart + 7) % 7
		beg := cur.AddDate(0, 0, -back)
		segS := beg
		if segS.Before(day(s)) {
			segS = day(s)
		}
		segE := dayEnd(beg.AddDate(0, 0, 6))
		if segE.After(e) {
			segE = e
		}
		if !segS.Before(segE) {
			// 单日窗口
			segs = append(segs, window{day(segS), dayEnd(segS)})
		} else {
			segs = append(segs, window{segS, segE})
		}
		cur = segE.AddDate(0, 0, 1)
		if cur.Before(day(segS).AddDate(0, 0, 1)) {
			cur = day(segS).AddDate(0, 0, 1)
		}
	}
	return segs
}

func nextRun(hhmm string) time.Time {
	h, m := 12, 0
	if parts := strings.Split(hhmm, ":"); len(parts) == 2 {
		h, _ = strconv.Atoi(parts[0])
		m, _ = strconv.Atoi(parts[1])
	}
	now := time.Now()
	next := time.Date(now.Year(), now.Month(), now.Day(), h, m, 0, 0, time.Local)
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

func maskURL(u string) string {
	if u == "" {
		return "(未配置)"
	}
	if i := strings.Index(u, "@"); i >= 0 && strings.Contains(u, "://") {
		scheme := u[:strings.Index(u, "://")+3]
		return scheme + "***@" + u[i+1:]
	}
	return u
}

func runListDS(cfg *Config) {
	cli := NewN9EClient(cfg.Base, "1", cfg.Token, cfg.User, cfg.Pass, 60*time.Second, cfg.Insecure)
	cli.login()
	dss, err := cli.ListDatasources()
	if err != nil {
		log.Errorf("获取数据源清单失败：%v", err)
		return
	}
	fmt.Printf("n9e 数据源清单（%s，共 %d 个，按数据源ID排序）：\n", cfg.Base, len(dss))
	for _, d := range dss {
		fmt.Printf("  #%-4v %-12s %s\n", d.ID, d.Type, d.Name)
	}
	fmt.Println("\n提示：用 -n9e_ds_id <数据源ID> 直接指定，或 -n9e_project <名称关键字> 自动检索。")
}

// collectWaf 拉取 WAF 数据（失败不阻断报告生成）
func collectWaf(cfg *Config, ws, we time.Time) (*WafData, string) {
	if cfg.DatabaseURL == "" {
		return nil, ""
	}
	cli, err := OpenWaf(cfg.DatabaseURL, ws, we, cfg.ExceptAppIDs, cfg.ExceptIPs)
	if err != nil {
		log.Errorf("%v", err)
		return nil, "WAF 数据库连接失败，本期报告不含防火墙安全巡检数据。"
	}
	defer cli.Close()
	waf, err := cli.Collect()
	if err != nil {
		log.Errorf("%v", err)
		if waf == nil {
			return nil, "WAF 数据查询失败，本期报告不含防火墙安全巡检数据。"
		}
		return waf, "部分 WAF 数据查询失败（" + err.Error() + "），相关章节可能不完整。"
	}
	return waf, ""
}

// demoRows / demoWaf：内置样例数据（-demo 本地验证用）
func demoRows() []HostRow {
	seed := []struct {
		ip, section, proj, role, eng, os string
		cores                        float64
		cpu, cpuP, mem, memP, disk   float64
		memGB, diskGB                float64
		io, net, netP                float64
		conn                         float64
		noConn                       bool
	}{
		{"192.168.30.105", "大数据生产区", "天地图政务版", "Web前端1", "邹源", "Ubuntu 22.04", 8, 23.1, 61.2, 46.5, 72.3, 55.1, 16, 200, 12.4, 88.3, 210.5, 432, false},
		{"192.168.30.106", "大数据生产区", "天地图政务版", "Web前端2", "邹源", "Ubuntu 22.04", 8, 21.8, 58.7, 44.2, 70.1, 51.6, 16, 200, 10.9, 82.1, 198.3, 398, false},
		{"192.168.30.110", "大数据生产区", "天地图政务版", "应用服务器1", "邹源", "Ubuntu 22.04", 16, 55.3, 92.4, 61.7, 84.2, 63.8, 32, 500, 31.2, 145.6, 402.1, 1205, false},
		{"192.168.30.111", "大数据生产区", "天地图政务版", "应用服务器2", "邹源", "Ubuntu 22.04", 16, 52.9, 88.6, 59.4, 81.5, 60.2, 32, 500, 28.7, 138.9, 376.4, 1142, false},
		{"10.40.1.2", "互联网区", "智慧民政", "数据库主", "张三", "Microsoft Windows Server 2016 Standard", 8, 34.6, 78.9, 72.8, 91.3, 86.4, 32, 1000, 45.3, 96.2, 233.8, 865, false},
		{"10.40.1.3", "互联网区", "智慧民政", "数据库备", "张三", "Microsoft Windows Server 2016 Standard", 8, 31.2, 74.3, 70.1, 89.7, 84.9, 32, 1000, 41.8, 91.5, 221.6, 802, false},
		{"10.40.1.10", "互联网区", "智慧民政", "业务服务器1", "李四", "Ubuntu 22.04", 4, 12.3, 45.8, 38.2, 55.6, 40.3, 8, 100, 6.2, 42.8, 120.3, 215, false},
		{"10.40.1.11", "互联网区", "智慧民政", "业务服务器2", "李四", "Ubuntu 22.04", 4, 11.7, 44.2, 36.9, 53.8, 39.1, 8, 100, 5.8, 40.2, 115.7, 208, false},
		{"10.40.1.20", "互联网区", "智慧民政", "缓存服务器", "李四", "Ubuntu 22.04", 8, 8.9, 22.1, 74.5, 80.2, 12.3, 16, 100, 3.1, 30.5, 88.9, 0, true},
		{"172.16.8.5", "城投集团生产区", "两江控股OA", "OA应用", "王五", "CentOS 7.9", 4, 42.5, 96.8, 66.1, 78.4, 91.7, 8, 100, 18.6, 55.4, 165.2, 655, false},
	}
	now := time.Now()
	rows := make([]HostRow, 0, len(seed))
	for _, s := range seed {
		rows = append(rows, HostRow{
			Ident: "0-0-" + s.ip + "-" + s.section + "-" + s.proj + "-" + s.role + "-" + s.eng,
			Tenant: "000000", IP: s.ip, Section: s.section, Project: s.proj, Role: s.role, Engineer: s.eng,
			Cores: s.cores, CPU: s.cpu, CPUPeak: s.cpuP, MemTotalGB: s.memGB, Mem: s.mem, MemPeak: s.memP,
			DiskCapGB: s.diskGB, Disk: s.disk, DiskPeak: s.disk, DiskIO: s.io,
			Net: s.net, NetPeak: s.netP, Conn: s.conn, NoConn: s.noConn,
			SampleRate: 1.0, OS: s.os,
		})
	}
	_ = now
	return rows
}

func demoWaf() *WafData {
	return &WafData{
		Total: WafTotal{VisitTotal: 1284563, BlockedTotal: 98214, BlacklistBlocked: 1023, Unblocked: 37, BlockRate: "99.96"},
		Apps: []WafApp{
			{ID: 1, Name: "门户官网", Domains: "www.example.com", Ports: "80, 443", Requests: 834521, Blocked: 41235},
			{ID: 2, Name: "业务系统A", Domains: "app.example.com", Ports: "443", Requests: 321004, Blocked: 42871},
			{ID: 3, Name: "业务系统B", Domains: "app2.example.com", Ports: "8080", Requests: 129038, Blocked: 14108},
		},
		Geos: []WafGeo{
			{"CN", "广东省", "深圳市", 623451}, {"CN", "北京市", "北京市", 412334},
			{"CN", "浙江省", "杭州市", 210456}, {"CN", "四川省", "成都市", 98231},
		},
		AccessIPs: []WafIPStat{
			{"10.20.30.40", "正常访问", 52341}, {"10.20.30.41", "正常访问", 41230}, {"192.168.9.9", "正常访问", 30112},
		},
		AttackIPs: []WafIPStat{
			{"45.61.136.201", "扫描器", 8231}, {"103.42.10.7", "SQL 注入", 5123}, {"185.220.101.5", "XSS", 2340},
		},
		AttackTys: []WafAttackType{
			{"扫描器", 23451}, {"SQL 注入", 12305}, {"XSS", 8214}, {"目录穿越", 5102},
			{"命令注入", 3214}, {"未授权访问", 2103},
		},
		NotBlocked: []WafNotBlocked{
			{"业务系统A", "45.61.136.201", "app.example.com", "/api/login", "443", "NL", "北荷兰省", "阿姆斯特丹", "SQL 注入", "2026-09-15 03:12:44"},
			{"门户官网", "103.42.10.7", "www.example.com", "/search?q=<script>", "80", "HK", "香港", "香港", "XSS", "2026-09-16 21:43:10"},
		},
	}
}

// runOnce 生成一个周期的合并报告
func runOnce(cfg *Config, ws, we time.Time) {
	log.Infof("==== 开始生成巡检周报（%s ~ %s）====", ws.Format("2006-01-02"), we.Format("2006-01-02"))

	// ---- 资源巡检数据（n9e）----
	var rows []HostRow
	srcDesc := ""
	if cfg.Demo {
		rows = demoRows()
		srcDesc = "演示样例数据"
	} else if cfg.Base == "" {
		log.Warnf("未配置 n9e 地址（N9E_BASE / -n9e_base），本次报告将不含资源巡检数据。")
	} else {
		ds := cfg.DS
		if cfg.DS == "1" && cfg.Project != "" && os.Getenv("N9E_DS_ID") == "" {
			// 未明确指定数据源 → 按项目名检索
			cli := NewN9EClient(cfg.Base, "1", cfg.Token, cfg.User, cfg.Pass, 60*time.Second, cfg.Insecure)
			if id, err := ResolveDSByProject(cfg.Project, cli, cfg.MaxDS); err == nil {
				ds = id
			} else {
				log.Errorf("%v", err)
			}
		}
		cli := NewN9EClient(cfg.Base, ds, cfg.Token, cfg.User, cfg.Pass, 180*time.Second, cfg.Insecure)
		res, err := ReadN9E(cli, ws.Unix(), we.Unix(), cfg.Step, cfg.Project, cfg.Layout)
		if err != nil {
			log.Errorf("n9e 采集失败：%v", err)
		} else {
			rows = res
			srcDesc = fmt.Sprintf("夜莺监控 n9e（%s，数据源 #%s）", cfg.Base, ds)
		}
	}
	if cfg.Engineer != "" {
		for i := range rows {
			rows[i].Engineer = cfg.Engineer
		}
	}

	// 低采样率提示（可能停机 / 采集中断）
	if len(rows) > 0 {
		var offline []string
		for _, r := range rows {
			if r.SampleRate < 0.6 {
				offline = append(offline, r.IP)
			}
		}
		if len(offline) > 0 {
			log.Warnf("%d 台主机采样点不足 60%%，可能存在停机或采集中断：%s", len(offline), strings.Join(offline, "、"))
		}
	}

	// ---- WAF 巡检数据（safeline 数据库）----
	var waf *WafData
	wafNote := ""
	if cfg.Demo {
		waf = demoWaf()
	} else if cfg.DatabaseURL != "" {
		waf, wafNote = collectWaf(cfg, ws, we)
	}

	if len(rows) == 0 && waf == nil {
		log.Warnf("本周期（%s ~ %s）无可用数据（未采集到主机且未配置 WAF 数据库），跳过。",
			ws.Format("2006-01-02"), we.Format("2006-01-02"))
		return
	}

	if cfg.Dump {
		dumpRows(rows)
		if cfg.Out == "" {
			return
		}
	}

	// ---- 拆分 / 生成 ----
	path, err := BuildReport(&BuildInput{
		Cfg: cfg, Rows: rows, Waf: waf, WafNote: wafNote, WS: ws, WE: we, Source: srcDesc,
	})
	if err != nil {
		log.Errorf("生成报告失败：%v", err)
		return
	}
	log.Infof("已生成：%s", path)

	// ---- WebDAV 上传 ----
	if cfg.WebdavHost != "" {
		if err := UploadReport(cfg, path); err != nil {
			log.Errorf("上传 WebDAV 失败：%v", err)
		}
	}
	log.Infof("==== 巡检周报任务结束 ====")
}

func dumpRows(rows []HostRow) {
	fmt.Printf("\n%-16s %-12s %-20s %-20s %4s %14s %14s %14s %10s %8s %10s %8s\n",
		"IP", "网络分区", "业务系统/项目", "主机角色", "核",
		"CPU当前/峰值", "内存当前/峰值", "磁盘当前/峰值", "内存GB", "IO%", "网络Mb/s", "连接数")
	for _, r := range rows {
		conn := "—"
		if !r.NoConn {
			conn = sprintf("%d", int(r.Conn))
		}
		fmt.Printf("%-16s %-12s %-20s %-20s %4d %6.1f/%6.1f %6.1f/%6.1f %6.1f/%6.1f %10.0f %8.2f %10.2f %8s\n",
			r.IP, cutStr(r.Section, 12), cutStr(r.Project, 20), cutStr(r.Role, 20), int(r.Cores),
			r.CPU, r.CPUPeak, r.Mem, r.MemPeak, r.Disk, r.DiskPeak, r.MemTotalGB, r.DiskIO, r.Net, conn)
	}
	fmt.Println("")
}

func cutStr(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

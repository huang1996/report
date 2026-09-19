package main

// config.go —— 配置加载：环境变量为基础，命令行参数可覆盖（docker compose 通过 env_file + command 注入）

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	// n9e 连接
	Base, DS, Token, User, Pass string
	DSExplicit                  bool   // N9E_DS_ID 是否被显式指定（env 或参数）
	Project                     string // 项目名关键字（ident 过滤 / 自动检索数据源）
	MaxDS                       int
	Layout                      string // auto / section-first / project-first
	Insecure                    bool
	Step                        int
	ListDS                      bool

	// 时间范围
	Days   int
	Start  string
	End    string
	Period string // check / week / range

	// 输出
	Out      string
	Title    string
	Engineer string
	Split    string // none / project / section
	ChartTop int
	Dump     bool
	Demo     bool // 内置样例数据，用于本地验证
	IncludeBiz bool // 是否包含「业务巡检」章节（默认不添加）

	// WAF（safeline）数据源
	DatabaseURL  string
	ExceptAppIDs []string
	ExceptIPs    []string

	// WebDAV 上传
	WebdavHost  string
	WebdavLogin string
	WebdavPass  string

	// 其它
	ReportDir  string
	ReportTime string // 定时任务触发时刻 HH:MM
	LogLevel   string
	Now        bool // 立即执行一次后退出

	RunWeekdays []int // 定时模式下允许生成的星期（0=周日...6=周六；为空则每天生成）

	ShowVersion bool // 打印版本信息后退出
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// parseWeekdays 解析星期列表："5" / "1,3,5"；0=周日 ... 6=周六
func parseWeekdays(v string) []int {
	var out []int
	for _, item := range splitList(v) {
		n, err := strconv.Atoi(item)
		if err != nil || n < 0 || n > 6 {
			continue
		}
		out = append(out, n)
	}
	return out
}

func intsStr(v []int) []string {
	out := make([]string, 0, len(v))
	for _, n := range v {
		out = append(out, strconv.Itoa(n))
	}
	return out
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		n := 0
		for _, c := range v {
			if c < '0' || c > '9' {
				return def
			}
			n = n*10 + int(c-'0')
		}
		return n
	}
	return def
}

func splitList(v string) []string {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	var out []string
	for _, item := range strings.Split(v, ",") {
		if s := strings.TrimSpace(item); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func parseConfig() *Config {
	c := &Config{}

	// 先取环境变量默认值
	c.Base = env("N9E_BASE", "")
	c.DS = env("N9E_DS_ID", "")
	c.Token = env("N9E_TOKEN", "")
	c.User = env("N9E_USER", "")
	c.Pass = env("N9E_PASS", "")
	c.Project = env("N9E_PROJECT", "")
	c.MaxDS = envInt("N9E_MAX_DS", 90)
	c.Title = env("REPORT_NAME", "")
	c.Engineer = env("REPORT_ENGINEER", "")
	c.DatabaseURL = env("WAF_DATABASE_URL", env("DATABASE_URL", "")) // DATABASE_URL 为旧名，兼容保留
	c.ExceptAppIDs = splitList(env("WAF_EXCEPT_APP_IDS", env("EXCEPT_APP_IDS", "")))
	c.ExceptIPs = splitList(env("WAF_EXCEPT_IPS", env("EXCEPT_IPS", "")))
	c.WebdavHost = env("WEBDAV_HOSTNAME", "")
	c.WebdavLogin = env("WEBDAV_LOGIN", "")
	c.WebdavPass = env("WEBDAV_PASSWORD", "")
	c.ReportDir = env("REPORT_DIR", "./report")
	c.ReportTime = env("REPORT_TIME", "12:00")
	c.LogLevel = env("LOG_LEVEL", "INFO")
	c.IncludeBiz = os.Getenv("REPORT_INCLUDE_BIZ") == "1" || strings.EqualFold(os.Getenv("REPORT_INCLUDE_BIZ"), "true")
	c.RunWeekdays = parseWeekdays(os.Getenv("REPORT_RUN_WEEKDAYS"))

	// 命令行参数覆盖（compose 中通过 command 配置）
	// 双通道配置项：env 用大写、参数用同名小写（含前缀），参数显式传入时以参数为准
	f := flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
	f.StringVar(&c.Base, "n9e_base", c.Base, "n9e 地址")
	f.StringVar(&c.DS, "n9e_ds_id", c.DS, "数据源ID（一个数据源≈一个项目）")
	f.StringVar(&c.Token, "n9e_token", c.Token, "个人令牌（可选）")
	f.StringVar(&c.User, "n9e_user", c.User, "登录账号（可选）")
	f.StringVar(&c.Pass, "n9e_pass", c.Pass, "登录密码（可选）")
	f.StringVar(&c.Project, "n9e_project", c.Project, "项目名关键字，如「智慧民政」；未指定 -n9e_ds_id 时自动在全部数据源中检索")
	f.IntVar(&c.MaxDS, "n9e_max_ds", c.MaxDS, "自动检索数据源时的最大编号")
	f.StringVar(&c.Layout, "layout", "auto", "ident 命名规则：auto/section-first/project-first")
	f.BoolVar(&c.Insecure, "insecure", false, "跳过 HTTPS 证书校验")
	f.IntVar(&c.Step, "step", 300, "采样步长（秒）")
	f.BoolVar(&c.ListDS, "list-ds", false, "列出 n9e 全部数据源后退出")
	f.IntVar(&c.Days, "days", 7, "采集最近 N 天（仅指定 -end 时生效）")
	f.StringVar(&c.Start, "start", "", "起始日期 YYYY-MM-DD")
	f.StringVar(&c.End, "end", "", "结束日期 YYYY-MM-DD")
	f.StringVar(&c.Period, "period", "check", "周期模式：check=巡检周(周六~周五) / week=自然周 / fri=周五~次周四 / days=最近N个完整天(默认7，配合-days) / range=按-start~-end整段")
	f.StringVar(&c.Out, "out", "", "输出 docx 路径（仅单份报告时生效）")
	f.StringVar(&c.Title, "report_name", c.Title, "自定义报告名称")
	f.StringVar(&c.Engineer, "report_engineer", c.Engineer, "运维工程师姓名（兼报告审核人与 WebDAV 上传目录名），覆盖所有主机")
	f.StringVar(&c.Split, "split", "none", "拆分维度：none/project/section")
	f.IntVar(&c.ChartTop, "chart-top", 18, "图表最多展示的主机台数，0=全部")
	f.StringVar(&c.ReportDir, "report_dir", c.ReportDir, "报告本地输出目录")
	f.StringVar(&c.ReportTime, "report_time", c.ReportTime, "定时模式的每日触发时刻 HH:MM")
	f.StringVar(&c.LogLevel, "log_level", c.LogLevel, "日志等级：DEBUG/INFO/WARN/ERROR")
	f.StringVar(&c.WebdavHost, "webdav_hostname", c.WebdavHost, "WebDAV 上传地址（不配则只存本地）")
	f.StringVar(&c.WebdavLogin, "webdav_login", c.WebdavLogin, "WebDAV 账号")
	f.StringVar(&c.WebdavPass, "webdav_password", c.WebdavPass, "WebDAV 密码")
	f.BoolVar(&c.IncludeBiz, "report_include_biz", c.IncludeBiz, "是否包含「业务巡检」章节（默认不添加）")
	var runWeekdays string
	f.StringVar(&runWeekdays, "report_run_weekdays", strings.Join(intsStr(c.RunWeekdays), ","), "定时模式下允许生成的星期（0=周日...6=周六，逗号分隔；留空则每天生成）")
	var wafURL, wafExceptApps, wafExceptIPs string
	f.StringVar(&wafURL, "waf_database_url", c.DatabaseURL, "WAF PostgreSQL 连接串（留空跳过 WAF 章节）")
	f.StringVar(&wafExceptApps, "waf_except_app_ids", strings.Join(c.ExceptAppIDs, ","), "排除的 WAF 应用 ID，逗号分隔")
	f.StringVar(&wafExceptIPs, "waf_except_ips", strings.Join(c.ExceptIPs, ","), "排除的 WAF 访问来源 IP，逗号分隔")
	f.BoolVar(&c.Dump, "dump", false, "仅打印采集到的数据，不生成报告")
	f.BoolVar(&c.Demo, "demo", false, "使用内置样例数据生成报告（本地验证用）")
	f.BoolVar(&c.Now, "now", false, "立即执行一次后退出（否则按 REPORT_TIME 每天定时执行）")
	f.BoolVar(&c.ShowVersion, "version", false, "打印版本信息后退出")
	f.Parse(os.Args[1:])

	changed := map[string]bool{}
	f.Visit(func(fl *flag.Flag) { changed[fl.Name] = true })
	if changed["waf_database_url"] {
		c.DatabaseURL = wafURL
	}
	if changed["waf_except_app_ids"] {
		c.ExceptAppIDs = splitList(wafExceptApps)
	}
	if changed["waf_except_ips"] {
		c.ExceptIPs = splitList(wafExceptIPs)
	}
	if changed["report_run_weekdays"] {
		c.RunWeekdays = parseWeekdays(runWeekdays)
	}

	if c.DS == "" {
		c.DS = "1"
	}
	// 判断数据源是否被显式指定（环境变量或命令行参数）
	c.DSExplicit = os.Getenv("N9E_DS_ID") != ""
	if changed["n9e_ds_id"] {
		c.DSExplicit = true
	}
	return c
}

// sqlIn 把 ["3","5"] 转成 SQL 的 "'3','5'"
func sqlIn(items []string) string {
	if len(items) == 0 {
		return ""
	}
	return "'" + strings.Join(items, "','") + "'"
}

// sqlInCond 生成 "and col in (...)" 片段（items 为空时返回空串）
func sqlInCond(col string, items []string) string {
	if len(items) == 0 {
		return ""
	}
	return fmt.Sprintf(" and %s not in (%s)", col, sqlIn(items))
}

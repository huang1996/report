package main

// waf.go —— 雷池（Safeline）WAF 数据采集：直连 PostgreSQL（safeline-ce 库）

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	_ "github.com/lib/pq"
)

// 攻击类型字典（与 safeline-report 一致）
var attackTypeNames = map[int64]string{
	-4: "超长数据", -3: "黑名单", -2: "白名单", -1: "正常访问",
	0: "SQL 注入", 1: "XSS", 2: "CSRF", 3: "SSRF", 4: "拒绝服务", 5: "后门",
	6: "反序列化", 7: "代码执行", 8: "代码注入", 9: "命令注入", 10: "文件上传",
	11: "文件包含", 12: "重定向", 13: "权限不当", 14: "信息泄露", 15: "未授权访问",
	16: "不安全的配置", 17: "XXE", 18: "XPath 注入", 19: "LDAP 注入", 20: "目录穿越",
	21: "扫描器", 22: "水平权限绕过", 23: "垂直权限绕过", 24: "文件修改", 25: "文件读取",
	26: "文件删除", 27: "逻辑错误", 28: "CRLF 注入", 29: "模板注入", 30: "点击劫持",
	31: "缓冲区溢出", 32: "整数溢出", 33: "格式化字符串", 34: "条件竞争", 35: "HTTP 协议违规",
	36: "HTTP 请求走私", 61: "超时", 62: "未知", 63: "威胁情报", 64: "Cookie 篡改",
}

func attackTypeName(t int64) string {
	if n, ok := attackTypeNames[t]; ok {
		return n
	}
	return "未知攻击类型"
}

type WafTotal struct {
	VisitTotal       int64
	BlockedTotal     int64
	BlacklistBlocked int64
	Unblocked        int64
	BlockRate        string // 百分数字符串，如 "99.87"
}

type WafApp struct {
	ID       int64
	Name     string
	Domains  string
	Ports    string
	Requests int64
	Blocked  int64
}

type WafGeo struct {
	Country, Province, City string
	Count                   int64
}

type WafIPStat struct {
	IP        string
	AttackType string
	Count     int64
}

type WafAttackType struct {
	Type  string
	Count int64
}

type WafNotBlocked struct {
	App, SrcIP, Host, Path, Port, Country, Province, City, AttackType, Time string
}

type WafData struct {
	Total      WafTotal
	Apps       []WafApp
	Geos       []WafGeo
	AccessIPs  []WafIPStat
	AttackIPs  []WafIPStat
	AttackTys  []WafAttackType
	NotBlocked []WafNotBlocked
}

type WafClient struct {
	db          *sql.DB
	exceptApps  []string
	exceptIPs   []string
	startTime   int64 // unix 秒
	endTime     int64
	startDay    string
	endDay      string
	jsonbServerNames bool // mgt_website.server_names 为 jsonb（新版本）；false 为 text[]（旧版本）
	jsonbPorts       bool // mgt_website.ports 为 jsonb
}

func OpenWaf(dbURL string, ws, we time.Time, exceptApps, exceptIPs []string) (*WafClient, error) {
	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		return nil, err
	}
	db.SetConnMaxLifetime(5 * time.Minute)
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("WAF 数据库连接失败: %v", err)
	}
	startDay := time.Date(ws.Year(), ws.Month(), ws.Day(), 0, 0, 0, 0, time.Local)
	endDay := time.Date(we.Year(), we.Month(), we.Day(), 23, 59, 59, 0, time.Local)
	c := &WafClient{
		db:         db,
		exceptApps: exceptApps,
		exceptIPs:  exceptIPs,
		startTime:  startDay.Unix(),
		endTime:    endDay.Unix(),
		startDay:   ws.Format("2006-01-02"),
		endDay:     we.Format("2006-01-02"),
	}
	c.jsonbServerNames = colIsJSON(db, "mgt_website", "server_names")
	c.jsonbPorts = colIsJSON(db, "mgt_website", "ports")
	return c, nil
}

// colIsJSON 探测列是否为 json/jsonb 类型（探测失败按旧版 text[] 处理）
func colIsJSON(db *sql.DB, table, col string) bool {
	var dt string
	err := db.QueryRow(`select data_type from information_schema.columns
		where table_name = $1 and column_name = $2`, table, col).Scan(&dt)
	if err != nil {
		return false
	}
	return dt == "jsonb" || dt == "json"
}

func (w *WafClient) Close() {
	if w.db != nil {
		w.db.Close()
	}
}

func (w *WafClient) query(sqlText string, scan func(rows *sql.Rows) error) error {
	rows, err := w.db.Query(sqlText)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := scan(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (w *WafClient) GetTotal() (WafTotal, error) {
	var t WafTotal
	q := fmt.Sprintf(`
select
    coalesce(sum(case when mss."type" = 'website-req' then mss.value end)::int, 0) as visit_total,
    coalesce(sum(case when mss."type" = 'website-denied' then mss.value end)::int, 0) as blocked_total,
    (select count(*) from mgt_rule_detect_log_basic mrdlb
        where mrdlb.attack_type = -3
          and mrdlb."timestamp" >= %d and mrdlb."timestamp" <= %d%s),
    (select count(*) from mgt_detect_log_basic mdlb
        where mdlb."action" = 0
          and mdlb.attack_type != -2
          and mdlb."timestamp" >= %d and mdlb."timestamp" <= %d%s)
from mgt_system_statistics mss
where mss.created_at >= '%s' and mss.created_at <= '%s'%s`,
		w.startTime, w.endTime, sqlInCond("mrdlb.site_uuid", w.exceptApps),
		w.startTime, w.endTime, sqlInCond("mdlb.site_uuid", w.exceptApps),
		w.startDay, w.endDay, sqlInCond("mss.website", w.exceptApps))
	var visit, blocked sql.NullInt64
	var bl, un int64
	err := w.db.QueryRow(q).Scan(&visit, &blocked, &bl, &un)
	if err != nil {
		return t, fmt.Errorf("WAF 总量查询失败: %v", err)
	}
	t.VisitTotal, t.BlockedTotal, t.BlacklistBlocked, t.Unblocked = visit.Int64, blocked.Int64, bl, un
	if t.Unblocked == 0 {
		t.BlockRate = "100.00"
	} else {
		t.BlockRate = fmt.Sprintf("%.2f", float64(t.BlockedTotal)/float64(t.BlockedTotal+t.Unblocked)*100)
	}
	return t, nil
}

func (w *WafClient) GetApps() ([]WafApp, error) {
	var out []WafApp
	// server_names / ports 兼容两种 Safeline 版本：新版 jsonb 数组、旧版 text[] 数组
	serverExpr := "coalesce(array_to_string(mw.server_names, ', '), '')"
	if w.jsonbServerNames {
		serverExpr = "coalesce((select string_agg(e, ', ') from jsonb_array_elements_text(mw.server_names) e), '')"
	}
	portsExpr := "coalesce(array_to_string(mw.ports, ', '), '')"
	if w.jsonbPorts {
		portsExpr = "coalesce((select string_agg(e, ', ') from jsonb_array_elements_text(mw.ports) e), '')"
	}
	q := fmt.Sprintf(`
select
    mw.id,
    coalesce(mw."comment", ''),
    %s,
    %s,
    coalesce(sum(case when mss."type" = 'website-req' then mss.value end)::int, 0),
    coalesce(sum(case when mss."type" = 'website-denied' then mss.value end)::int, 0)
from mgt_website mw
left join mgt_system_statistics mss
    on mw.id = mss.website::bigint
    and mss.created_at >= '%s' and mss.created_at <= '%s'
where 1=1%s
group by mw.id, mw."comment", mw.server_names, mw.ports
order by mw.id`,
		serverExpr, portsExpr, w.startDay, w.endDay, sqlInCond("mw.id", w.exceptApps))
	err := w.query(q, func(rows *sql.Rows) error {
		var a WafApp
		if err := rows.Scan(&a.ID, &a.Name, &a.Domains, &a.Ports, &a.Requests, &a.Blocked); err != nil {
			return err
		}
		out = append(out, a)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("WAF 分应用查询失败: %v", err)
	}
	return out, nil
}

func (w *WafClient) GetGeos() ([]WafGeo, error) {
	var out []WafGeo
	q := fmt.Sprintf(`
select country, province, city, sum(count) as visit_count
from statistics_geos
where "time" >= %d and "time" <= %d
group by country, province, city
order by visit_count desc, country, province, city`, w.startTime, w.endTime)
	err := w.query(q, func(rows *sql.Rows) error {
		var g WafGeo
		if err := rows.Scan(&g.Country, &g.Province, &g.City, &g.Count); err != nil {
			return err
		}
		out = append(out, g)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("WAF 地域统计查询失败: %v", err)
	}
	return out, nil
}

func (w *WafClient) getIPStats(attackCond string, limit int) ([]WafIPStat, error) {
	var out []WafIPStat
	lim := ""
	if limit > 0 {
		lim = fmt.Sprintf(" limit %d", limit)
	}
	q := fmt.Sprintf(`
select si."key", si.attack_type, sum(si.count) as cnt
from statistics_ips si
where si."time" >= %d and si."time" <= %d and %s%s
group by si."key", si.attack_type
order by cnt desc, si."key"%s`, w.startTime, w.endTime, attackCond, sqlInCond("si.key", w.exceptIPs), lim)
	err := w.query(q, func(rows *sql.Rows) error {
		var s WafIPStat
		var at int64
		if err := rows.Scan(&s.IP, &at, &s.Count); err != nil {
			return err
		}
		s.AttackType = attackTypeName(at)
		out = append(out, s)
		return nil
	})
	return out, err
}

func (w *WafClient) GetAccessIPs() ([]WafIPStat, error) {
	out, err := w.getIPStats("si.attack_type = -1", 10)
	if err != nil {
		return nil, fmt.Errorf("WAF 访问IP统计查询失败: %v", err)
	}
	return out, nil
}

func (w *WafClient) GetAttackIPs() ([]WafIPStat, error) {
	out, err := w.getIPStats("si.attack_type > 0", 10)
	if err != nil {
		return nil, fmt.Errorf("WAF 攻击IP统计查询失败: %v", err)
	}
	return out, nil
}

func (w *WafClient) GetAttackTypes() ([]WafAttackType, error) {
	var out []WafAttackType
	q := fmt.Sprintf(`
select si.attack_type, sum(si.count)::int as cnt
from statistics_ips si
where si."time" >= %d and si."time" <= %d and si.attack_type > 0%s
group by si.attack_type
order by cnt desc`, w.startTime, w.endTime, sqlInCond("si.key", w.exceptIPs))
	err := w.query(q, func(rows *sql.Rows) error {
		var at, cnt int64
		if err := rows.Scan(&at, &cnt); err != nil {
			return err
		}
		out = append(out, WafAttackType{Type: attackTypeName(at), Count: cnt})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("WAF 攻击类型统计查询失败: %v", err)
	}
	return out, nil
}

// fmtWafTime 尽力把时间字段格式化为可读时间（兼容秒/毫秒时间戳与 timestamp 文本）
func fmtWafTime(v interface{}) string {
	switch t := v.(type) {
	case time.Time:
		return t.Format("2006-01-02 15:04:05")
	case int64:
		return tsToTime(t)
	case float64:
		return tsToTime(int64(t))
	case []byte:
		return fmtWafTime(string(t))
	case string:
		s := strings.TrimSpace(t)
		if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 1e9 {
			return tsToTime(n)
		}
		return s
	case nil:
		return "—"
	}
	return fmt.Sprintf("%v", v)
}

func tsToTime(n int64) string {
	if n > 1e13 { // 毫秒
		return time.UnixMilli(n).Format("2006-01-02 15:04:05")
	}
	return time.Unix(n, 0).Format("2006-01-02 15:04:05")
}

func (w *WafClient) GetNotBlocked() ([]WafNotBlocked, error) {
	var out []WafNotBlocked
	q := fmt.Sprintf(`
select
    coalesce(mw."comment", ''),
    mdlb.src_ip,
    mdlb.host,
    mdlb.url_path,
    mdlb.dst_port,
    mdlb.country,
    mdlb.province,
    mdlb.city,
    mdlb.attack_type,
    mdlb.updated_at
from mgt_detect_log_basic mdlb, mgt_website mw
where mdlb.site_uuid::int = mw.id::int
  and mdlb."timestamp" >= %d and mdlb."timestamp" <= %d
  and mdlb."action" = 0
  and mdlb.attack_type != -2%s`,
		w.startTime, w.endTime, sqlInCond("mdlb.site_uuid", w.exceptApps))
	err := w.query(q, func(rows *sql.Rows) error {
		var r WafNotBlocked
		var at int64
		var port, updatedAt interface{}
		var srcIP, host, path, country, province, city sql.NullString
		if err := rows.Scan(&r.App, &srcIP, &host, &path, &port, &country, &province, &city, &at, &updatedAt); err != nil {
			return err
		}
		r.SrcIP = nz(srcIP)
		r.Host = nz(host)
		r.Path = nz(path)
		r.Port = fmtWafTime(port)
		if r.Port == "—" {
			r.Port = ""
		} else if _, err := strconv.Atoi(strings.TrimSpace(r.Port)); err != nil {
			r.Port = fmt.Sprintf("%v", port)
		}
		r.Country = nz(country)
		r.Province = nz(province)
		r.City = nz(city)
		r.AttackType = attackTypeName(at)
		r.Time = fmtWafTime(updatedAt)
		out = append(out, r)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("WAF 未拦截攻击明细查询失败: %v", err)
	}
	return out, nil
}

func nz(s sql.NullString) string {
	if !s.Valid || strings.TrimSpace(s.String) == "" {
		return "—"
	}
	return s.String
}

// Collect 拉取全部 WAF 数据；单项失败不阻断整体（记录错误并继续）
func (w *WafClient) Collect() (*WafData, error) {
	d := &WafData{}
	var firstErr error
	setErr := func(e error) {
		if e != nil && firstErr == nil {
			firstErr = e
		}
	}
	if t, err := w.GetTotal(); err != nil {
		setErr(err)
	} else {
		d.Total = t
	}
	if a, err := w.GetApps(); err != nil {
		setErr(err)
	} else {
		d.Apps = a
	}
	if g, err := w.GetGeos(); err != nil {
		setErr(err)
	} else {
		d.Geos = g
	}
	if a, err := w.GetAccessIPs(); err != nil {
		setErr(err)
	} else {
		d.AccessIPs = a
	}
	if a, err := w.GetAttackIPs(); err != nil {
		setErr(err)
	} else {
		d.AttackIPs = a
	}
	if a, err := w.GetAttackTypes(); err != nil {
		setErr(err)
	} else {
		d.AttackTys = a
	}
	if n, err := w.GetNotBlocked(); err != nil {
		setErr(err)
	} else {
		d.NotBlocked = n
	}
	return d, firstErr
}

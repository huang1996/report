package main

// n9e.go —— 夜莺监控数据源客户端（/api/n9e/proxy/<ds>/api/v1 Prometheus 兼容接口）

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// 磁盘分区 / 网卡 / 磁盘设备过滤（与原 Python 脚本一致）
const (
	diskFilter = `fstype!="efivarfs",path!~"/run/docker.*|/var/lib/docker.*|/sys/.*"`
	ifFilter   = `interface!~"veth.*|br-.*|docker.*|vni.*|cni.*|flannel.*|lo|virbr.*|tun.*|tap.*|vnet.*|virb.*"`
	ioFilter   = `name!~"loop.*|sr[0-9]+|ram.*"`
)

var n9eQueries = map[string]string{
	"cores":    "system_n_cpus",
	"cpu":      "avg by (ident) (cpu_usage_active)",
	"mem_pct":  "mem_used_percent",
	"mem_total": "mem_total",
	"disk_pct":  "disk_used_percent{" + diskFilter + "}",
	"disk_total": "disk_total{" + diskFilter + "}",
	"diskio":    "diskio_io_util{" + ioFilter + "}",
	"net":       "sum by (ident) (rate(net_bits_recv{" + ifFilter + "}[5m]))/1e6 + sum by (ident) (rate(net_bits_sent{" + ifFilter + "}[5m]))/1e6",
	"conn":      "netstat_tcp_inuse",
	"conn_alt":  "sockstat_tcp_inuse",
}

type N9EClient struct {
	base     string
	ds       string
	token    string
	user     string
	password string
	timeout  time.Duration
	http     *http.Client
	hdr      map[string]string
}

func NewN9EClient(base, ds, token, user, password string, timeout time.Duration, insecure bool) *N9EClient {
	tr := &http.Transport{}
	if insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return &N9EClient{
		base:     strings.TrimRight(base, "/"),
		ds:       ds,
		token:    token,
		user:     user,
		password: password,
		timeout:  timeout,
		http:     &http.Client{Timeout: timeout, Transport: tr},
		hdr:      map[string]string{},
	}
}

func (c *N9EClient) login() {
	if c.token != "" {
		c.hdr["Authorization"] = "Bearer " + c.token
		return
	}
	if c.user == "" || c.password == "" {
		return
	}
	body, _ := json.Marshal(map[string]string{"username": c.user, "password": c.password})
	resp, err := c.http.Post(c.base+"/api/n9e/auth/login", "application/json", strings.NewReader(string(body)))
	if err != nil {
		log.Warnf("n9e 登录失败（%v），将以匿名方式访问数据源代理接口。", err)
		return
	}
	defer resp.Body.Close()
	var j struct {
		Dat struct {
			AccessToken string `json:"access_token"`
			Token       string `json:"token"`
		} `json:"dat"`
	}
	raw, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(raw, &j); err == nil {
		if j.Dat.AccessToken != "" {
			c.hdr["Authorization"] = "Bearer " + j.Dat.AccessToken
		} else if j.Dat.Token != "" {
			c.hdr["Authorization"] = "Bearer " + j.Dat.Token
		}
	}
}

type DataSource struct {
	ID    interface{}
	Name  string
	Type  string
}

func (c *N9EClient) ListDatasources() ([]DataSource, error) {
	var j struct {
		Dat []struct {
			ID         interface{} `json:"id"`
			Name       string      `json:"name"`
			PluginType string      `json:"plugin_type"`
		} `json:"dat"`
	}
	if err := c.getJSON(c.base+"/api/n9e/datasource/brief", &j); err != nil {
		return nil, err
	}
	out := make([]DataSource, 0, len(j.Dat))
	for _, d := range j.Dat {
		out = append(out, DataSource{ID: d.ID, Name: strings.TrimSpace(d.Name), Type: strings.TrimSpace(d.PluginType)})
	}
	sort.Slice(out, func(i, k int) bool { return dsID(out[i].ID) < dsID(out[k].ID) })
	return out, nil
}

func dsID(v interface{}) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case string:
		n, _ := strconv.Atoi(t)
		return n
	}
	return 0
}

func (c *N9EClient) getJSON(u string, out interface{}) error {
	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		return err
	}
	for k, v := range c.hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(raw))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *N9EClient) api(path string, ds string) string {
	return fmt.Sprintf("%s/api/n9e/proxy/%s/api/v1%s", c.base, ds, path)
}

func (c *N9EClient) Idents(ds string) ([]string, error) {
	var j struct {
		Data []string `json:"data"`
	}
	if err := c.getJSON(c.api("/label/ident/values", ds), &j); err != nil {
		return nil, err
	}
	return j.Data, nil
}

type promSeries struct {
	Metric map[string]string `json:"metric"`
	Values [][]interface{}   `json:"values"`
}

func (c *N9EClient) QueryInstant(expr string, ts int64) ([]promSeries, error) {
	u := fmt.Sprintf("%s/query?query=%s&time=%d", c.api("", c.ds), url.QueryEscape(expr), ts)
	var j struct {
		Status string `json:"status"`
		Data   struct {
			Result []promSeries `json:"result"`
		} `json:"data"`
	}
	if err := c.getJSON(u, &j); err != nil {
		return nil, fmt.Errorf("n9e 查询失败: %s -> %v", expr, err)
	}
	if j.Status != "success" {
		return nil, fmt.Errorf("n9e 查询失败：%s", expr)
	}
	return j.Data.Result, nil
}

// osDisplay 由 system_info 标签组装可读的操作系统名称
func osDisplay(m map[string]string) string {
	name := strings.TrimSpace(m["os_name"])
	ver := strings.TrimSpace(m["os_version"])
	if name == "" {
		return ""
	}
	lower := strings.ToLower(name)
	if strings.Contains(lower, "windows") || ver == "" || strings.Contains(ver, "build") {
		return name
	}
	return name + " " + ver
}

// fetchOS 拉取各主机操作系统（system_info 指标标签；缺失时返回空映射，不阻断报告）
func (c *N9EClient) fetchOS(ts int64) map[string]string {
	out := map[string]string{}
	// 瞬时查询只回溯时间点前 5 分钟内的样本：巡检窗口结束时间在未来时
	// （如生成当前所在巡检周报告）按结束时间查必然为空，改用当前时间查询
	if now := time.Now().Unix(); ts > now {
		ts = now
	}
	res, err := c.QueryInstant("system_info", ts)
	if err != nil {
		log.Debugf("system_info 查询失败（%v），表 3 操作系统列将留空。", err)
		return out
	}
	for _, s := range res {
		if id := s.Metric["ident"]; id != "" {
			if v := osDisplay(s.Metric); v != "" {
				out[id] = v
			}
		}
	}
	return out
}

func (c *N9EClient) QueryRange(expr string, start, end int64, step int) ([]promSeries, error) {
	u := fmt.Sprintf("%s/query_range?query=%s&start=%d&end=%d&step=%d",
		c.api("", c.ds), url.QueryEscape(expr), start, end, step)
	var j struct {
		Status string `json:"status"`
		Data   struct {
			Result []promSeries `json:"result"`
		} `json:"data"`
	}
	if err := c.getJSON(u, &j); err != nil {
		return nil, fmt.Errorf("n9e 查询失败: %s -> %v", expr, err)
	}
	if j.Status != "success" {
		return nil, fmt.Errorf("n9e 查询失败：%s", expr)
	}
	return j.Data.Result, nil
}

// ---- ident 解析 ----
// 形如：000093-192.168.30.105-大数据生产区-天地图政务版-邹源
//      000095-10.40.1.2-智慧民政-业务服务器8
var (
	ipRe   = regexp.MustCompile(`^\d{1,3}(?:\.\d{1,3}){3}$`)
	nameRe = regexp.MustCompile(`^[\p{Han}·]{2,4}$`)
)

type IdentInfo struct {
	Tenant, IP, Section, Project, Role, Engineer string
}

func parseIdent(ident, layout string) IdentInfo {
	out := IdentInfo{IP: strings.TrimSpace(ident), Section: "—", Project: "—", Role: "—", Engineer: "—"}
	var parts []string
	for _, p := range strings.Split(ident, "-") {
		if p = strings.TrimSpace(p); p != "" {
			parts = append(parts, p)
		}
	}
	idx := -1
	for i, p := range parts {
		if ipRe.MatchString(p) {
			idx = i
			break
		}
	}
	if idx < 0 {
		out.Project = strings.TrimSpace(ident)
		if out.Project == "" {
			out.Project = "—"
		}
		return out
	}
	out.Tenant = strings.Join(parts[:idx], "-")
	out.IP = parts[idx]
	rest := parts[idx+1:]
	if len(rest) == 0 {
		return out
	}
	// 末尾若是纯中文姓名（2~4 字），视为运维工程师
	if len(rest) >= 2 && nameRe.MatchString(rest[len(rest)-1]) {
		out.Engineer = rest[len(rest)-1]
		rest = rest[:len(rest)-1]
	}
	if len(rest) == 0 {
		return out
	}
	lay := layout
	if lay == "auto" {
		if strings.HasSuffix(rest[0], "区") {
			lay = "section-first"
		} else {
			lay = "project-first"
		}
	}
	if lay == "section-first" {
		out.Section = rest[0]
		rest = rest[1:]
	}
	if len(rest) > 0 {
		out.Project = rest[0]
		rest = rest[1:]
	}
	if len(rest) > 0 {
		out.Role = strings.Join(rest, "-")
	}
	return out
}

// ---- 主机数据行 ----
type HostRow struct {
	Ident      string
	Tenant     string
	IP         string
	Section    string
	Project    string
	Role       string
	Engineer   string
	Cores      float64
	CPU        float64
	CPUPeak    float64
	MemTotalGB float64
	Mem        float64
	MemPeak    float64
	DiskCapGB  float64
	Disk       float64
	DiskPeak   float64
	DiskIO     float64
	Conn       float64
	NoConn     bool
	Net        float64
	NetPeak    float64
	SampleRate float64
	OS         string
	// 派生
	Status string
	CPUGap float64
	MemGap float64
}

func fnum(v interface{}) float64 {
	switch t := v.(type) {
	case string:
		f, _ := strconv.ParseFloat(t, 64)
		return f
	case float64:
		return t
	}
	return 0
}

func vals(s promSeries) []float64 {
	out := make([]float64, 0, len(s.Values))
	for _, p := range s.Values {
		if len(p) >= 2 {
			out = append(out, fnum(p[1]))
		}
	}
	return out
}

func lastVal(s promSeries) (float64, bool) {
	if len(s.Values) == 0 {
		return 0, false
	}
	return fnum(s.Values[len(s.Values)-1][1]), true
}

func avg(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := 0.0
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

func ipKey(ip string) (int, string) {
	parts := strings.Split(strings.TrimSpace(ip), ".")
	if len(parts) != 4 {
		return 999, ip
	}
	key := 0
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return 999, ip
		}
		key = key*256 + n
	}
	return key, ""
}

func sortHosts(rows []HostRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		ki, _ := ipKey(rows[i].IP)
		kj, _ := ipKey(rows[j].IP)
		if rows[i].Section != rows[j].Section {
			return rows[i].Section < rows[j].Section
		}
		return ki < kj
	})
}

// ReadN9E 从 n9e 采集 [start,end] 窗口内各主机指标；identFilter 非空时按 ident 关键字过滤
func ReadN9E(c *N9EClient, start, end int64, step int, identFilter, layout string) ([]HostRow, error) {
	c.login()
	log.Infof("正在从 n9e 拉取数据：%s（数据源 #%s，%s ~ %s，步长 %ds）",
		c.base, c.ds,
		time.Unix(start, 0).Format("2006-01-02 15:04"),
		time.Unix(end, 0).Format("2006-01-02 15:04"), step)

	data := map[string][]promSeries{}
	for _, key := range []string{"cores", "cpu", "mem_pct", "mem_total", "disk_pct", "disk_total", "diskio", "net", "conn"} {
		res, err := c.QueryRange(n9eQueries[key], start, end, step)
		if err != nil {
			return nil, err
		}
		data[key] = res
	}
	if len(data["conn"]) == 0 {
		res, err := c.QueryRange(n9eQueries["conn_alt"], start, end, step)
		if err == nil {
			data["conn"] = res
		}
	}
	if len(data["cores"]) == 0 {
		return nil, fmt.Errorf("n9e 未返回任何主机数据（表达式：%s），请检查数据源ID与时间范围。", n9eQueries["cores"])
	}

	osMap := c.fetchOS(end)

	keep := func(m map[string]string) bool {
		id := m["ident"]
		return identFilter == "" || strings.Contains(id, identFilter)
	}

	cores := map[string]float64{}
	cpu := map[string][]float64{}
	memP := map[string][]float64{}
	memT := map[string]float64{}
	net := map[string][]float64{}
	conn := map[string][]float64{}
	io := map[string][][][2]float64{}   // ident -> series -> [(ts, val)]
	disk := map[string]map[string][][2]float64{} // ident -> path -> [(ts, val)]
	dtot := map[[2]string]float64{}    // (ident, path) -> total bytes

	for _, s := range data["cores"] {
		if id := s.Metric["ident"]; id != "" && keep(s.Metric) {
			if v, ok := lastVal(s); ok {
				cores[id] = v
			}
		}
	}
	for _, s := range data["cpu"] {
		if keep(s.Metric) {
			cpu[s.Metric["ident"]] = vals(s)
		}
	}
	for _, s := range data["mem_pct"] {
		if keep(s.Metric) {
			memP[s.Metric["ident"]] = vals(s)
		}
	}
	for _, s := range data["mem_total"] {
		if keep(s.Metric) {
			if v, ok := lastVal(s); ok {
				memT[s.Metric["ident"]] = v
			}
		}
	}
	for _, s := range data["net"] {
		if keep(s.Metric) {
			net[s.Metric["ident"]] = vals(s)
		}
	}
	for _, s := range data["diskio"] {
		if !keep(s.Metric) {
			continue
		}
		id := s.Metric["ident"]
		vs := vals(s)
		pairs := make([][2]float64, 0, len(s.Values))
		for i, p := range s.Values {
			if i < len(vs) {
				pairs = append(pairs, [2]float64{fnum(p[0]), vs[i]})
			}
		}
		io[id] = append(io[id], pairs)
	}
	for _, s := range data["conn"] {
		if keep(s.Metric) {
			conn[s.Metric["ident"]] = vals(s)
		}
	}
	for _, s := range data["disk_pct"] {
		if !keep(s.Metric) {
			continue
		}
		id := s.Metric["ident"]
		path := s.Metric["path"]
		if path == "" {
			path = s.Metric["device"]
		}
		if path == "" {
			path = "?"
		}
		if disk[id] == nil {
			disk[id] = map[string][][2]float64{}
		}
		vs := vals(s)
		for i, p := range s.Values {
			if i < len(vs) {
				disk[id][path] = append(disk[id][path], [2]float64{fnum(p[0]), vs[i]})
			}
		}
	}
	for _, s := range data["disk_total"] {
		if !keep(s.Metric) {
			continue
		}
		id := s.Metric["ident"]
		path := s.Metric["path"]
		if path == "" {
			path = s.Metric["device"]
		}
		key := [2]string{id, path}
		if v, ok := lastVal(s); ok && v > dtot[key] {
			dtot[key] = v
		}
	}

	if identFilter != "" && len(cores) == 0 {
		var appear []string
		seen := map[string]bool{}
		for _, s := range data["cores"] {
			if id := s.Metric["ident"]; id != "" && !seen[id] {
				seen[id] = true
				appear = append(appear, id)
			}
		}
		return nil, fmt.Errorf("数据源 #%s 中未找到含「%s」的主机。该数据源现有主机：\n  %s",
			c.ds, identFilter, strings.Join(appear, "\n  "))
	}

	GB := float64(1 << 30)
	idents := make([]string, 0, len(cores))
	for id := range cores {
		idents = append(idents, id)
	}
	sort.Slice(idents, func(i, j int) bool {
		ki, _ := ipKey(idents[i])
		kj, _ := ipKey(idents[j])
		return ki < kj
	})

	rows := []HostRow{}
	for _, ident := range idents {
		info := parseIdent(ident, layout)
		cpuV := cpu[ident]
		memV := memP[ident]
		netV := net[ident]

		// 磁盘：逐时刻选出使用率最高的分区；容量取该分区总容量
		var diskVals []float64
		var diskCap float64
		var bestTs float64
		var bestPath string
		best := map[float64][2]interface{}{} // ts -> (pct, path)
		for path, pairs := range disk[ident] {
			for _, pv := range pairs {
				ts, v := pv[0], pv[1]
				if cur, ok := best[ts]; !ok || v > cur[0].(float64) {
					best[ts] = [2]interface{}{v, path}
				}
			}
		}
		tsList := make([]float64, 0, len(best))
		for ts := range best {
			tsList = append(tsList, ts)
		}
		sort.Slice(tsList, func(i, j int) bool { return tsList[i] < tsList[j] })
		for _, ts := range tsList {
			cur := best[ts]
			diskVals = append(diskVals, cur[0].(float64))
			if cur[0].(float64) > bestTs || bestPath == "" {
				bestTs = cur[0].(float64)
				bestPath = cur[1].(string)
			}
		}
		if bestPath != "" {
			diskCap = dtot[[2]string{ident, bestPath}] / GB
		}

		// 多块磁盘：每个时刻取利用率最高的那块
		perTs := map[float64]float64{}
		for _, series := range io[ident] {
			for _, pv := range series {
				if pv[1] > perTs[pv[0]] {
					perTs[pv[0]] = pv[1]
				}
			}
		}
		ioVals := make([]float64, 0, len(perTs))
		ioTs := make([]float64, 0, len(perTs))
		for ts := range perTs {
			ioTs = append(ioTs, ts)
		}
		sort.Slice(ioTs, func(i, j int) bool { return ioTs[i] < ioTs[j] })
		for _, ts := range ioTs {
			ioVals = append(ioVals, perTs[ts])
		}

		rec := HostRow{
			Ident: ident, Tenant: info.Tenant, IP: info.IP, Section: info.Section,
			Project: info.Project, Role: info.Role, Engineer: info.Engineer,
			Cores:      cores[ident],
			CPU:        avg(cpuV),
			CPUPeak:    sliceMax(cpuV),
			MemTotalGB: memT[ident] / GB,
			Mem:        avg(memV),
			MemPeak:    sliceMax(memV),
			DiskCapGB:  diskCap,
			Disk:       avg(diskVals),
			DiskPeak:   sliceMax(diskVals),
			DiskIO:     avg(ioVals),
			Net:        avg(netV),
			NetPeak:    sliceMax(netV),
			SampleRate: 1.0,
			OS:         osMap[ident],
		}
		_, hasConn := conn[ident]
		rec.NoConn = !hasConn
		if hasConn {
			rec.Conn = avg(conn[ident])
		}
		expect := (end - start) / int64(step)
		if expect < 1 {
			expect = 1
		}
		rec.SampleRate = float64(len(cpuV)) / float64(expect)
		rows = append(rows, rec)
	}
	log.Infof("已获取 %d 台主机数据。", len(rows))
	return rows, nil
}

func sliceMax(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	m := v[0]
	for _, x := range v {
		if x > m {
			m = x
		}
	}
	return m
}

// ResolveDSByProject 按项目名在全部数据源中定位，返回命中的第一个数据源ID
func ResolveDSByProject(project string, c *N9EClient, maxDS int) (string, error) {
	log.Infof("未指定数据源，正在按项目名「%s」检索 n9e 各数据源……", project)
	for i := 1; i <= maxDS; i++ {
		ids, err := c.Idents(strconv.Itoa(i))
		if err != nil || len(ids) == 0 {
			continue
		}
		var m []string
		for _, x := range ids {
			if strings.Contains(x, project) {
				m = append(m, x)
			}
		}
		if len(m) > 0 {
			log.Infof("  · 数据源 #%d 命中 %d 台主机", i, len(m))
			return strconv.Itoa(i), nil
		}
	}
	return "", fmt.Errorf("在 n9e 的 %d 个数据源中均未找到包含「%s」的主机", maxDS, project)
}

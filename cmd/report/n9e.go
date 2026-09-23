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

// remoteFSRe 网络 / 共享文件系统前缀。这类挂载点不是主机自带存储，而且同一卷常被多台
// 主机同时挂载，计入磁盘容量会重复计数并严重放大总量——实测有主机挂载了 100 TB 的
// NFS 数据卷（对象网关后端），单台就能把全量容量统计抬高一个量级。
var remoteFSRe = regexp.MustCompile(`^(?:nfs4?|cifs|smb|smbfs|fuse[.]|ceph|glusterfs|9p|afs|sshfs|davfs)`)

// isRemoteFS 判断挂载点是否为网络/共享文件系统。
func isRemoteFS(fstype string) bool {
	return remoteFSRe.MatchString(strings.ToLower(strings.TrimSpace(fstype)))
}

// pseudoFSRe 伪文件系统：内存文件系统与内核接口目录，不对应真实磁盘分区。
var pseudoFSRe = regexp.MustCompile(`^(?:tmpfs|devtmpfs|devpts|proc|procfs|sysfs|cgroup|cgroup2|overlay|overlayfs|squashfs|ramfs|rootfs|autofs|mqueue|debugfs|tracefs|securityfs|pstore|bpf|configfs|hugetlbfs|fusectl|nsfs|efivarfs|binfmt_misc|rpc_pipefs|selinuxfs|none|swap)$`)

// pseudoPathPrefixes 伪挂载点前缀。这些目录由内核或容器运行时动态生成，
// 不是需要关注的磁盘分区——例如 Docker 会在 /run/docker/runtime-runc/moby/<id>/ 下
// 生成大量 runc.xxxxxx 挂载点，实测单个数据源可达上百条，全部属于噪音。
var pseudoPathPrefixes = []string{"/run/", "/dev/", "/sys/", "/proc/", "/snap/", "/var/lib/docker/"}

// isRealMount 判断某挂载点是否为「真实分区」，用于「磁盘分区使用明细」表。
//
// 过滤两类噪音：
//  1. 伪文件系统（tmpfs / devtmpfs / overlay / proc / sysfs / efivarfs …）；
//  2. 内核与容器运行时的虚拟目录（/run、/dev、/sys、/proc、/snap、/var/lib/docker）。
//
// Windows 盘符（如 `\C:`、`D:`）不以 `/` 开头，一律保留。
// 注意 `/tmp`、`/boot` 等真实分区**不在**排除范围内。
func isRealMount(path, fstype string) bool {
	p := strings.TrimSpace(path)
	if p == "" || p == "?" {
		return false
	}
	if pseudoFSRe.MatchString(strings.ToLower(strings.TrimSpace(fstype))) {
		return false
	}
	if !strings.HasPrefix(p, "/") {
		return true // Windows 盘符
	}
	low := strings.ToLower(p)
	for _, pre := range pseudoPathPrefixes {
		if low == strings.TrimSuffix(pre, "/") || strings.HasPrefix(low, pre) {
			return false
		}
	}
	return true
}

var n9eQueries = map[string]string{
	"cores":      "system_n_cpus",
	"cpu":        "avg by (ident) (cpu_usage_active)",
	"mem_pct":    "mem_used_percent",
	"mem_total":  "mem_total",
	"disk_pct":   "disk_used_percent{" + diskFilter + "}",
	"disk_total": "disk_total{" + diskFilter + "}",
	"diskio":     "diskio_io_util{" + ioFilter + "}",
	"net":        "sum by (ident) (rate(net_bits_recv{" + ifFilter + "}[5m]))/1e6 + sum by (ident) (rate(net_bits_sent{" + ifFilter + "}[5m]))/1e6",
	"conn":       "netstat_tcp_inuse",
	"conn_alt":   "sockstat_tcp_inuse",
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
	ID   interface{}
	Name string
	Type string
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

// kylinVerRe 从内核版本号中提取麒麟大版本标记（银河麒麟基于 openEuler，
// 内核版本串形如 4.19.90-52.22.v2207.ky10.x86_64，ky10 即 V10）
var kylinVerRe = regexp.MustCompile(`ky(?:lin)?v?(\d+)`)

// osDisplay 由 system_info 标签组装可读的操作系统名称。
// 麒麟系统的 categraf 不上报 os_version 标签，此时从 kernel_version 的 ky<N> 标记推导版本。
func osDisplay(m map[string]string) string {
	name := strings.TrimSpace(m["os_name"])
	ver := strings.TrimSpace(m["os_version"])
	if name == "" {
		return ""
	}
	if strings.EqualFold(strings.TrimSpace(name), "kylin") {
		name = "Kylin"
	}
	lower := strings.ToLower(name)
	if strings.Contains(lower, "windows") || ver == "" || strings.Contains(ver, "build") {
		if ver == "" {
			if mm := kylinVerRe.FindStringSubmatch(strings.ToLower(m["kernel_version"])); mm != nil {
				return name + " V" + mm[1]
			}
		}
		return name
	}
	// 版本号统一首字母大写：categraf 在不同版本里上报成 v10 / V10 两种写法，
	// 直接拼接会让同一批麒麟主机出现「Kylin v10」与「Kylin V10」两个值，影响分组统计
	if ver[0] >= 'a' && ver[0] <= 'z' {
		ver = strings.ToUpper(ver[:1]) + ver[1:]
	}
	return name + " " + ver
}

// archMarkers 内核版本串中出现的 CPU 架构标记 → 架构名。
// 架构名沿用 Go 的 GOARCH 命名习惯（amd64 / arm64 / 386 …），便于与构建产物对照。
// 顺序有意义：先匹配更具体的标记（ppc64le 先于 ppc64、mips64el 先于 mips64、aarch64 先于 arm64 的近似写法）。
var archMarkers = []struct{ marker, arch string }{
	{"aarch64", "arm64"},
	{"arm64", "arm64"},
	{"armv8", "arm"},
	{"armv7", "arm"},
	{"armv6", "arm"},
	{"x86_64", "amd64"},
	{"amd64", "amd64"},
	{"i686", "386"},
	{"i586", "386"},
	{"i486", "386"},
	{"i386", "386"},
	{"ppc64le", "ppc64le"},
	{"ppc64", "ppc64"},
	{"s390x", "s390x"},
	{"mips64el", "mips64le"},
	{"mips64", "mips64"},
	{"loongarch64", "loong64"},
	{"riscv64", "riscv64"},
	{"sw_64", "sw64"},
	{"sparc64", "sparc64"},
}

// archOf 从 system_info 的 kernel_version 标签解析 CPU 架构。
//
// 这是 n9e 侧唯一能拿到架构的途径——categraf 未上报任何 arch/os_arch/machine 标签
// （已枚举全部数据源的指标名与标签名确认）。Linux 内核版本串末尾一般带架构标记，
// 如 CentOS 的 3.10.0-1160.el7.x86_64、麒麟的 4.19.90-52.22.v2207.ky10.x86_64、
// openEuler 的 6.6.0-159.4.3.154.oe2403sp4.aarch64；
// 而 Ubuntu 的 6.8.0-60-generic 与 Windows 的 10.0.14393 Build 14393 不含架构信息，
// 这类主机返回空串（报告中显示「—」），需在主机侧补充采集才能获得。
func archOf(kernel string) string {
	k := strings.ToLower(strings.TrimSpace(kernel))
	if k == "" {
		return ""
	}
	for _, m := range archMarkers {
		if strings.Contains(k, m.marker) {
			return m.arch
		}
	}
	return ""
}

// hostMeta 主机元数据（全部取自 n9e 指标标签，缺失时为空映射，不阻断报告）。
type hostMeta struct {
	OS    map[string]string // ident -> 可读操作系统名（system_info）
	IP    map[string]string // ident -> 主机真实 IP（system_info 的 host_ip）
	Arch  map[string]string // ident -> CPU 架构（system_info 的 kernel_version 解析）
	Agent map[string]string // ident -> categraf agent 版本（categraf_info 的 version 标签）
	// Live 为当前在报 ident 的集合——system_info 是主机当前状态快照而非累计计数器，
	// 可作为「运维改名后仍活跃的 ident」的权威依据，用于剔除留存期内的历史残留 ident。
	Live map[string]bool
}

// fetchHostMeta 拉取各主机元数据：操作系统、真实 IP、CPU 架构、categraf 版本。
// ip 用于补齐 ident 里不含 IP 的数据源（如「租户-项目-角色」形态的 ident，
// 真实 IP 只存在于 system_info 的 host_ip 标签）。
func (c *N9EClient) fetchHostMeta(ts int64) hostMeta {
	meta := hostMeta{
		OS:    map[string]string{},
		IP:    map[string]string{},
		Arch:  map[string]string{},
		Agent: map[string]string{},
		Live:  map[string]bool{},
	}
	// 瞬时查询只回溯时间点前 5 分钟内的样本：巡检窗口结束时间在未来时
	// （如生成当前所在巡检周报告）按结束时间查必然为空，改用当前时间查询
	if now := time.Now().Unix(); ts > now {
		ts = now
	}
	res, err := c.QueryInstant("system_info", ts)
	if err != nil {
		log.Debugf("system_info 查询失败（%v），表 3 操作系统 / CPU 架构列将留空。", err)
	} else {
		for _, s := range res {
			id := s.Metric["ident"]
			if id == "" {
				continue
			}
			meta.Live[id] = true
			if v := osDisplay(s.Metric); v != "" {
				meta.OS[id] = v
			}
			if ip := strings.TrimSpace(s.Metric["host_ip"]); ip != "" {
				meta.IP[id] = ip
			}
			if a := archOf(s.Metric["kernel_version"]); a != "" {
				meta.Arch[id] = a
			}
		}
	}
	// categraf 自身监控指标带 ident + version 标签，是 agent 版本号的唯一来源
	// （v0.4.36-<commit sha> 形态）。查询失败只影响该列，不影响主流程。
	if res, err := c.QueryInstant("categraf_info", ts); err != nil {
		log.Debugf("categraf_info 查询失败（%v），agent 版本将留空。", err)
	} else {
		for _, s := range res {
			id := s.Metric["ident"]
			v := strings.TrimSpace(s.Metric["version"])
			if id != "" && v != "" {
				meta.Agent[id] = v
			}
		}
	}
	return meta
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
// ident 由「-」分隔、全部字段靠约定命名，常见形态：
//
//	000002-10.194.67.194-泸州市环保三级统筹项目-电子签章      （租户-IP-项目-角色）
//	000095-10.40.1.2-智慧民政-业务服务器8-张三               （租户-IP-项目-角色-工程师）
//	000093-192.168.30.105-大数据生产区-天地图政务版-业务服务器1-邹源（租户-IP-分区-项目-角色-工程师）
//
// ident 段形态识别：IP 段（如 10.40.1.2）与租户 ID 段（如 000064）。
// ident 不含 IP 段的数据源里，首段为租户 ID（≥4 位数字），用于可靠定位后续字段边界。
var (
	ipRe       = regexp.MustCompile(`^\d{1,3}(?:\.\d{1,3}){3}$`)
	tenantIDRe = regexp.MustCompile(`^\d{4,}$`)
)

// ident 解析选项
type IdentOpts struct {
	Filter       string // ident 关键字过滤（空=全部）
	Layout       string // auto / section-first / project-first
	EngineerTail string // 【已弃用】曾用于从 ident 末尾段推断工程师，现仅保留以兼容旧配置
}

// identOpts 由配置生成 ident 解析选项
func identOpts(cfg *Config) IdentOpts {
	return IdentOpts{Filter: cfg.Project, Layout: cfg.Layout, EngineerTail: cfg.EngineerTail}
}

type IdentInfo struct {
	Tenant, IP, Section, Project, Role, Engineer string
}

// parseIdent 解析 ident。layout：auto/section-first/project-first。
//
// 「运维工程师」**不从 ident 推断**，恒为占位值「—」，只认 -report_engineer 手动指定。
// 原因：ident 里技术角色词（数据库 / 中间件 / 前后端 / 大屏 / 政务网 / 主数据库…）
// 与姓名（张登杰 / 邹源 / 佘发彬）都是 2~4 个纯汉字，字面完全无法区分。
// 早期按「摘掉末尾段后仍能解析出角色就当姓名」的启发式判定，在 ds39 这类
// `租户-IP-项目-子项目-角色` 的四段式 ident 上会把角色误判成工程师
// （如「…-公共信用信息共享平台-信用二期-中间件」→ 工程师=「中间件」），
// 导致报告表 1 的「运维工程师」栏显示成一串角色词，故彻底改为手动指定。
//
// engineerTail 参数保留仅为兼容既有调用与 `-ident_engineer_tail` 配置，不再影响解析结果。
func parseIdent(ident, layout, engineerTail string) IdentInfo {
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
	// ident 不含 IP 段的形态（如「租户-项目-角色」）：此时首段按租户 ID 约定（如 000064）识别，
	// 其余段按「项目 + 角色（+ 可选工程师）」解析；真实 IP 由调用方从 host_ip 标签回填。
	// 若首段也不像租户 ID，则无法可靠拆分，整体作为项目名兜底。
	if idx < 0 {
		if len(parts) >= 2 && tenantIDRe.MatchString(parts[0]) {
			out.Tenant = parts[0]
			out.IP = "—"
			return fillIdentTail(out, parts[1:], layout, engineerTail)
		}
		out.Project = strings.TrimSpace(ident)
		if out.Project == "" {
			out.Project = "—"
		}
		return out
	}
	out.Tenant = strings.Join(parts[:idx], "-")
	out.IP = parts[idx]
	return fillIdentTail(out, parts[idx+1:], layout, engineerTail)
}

// asciiIdentRe 匹配「纯 ASCII 短标识」形态的段（如 db / web / kc / WAF）。
// 注意 WAF 等大写标识同样属于此形态，故不限小写。
var asciiIdentRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,11}$`)

// projectFirstRe 匹配「项目在前」命名习惯中作为项目名首段的机构名（如「泸州市委组织部」「大数据生产区」）
var projectFirstRe = regexp.MustCompile(`^[\p{Han}]{2,}(?:市|州|县|区|局|委|办|部|厅|院|校|司|集团|公司|中心)`)

// mergeProjectSegs 处理「项目名本身含连字符」的 ident（如 000063-泸州市委组织部-公务员培训网-db，
// 项目实为「泸州市委组织部-公务员培训网」、角色为 db）。判断依据：首段是机构名（项目名首段）、
// 末段是 ASCII 短标识（角色），中间段为中文 —— 此时把首段与中间段并成一个项目名。
func mergeProjectSegs(rest []string) []string {
	if len(rest) < 3 || !projectFirstRe.MatchString(rest[0]) || !asciiIdentRe.MatchString(rest[len(rest)-1]) {
		return rest
	}
	return append([]string{strings.Join(rest[:len(rest)-1], "-")}, rest[len(rest)-1])
}

// fillIdentTail 解析 IP 段之后（或 ident 无 IP 段时的首段之后）的项目 / 分区 / 角色。
//
// 关于「运维工程师」：ident 里不再推断工程师姓名。原因见 parseIdent 的注释——
// 技术角色词（数据库 / 中间件 / 前后端 / 大屏 / 政务网 / 主数据库…）恰好都是 2~4 个汉字，
// 与姓名（张登杰 / 邹源 / 佘发彬）在字面上完全无法区分，误判率极高。
// 工程师统一由 -report_engineer（或 REPORT_ENGINEER 环境变量）手动指定，见 main.go 覆盖逻辑。
func fillIdentTail(out IdentInfo, rest []string, layout, engineerTail string) IdentInfo {
	if len(rest) == 0 {
		return out
	}
	// 命名布局：rest[0] 以「区」结尾视为「分区在前」，否则「项目在前」
	lay := layout
	if lay == "auto" {
		if strings.HasSuffix(rest[0], "区") {
			lay = "section-first"
		} else {
			lay = "project-first"
		}
	}
	rest = mergeProjectSegs(rest)
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
	DiskParts  []DiskPart // 各挂载点的分区明细（表 5 磁盘分区使用明细）
	DiskIO     float64
	Conn       float64
	NoConn     bool
	Net        float64
	NetPeak    float64
	SampleRate float64
	OS         string
	Arch       string // CPU 架构（由 system_info 的 kernel_version 解析；内核串不含架构时为空）
	AgentVer   string // categraf agent 版本（categraf_info 的 version 标签，用于清单核对）
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

// dedupeByIdent 剔除同一台主机的历史残留 ident，保证每个 IP 只输出一条记录。
//
// 背景：system_n_cpus 等指标在查询窗口内会累计「出现过的」时间序列，运维改过主机名时，
// 同一台物理主机会同时存在多个 ident（如 ds39 的 10.192.197.17 同时有
// 「000026-10.192.197.17-信用一期-前置机1」与「…-公共信用信息共享平台-信用一期-前置机1」，以及
// IP 与项目名粘连的旧串）。若不去重，报表会出现同一 IP 多行，且旧 ident 常解析不出角色。
//
// 「最新」的判定优先级（前者优先，同级别按 ident 字典序稳定取舍）：
//  1. 出现在 system_info 当前快照中的 ident —— 主机仍在报，即改名后的现行命名；
//  2. 能从 ident 自身解析出 IP 段的 —— 命名更完整，避免误判为无 IP 的数据源；
//  3. 字符更长者 —— 通常保留了更完整的「项目-角色」结构。
//
// 归并键取「host_ip 标签」或「ident 内的 IP 段」。若两者都取不到，即该 ident 既不在
// 当前快照中、也定位不到任何主机——属于改名后的纯残留，直接丢弃（不产出无 IP 的行）。
// 但当 live 为空（system_info 查询失败，无从判断）时，这类 ident 原样保留，避免误删主机。
func dedupeByIdent(idents []string, hostIP map[string]string, live map[string]bool) []string {
	// 先为每个 ident 求「所属主机」的归并键：优先 host_ip 标签，其次 ident 内的 IP 段
	type cand struct {
		ident string
		live  bool
		hasIP bool
	}
	score := func(id string) cand {
		return cand{ident: id, live: live[id], hasIP: ipKeyOf(id) != ""}
	}
	better := func(a, b cand) bool {
		if a.live != b.live {
			return a.live
		}
		if a.hasIP != b.hasIP {
			return a.hasIP
		}
		if len(a.ident) != len(b.ident) {
			return len(a.ident) > len(b.ident)
		}
		return a.ident < b.ident
	}

	snapshotOK := len(live) > 0 // system_info 可用时才敢丢弃无法归并的 ident
	best := map[string]cand{}   // 归并键 -> 当前最优
	order := []string{}         // 归并键出现顺序，保证输出稳定
	var orphan []string         // 无法归并到任何 IP 的 ident（保留，或判定为残留丢弃）
	for _, id := range idents {
		key := hostIP[id]
		if key == "" {
			key = ipKeyOf(id)
		}
		if key == "" {
			if snapshotOK && !live[id] {
				continue // 快照里没有、也定位不到 IP —— 改名后的残留，丢弃
			}
			orphan = append(orphan, id)
			continue
		}
		c := score(id)
		cur, ok := best[key]
		if !ok {
			best[key] = c
			order = append(order, key)
			continue
		}
		if better(c, cur) {
			best[key] = c
		}
	}

	out := make([]string, 0, len(best)+len(orphan))
	for _, k := range order {
		out = append(out, best[k].ident)
	}
	return append(out, orphan...)
}

// ipKeyOf 从 ident 中提取 IP 段（用于去重归并）；不含 IP 段时返回空串。
func ipKeyOf(ident string) string {
	for _, seg := range strings.Split(ident, "-") {
		if ipRe.MatchString(strings.TrimSpace(seg)) {
			return strings.TrimSpace(seg)
		}
	}
	return ""
}

// diskVol 单个挂载点的容量元信息（来自 disk_total 指标）。
type diskVol struct {
	device string
	fstype string
	total  float64 // 字节
}

// DiskPart 单个挂载点的分区明细，用于「磁盘分区使用明细」表。
type DiskPart struct {
	Path    string  // 挂载点
	Fstype  string  // 文件系统类型（ext4 / xfs / NTFS / nfs4 …）
	Device  string  // 设备名（dm-0 / sda2 / C: / 远端 NFS 路径）
	CapGB   float64 // 容量（GB）
	UsedPct float64 // 周期内平均使用率
}

// diskRep 归并后的代表挂载点
type diskRep struct {
	path  string
	total float64
}

// betterDiskRep 判断候选代表挂载点是否更优：容量大者优先；容量相同取路径更短者
// （bind mount / Docker 子目录的路径通常更长），再按字典序兜底，
// 保证同一份数据多次运行选出的代表路径稳定（map 遍历顺序是随机的）。
func betterDiskRep(a, b diskRep) bool {
	if a.total != b.total {
		return a.total > b.total
	}
	if len(a.path) != len(b.path) {
		return len(a.path) < len(b.path)
	}
	return a.path < b.path
}

// pickDiskDevReps 按「设备」归并挂载点，返回 设备键 → 代表挂载点。
// 同一块块设备常被挂载到多个路径（bind mount、Docker 子目录等），
// 按挂载点直接相加会重复计数——实测 dm-0 同时挂在 /data、/mnt/arkbase_backups、
// /mnt/arkbase_backups2（各 999 GB），相加会虚增 3 倍。
// skipRemote 为 true 时跳过网络/共享文件系统。
func pickDiskDevReps(vols map[string]diskVol, skipRemote bool) map[string]diskRep {
	best := map[string]diskRep{}
	for path, v := range vols {
		if skipRemote && isRemoteFS(v.fstype) {
			continue
		}
		key := v.device
		if key == "" {
			key = "path:" + path
		}
		cand := diskRep{path: path, total: v.total}
		if cur, ok := best[key]; !ok || betterDiskRep(cand, cur) {
			best[key] = cand
		}
	}
	return best
}

// diskPartsOf 把一台主机的挂载点整理成按挂载点排序的分区明细，用于「磁盘分区使用明细」表。
//
// 两步收敛：
//  1. 剔除伪分区（tmpfs / overlay 等内存文件系统，以及 /run、/dev、/sys 等运行时虚拟目录）；
//  2. 同一设备挂到多个路径时只保留一个代表路径，避免同一块盘出现多行重复数据。
//
// 与表 3 的容量口径差异：本函数**不排除** NFS 等共享存储（共享存储同样是会写满的分区），
// 故两表容量不可直接对照。
func diskPartsOf(vols map[string]diskVol, seq map[string][][2]float64) []DiskPart {
	if len(vols) == 0 {
		return nil
	}
	// 先剔伪分区再归并，避免某设备的代表路径恰好落在被剔除的伪挂载点上
	real := make(map[string]diskVol, len(vols))
	for path, v := range vols {
		if isRealMount(path, v.fstype) {
			real[path] = v
		}
	}
	if len(real) == 0 {
		return nil
	}
	keep := map[string]bool{}
	for _, r := range pickDiskDevReps(real, false) {
		keep[r.path] = true
	}

	out := make([]DiskPart, 0, len(real))
	for path, v := range real {
		if !keep[path] {
			continue
		}
		out = append(out, DiskPart{
			Path:    path,
			Fstype:  v.fstype,
			Device:  v.device,
			CapGB:   v.total / (1 << 30),
			UsedPct: avg(pairVals(seq[path])),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// pairVals 取出 (ts, val) 序列中的值部分
func pairVals(pairs [][2]float64) []float64 {
	out := make([]float64, 0, len(pairs))
	for _, p := range pairs {
		out = append(out, p[1])
	}
	return out
}

// pickDiskReps 把一台主机的挂载点归并成「参与统计的挂载点」集合，返回总容量（字节）
// 与「代表挂载点 → 容量（字节）」。
//
// 两个必要的收敛动作：
//  1. **按设备去重**（见 pickDiskDevReps）：同一块块设备常被挂载到多个路径
//     （bind mount、Docker 子目录等），按挂载点直接相加会重复计数；
//  2. **剔除网络/共享文件系统**：不是主机自带容量，且可能被多台主机重复挂载。
//
// 若剔除共享存储后一个挂载点都不剩（主机只挂了 NFS），回退为全部计入，
// 避免容量被显示成 0。
func pickDiskReps(vols map[string]diskVol) (float64, map[string]float64) {
	toMap := func(reps map[string]diskRep) map[string]float64 {
		out := make(map[string]float64, len(reps))
		for _, r := range reps {
			out[r.path] = r.total
		}
		return out
	}

	reps := toMap(pickDiskDevReps(vols, true))
	if len(reps) == 0 {
		reps = toMap(pickDiskDevReps(vols, false))
	}
	total := 0.0
	for _, v := range reps {
		total += v
	}
	return total, reps
}

// weightedDisk 按容量加权求逐时刻的整体磁盘使用率：
//
//	使用率(ts) = Σ(挂载点容量 × 该挂载点使用率(ts)) / Σ(有数据的挂载点容量)
//
// 与「容量 = 全部挂载点合计」保持同一口径，避免出现「容量是整个盘、使用率只是某个
// 分区」这种分子分母不匹配的情况。返回按时间升序的使用率序列。
func weightedDisk(reps map[string]float64, seq map[string][][2]float64) []float64 {
	at := make(map[string]map[float64]float64, len(reps))
	tsSet := map[float64]bool{}
	for path := range reps {
		m := make(map[float64]float64, len(seq[path]))
		for _, pv := range seq[path] {
			m[pv[0]] = pv[1]
			tsSet[pv[0]] = true
		}
		at[path] = m
	}
	tsList := make([]float64, 0, len(tsSet))
	for ts := range tsSet {
		tsList = append(tsList, ts)
	}
	sort.Slice(tsList, func(i, j int) bool { return tsList[i] < tsList[j] })

	out := make([]float64, 0, len(tsList))
	for _, ts := range tsList {
		num, den := 0.0, 0.0
		for path, cap := range reps {
			p, ok := at[path][ts]
			if !ok {
				continue
			}
			num += cap * p
			den += cap
		}
		if den > 0 {
			out = append(out, num/den)
		}
	}
	return out
}

// ReadN9E 从 n9e 采集 [start,end] 窗口内各主机指标；opts.Filter 非空时按 ident 关键字过滤
func ReadN9E(c *N9EClient, start, end int64, step int, opts IdentOpts) ([]HostRow, error) {
	c.login()
	identFilter, layout := opts.Filter, opts.Layout
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

	meta := c.fetchHostMeta(end)

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
	io := map[string][][][2]float64{}            // ident -> series -> [(ts, val)]
	disk := map[string]map[string][][2]float64{} // ident -> path -> [(ts, val)]
	dtot := map[string]map[string]diskVol{}      // ident -> path -> 容量元信息

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
		v, ok := lastVal(s)
		if !ok {
			continue
		}
		if dtot[id] == nil {
			dtot[id] = map[string]diskVol{}
		}
		// 同一 (ident, path) 可能出现多条序列（设备变更等），取容量最大者
		if cur, exists := dtot[id][path]; !exists || v > cur.total {
			dtot[id][path] = diskVol{device: s.Metric["device"], fstype: s.Metric["fstype"], total: v}
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
	idents = dedupeByIdent(idents, meta.IP, meta.Live)
	sort.Slice(idents, func(i, j int) bool {
		ki, _ := ipKey(idents[i])
		kj, _ := ipKey(idents[j])
		return ki < kj
	})

	rows := []HostRow{}
	for _, ident := range idents {
		info := parseIdent(ident, layout, opts.EngineerTail)
		// ident 不含 IP 段的数据源，用 system_info 的 host_ip 标签补齐真实 IP
		if (info.IP == "—" || info.IP == "") && meta.IP[ident] != "" {
			info.IP = meta.IP[ident]
		}
		cpuV := cpu[ident]
		memV := memP[ident]
		netV := net[ident]

		// 磁盘容量：全部本地挂载点之和（同一设备只计一次，不含 NFS 等共享存储）
		diskCapBytes, diskReps := pickDiskReps(dtot[ident])
		diskCap := diskCapBytes / GB
		// 磁盘使用率：按挂载点容量加权，与容量口径保持一致
		diskVals := weightedDisk(diskReps, disk[ident])
		// 分区明细：逐挂载点列出真实分区（已剔除 tmpfs、/run、/dev 等伪分区）
		diskParts := diskPartsOf(dtot[ident], disk[ident])

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
			DiskParts:  diskParts,
			DiskIO:     avg(ioVals),
			Net:        avg(netV),
			NetPeak:    sliceMax(netV),
			SampleRate: 1.0,
			OS:         meta.OS[ident],
			Arch:       meta.Arch[ident],
			AgentVer:   meta.Agent[ident],
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

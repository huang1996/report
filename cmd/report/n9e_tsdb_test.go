package main

// n9e_tsdb_test.go —— 直连 VictoriaMetrics（-tsdb_mode=vm/vmcluster）的路径与认证测试
//
// 背景：部分项目机房不具备连接中心 n9e 的条件，只部署 categraf + VictoriaMetrics——
// categraf 把指标 remote write 到时序库（不经过夜莺），报告程序直连时序库查询。
// 与经夜莺访问相比只有两处差别：**URL 前缀**与**认证方式**；PromQL、参数与响应
// 结构完全一致（都是同一套 Prometheus 兼容接口）。这里验证：
//  1. 三种模式的接口路径拼接正确，且 base 末尾斜杠不会拼出 `//`；
//  2. 模式取值容错（大小写 / 空格 / 未知值一律回落 n9e，保证旧配置行为不变）；
//  3. 直连 VM 时不请求夜莺的 /api/n9e/auth/login，账号密码改走 Basic Auth；
//  4. 数据源相关能力在 VM 模式下的降级行为（单机无数据源概念、集群无法枚举租户）；
//  5. 端到端跑通 ReadN9E，并能在 categraf 未配 ident 时自动回退 agent_hostname。

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- 路径拼接 ----

func TestAPIPathPerMode(t *testing.T) {
	// base 末尾带斜杠，验证会被 TrimRight 掉（否则会拼出 //api）
	c := NewN9EClient("http://vm:8428/", "3", "", "", "", time.Second, false)

	if got := c.Mode(); got != TSDBModeN9E {
		t.Errorf("未设置时默认模式应为 n9e，实得 %q", got)
	}
	if got, want := c.api("/query", "7"), "http://vm:8428/api/n9e/proxy/7/api/v1/query"; got != want {
		t.Errorf("n9e 模式路径不符：\n got %s\nwant %s", got, want)
	}

	// 单机版 VictoriaMetrics：路径里不出现数据源编号
	c.SetMode(TSDBModeVM)
	if got, want := c.api("/query_range", "7"), "http://vm:8428/api/v1/query_range"; got != want {
		t.Errorf("vm 模式路径不符：\n got %s\nwant %s", got, want)
	}
	if got, want := c.api("/label/ident/values", "7"), "http://vm:8428/api/v1/label/ident/values"; got != want {
		t.Errorf("vm 模式标签接口路径不符：\n got %s\nwant %s", got, want)
	}

	// 集群版 vmselect：ds 即租户号 accountID
	c.SetMode(TSDBModeVMCluster)
	if got, want := c.api("/query", "3"), "http://vm:8428/select/3/prometheus/api/v1/query"; got != want {
		t.Errorf("vmcluster 模式路径不符：\n got %s\nwant %s", got, want)
	}
	// 租户号缺失时按 0 兜底（VM 单租户默认值），避免拼出 /select//prometheus
	if got, want := c.api("/query", ""), "http://vm:8428/select/0/prometheus/api/v1/query"; got != want {
		t.Errorf("vmcluster 模式空租户号应兜底为 0：\n got %s\nwant %s", got, want)
	}
}

func TestSetModeNormalization(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", TSDBModeN9E},
		{"n9e", TSDBModeN9E},
		{"N9E", TSDBModeN9E},
		{"  n9e  ", TSDBModeN9E},
		{"prometheus", TSDBModeN9E}, // 未知值回落，避免拼出错误路径
		{"vm", TSDBModeVM},
		{" VM ", TSDBModeVM},
		{"vmcluster", TSDBModeVMCluster},
		{"VMCluster", TSDBModeVMCluster},
	}
	for _, c := range cases {
		cli := NewN9EClient("http://x", "1", "", "", "", time.Second, false)
		cli.SetMode(c.in)
		if got := cli.Mode(); got != c.want {
			t.Errorf("SetMode(%q) 应为 %q，实得 %q", c.in, c.want, got)
		}
	}
	// 零值客户端（未走构造函数）也要按 n9e 处理
	zero := &N9EClient{base: "http://x"}
	if got := zero.Mode(); got != TSDBModeN9E {
		t.Errorf("零值客户端应视为 n9e，实得 %q", got)
	}
}

// ---- 认证 ----

// authStub 记录收到的请求路径与 Authorization 头
type authStub struct {
	mu    sync.Mutex
	paths []string
	auths []string
}

func (s *authStub) handler(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.paths = append(s.paths, r.URL.Path)
	s.auths = append(s.auths, r.Header.Get("Authorization"))
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"status":"success","data":{"result":[]}}`)
}

func (s *authStub) snapshot() ([]string, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.paths...), append([]string(nil), s.auths...)
}

// TestVMLoginSkipsN9EAuthEndpoint 直连 VM 时不能去调夜莺的登录接口——
// 那个接口在 VictoriaMetrics 上根本不存在（404），只会白等一轮超时。
func TestVMLoginSkipsN9EAuthEndpoint(t *testing.T) {
	withQuietLogger(t)
	stub := &authStub{}
	srv := httptest.NewServer(http.HandlerFunc(stub.handler))
	defer srv.Close()

	cli := NewN9EClient(srv.URL, "1", "", "ops", "secret", 5*time.Second, false)
	cli.SetMode(TSDBModeVM)
	cli.SetRetry(1, 0)
	cli.login()
	if _, err := cli.QueryInstant("up", 1758000000); err != nil {
		t.Fatalf("查询失败：%v", err)
	}

	paths, auths := stub.snapshot()
	if len(paths) != 1 || paths[0] != "/api/v1/query" {
		t.Errorf("vm 模式只应请求 /api/v1/query，实际 %v", paths)
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("ops:secret"))
	if len(auths) != 1 || auths[0] != want {
		t.Errorf("vm 模式配了账号密码应改用 Basic Auth（前置 vmauth 场景），实际 %v", auths)
	}
}

// TestVMTokenStillUsesBearer 配了 token 时仍走 Bearer（与 n9e 模式一致），
// 便于对接前置网关做 token 鉴权的部署方式。
func TestVMTokenStillUsesBearer(t *testing.T) {
	withQuietLogger(t)
	stub := &authStub{}
	srv := httptest.NewServer(http.HandlerFunc(stub.handler))
	defer srv.Close()

	cli := NewN9EClient(srv.URL, "1", "tok-123", "", "", 5*time.Second, false)
	cli.SetMode(TSDBModeVM)
	cli.SetRetry(1, 0)
	cli.login()
	if _, err := cli.QueryInstant("up", 1758000000); err != nil {
		t.Fatalf("查询失败：%v", err)
	}

	_, auths := stub.snapshot()
	if len(auths) != 1 || auths[0] != "Bearer tok-123" {
		t.Errorf("配了 token 应使用 Bearer，实际 %v", auths)
	}
}

// TestVMNoAuthWhenNothingConfigured 单机 VictoriaMetrics 默认无鉴权：
// 不配账号密码时不应携带任何认证头。
func TestVMNoAuthWhenNothingConfigured(t *testing.T) {
	withQuietLogger(t)
	stub := &authStub{}
	srv := httptest.NewServer(http.HandlerFunc(stub.handler))
	defer srv.Close()

	cli := NewN9EClient(srv.URL, "1", "", "", "", 5*time.Second, false)
	cli.SetMode(TSDBModeVM)
	cli.SetRetry(1, 0)
	cli.login()
	if _, err := cli.QueryInstant("up", 1758000000); err != nil {
		t.Fatalf("查询失败：%v", err)
	}

	_, auths := stub.snapshot()
	if len(auths) != 1 || auths[0] != "" {
		t.Errorf("未配置认证信息时不应携带 Authorization，实际 %v", auths)
	}
}

// ---- 数据源能力的降级 ----

func TestListDatasourcesPerMode(t *testing.T) {
	withQuietLogger(t)

	// 单机 VM：整库就是一个数据源，直接返回虚拟条目，不发起请求
	cli := NewN9EClient("http://vm:8428", "1", "", "", "", time.Second, false)
	cli.SetMode(TSDBModeVM)
	dss, err := cli.ListDatasources()
	if err != nil {
		t.Fatalf("单机 VM 应返回虚拟数据源，实际报错：%v", err)
	}
	if len(dss) != 1 || dss[0].Type != "victoriametrics" {
		t.Errorf("单机 VM 应返回 1 条虚拟数据源，实际 %+v", dss)
	}

	// 集群 VM：租户号无法枚举，应给出可操作的提示而不是空列表
	cli.SetMode(TSDBModeVMCluster)
	_, err = cli.ListDatasources()
	if err == nil {
		t.Fatal("集群模式应提示无法枚举租户")
	}
	if !strings.Contains(err.Error(), "accountID") {
		t.Errorf("错误信息应提到 accountID，实得：%v", err)
	}
}

// TestResolveDSByProjectRejectsVMMode VM 模式没有「多个数据源」这一层，
// 不应再逐个数据源去试（地址不可达时会白等一串超时）。
func TestResolveDSByProjectRejectsVMMode(t *testing.T) {
	withQuietLogger(t)
	// 指向不可达地址：只要真发了请求就会失败，用于反证「没有发请求」
	cli := NewN9EClient("http://127.0.0.1:1", "1", "", "", "", time.Second, false)
	cli.SetMode(TSDBModeVM)
	cli.SetRetry(1, 0)

	_, err := ResolveDSByProject("任意项目", cli, 5)
	if err == nil {
		t.Fatal("VM 模式没有数据源概念，应直接返回提示")
	}
	if !strings.Contains(err.Error(), "ident") {
		t.Errorf("提示里应说明项目过滤改由 ident 关键字完成，实得：%v", err)
	}
}

// ---- 端到端：直连时序库采集 ----

// vmStub 模拟 VictoriaMetrics 的 Prometheus 兼容接口。
// ident / agentHost 为空串表示库中没有对应标签——后者用于复现「categraf 未配
// [global.labels] ident」这一隔离部署最常见的形态（此时只有默认写入的 agent_hostname）。
type vmStub struct {
	mu        sync.Mutex
	paths     []string
	ident     string
	agentHost string
}

func (s *vmStub) handler(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.paths = append(s.paths, r.URL.Path)
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")

	// /api/v1/label/<name>/values —— 供主机标识的自动探测使用
	if p := r.URL.Path; strings.HasPrefix(p, "/api/v1/label/") && strings.HasSuffix(p, "/values") {
		name := strings.TrimSuffix(strings.TrimPrefix(p, "/api/v1/label/"), "/values")
		vals := []string{}
		switch name {
		case HostLabelIdent:
			if s.ident != "" {
				vals = append(vals, s.ident)
			}
		case HostLabelAgentHostname:
			if s.agentHost != "" {
				vals = append(vals, s.agentHost)
			}
		}
		b, _ := json.Marshal(map[string]interface{}{"status": "success", "data": vals})
		_, _ = w.Write(b)
		return
	}

	labels := `"host_ip":"10.9.9.9","os_name":"Kylin",` +
		`"kernel_version":"4.19.90-52.22.v2207.ky10.aarch64","version":"v0.4.36-84dcda76"`
	if s.ident != "" {
		labels = `"ident":"` + s.ident + `",` + labels
	}
	if s.agentHost != "" {
		labels = `"agent_hostname":"` + s.agentHost + `",` + labels
	}
	// 所有查询（query / query_range）共用一条序列即可满足解析
	fmt.Fprintf(w, `{"status":"success","data":{"result":[{"metric":{%s},"values":[[1758000000,"8"]]}]}}`, labels)
}

func (s *vmStub) snapshotPaths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.paths...)
}

func (s *vmStub) sawProxyPath() bool {
	for _, p := range s.snapshotPaths() {
		if strings.Contains(p, "/api/n9e/") {
			return true
		}
	}
	return false
}

// sawLabelProbe 是否请求过某标签的取值列表（即做过主机标识探测）
func (s *vmStub) sawLabelProbe(name string) bool {
	want := "/api/v1/label/" + name + "/values"
	for _, p := range s.snapshotPaths() {
		if p == want {
			return true
		}
	}
	return false
}

const vmIdent = "000042-10.9.9.9-隔离机房项目-应用服务器"

func newVMReadClient(t *testing.T, stub *vmStub) *N9EClient {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(stub.handler))
	t.Cleanup(srv.Close)
	cli := NewN9EClient(srv.URL, "1", "", "", "", 5*time.Second, false)
	cli.SetMode(TSDBModeVM)
	cli.SetRetry(1, 0) // 该场景不测重试，降为 1 次以免失败时白等
	return cli
}

// TestReadN9EFromVictoriaMetrics 隔离机房的完整数据链路：
// categraf ──remote write──▶ VictoriaMetrics ──Prometheus API──▶ 报告程序。
func TestReadN9EFromVictoriaMetrics(t *testing.T) {
	withQuietLogger(t)
	stub := &vmStub{ident: vmIdent}
	cli := newVMReadClient(t, stub)

	rows, err := ReadN9E(cli, 1758000000, 1758003600, 300, IdentOpts{Layout: "auto"})
	if err != nil {
		t.Fatalf("直连 VictoriaMetrics 采集失败：%v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("应取回 1 台主机，实际 %d 台", len(rows))
	}
	r := rows[0]
	if r.IP != "10.9.9.9" || r.Project != "隔离机房项目" || r.Role != "应用服务器" {
		t.Errorf("ident 解析结果不符：%+v", r)
	}
	// 架构从容内核串解析（aarch64 → arm64），不应落到兜底的 amd64
	if r.Arch != "arm64" {
		t.Errorf("应解析出 arm64，实际 %q", r.Arch)
	}
	if r.OS == "" || r.AgentVer == "" {
		t.Errorf("应取到操作系统与 agent 版本，实际 OS=%q AgentVer=%q", r.OS, r.AgentVer)
	}
	// 直连模式下不应出现任何夜莺代理路径
	if stub.sawProxyPath() {
		t.Errorf("vm 模式不应请求 /api/n9e/ 路径，实际 %v", stub.paths)
	}
	// 采样率：窗口 3600s / step 300s = 12 个点，桩只给 1 个 → 1/12
	if got := r.SampleRate; got < 0.08 || got > 0.09 {
		t.Errorf("采样率应为 1/12≈0.083，实得 %v", got)
	}
}

// TestReadN9EVMDetectsMissingHostLabel 两个主机标识标签都取不到时（categraf 设了
// omit_hostname 或被裁剪），应明确报错并给出可操作的配置指引，
// 而不是产出一份「0 台主机」的空报告。
func TestReadN9EVMDetectsMissingHostLabel(t *testing.T) {
	withQuietLogger(t)
	stub := &vmStub{} // ident 与 agent_hostname 都不返回
	cli := newVMReadClient(t, stub)

	_, err := ReadN9E(cli, 1758000000, 1758003600, 300, IdentOpts{Layout: "auto"})
	if err == nil {
		t.Fatal("指标缺少主机标识标签时应报错")
	}
	if !strings.Contains(err.Error(), "ident") || !strings.Contains(err.Error(), "global.labels") {
		t.Errorf("错误应指出缺 ident 并给出配置位置，实得：%v", err)
	}
}

// ---- 主机标识标签的自动回退（ident → agent_hostname）----

// TestReadN9EVMHintWhenFixedLabelAbsent 显式指定 ident 而库里只有 agent_hostname 时，
// 提示应指向「改用自动探测」，而不是笼统地说两种标签都没有。
func TestReadN9EVMHintWhenFixedLabelAbsent(t *testing.T) {
	withQuietLogger(t)
	stub := &vmStub{agentHost: vmIdent}
	cli := newVMReadClient(t, stub)
	cli.SetHostLabel(HostLabelIdent)

	_, err := ReadN9E(cli, 1758000000, 1758003600, 300, IdentOpts{Layout: "auto"})
	if err == nil {
		t.Fatal("固定用 ident 而库里没有该标签时应报错")
	}
	if !strings.Contains(err.Error(), "n9e_host_label") {
		t.Errorf("提示应指出是该参数导致（改回 auto 即可），实得：%v", err)
	}
}

// TestReadN9EVMFallsBackToAgentHostname 隔离机房最常见的形态：categraf 未配
// [global.labels] ident，序列上只有它默认写入的 agent_hostname。程序应自动回退，
// 直接用 hostname 作为主机标识出报告（它的值就等于 n9e 环境里 ident 的原值）。
func TestReadN9EVMFallsBackToAgentHostname(t *testing.T) {
	withQuietLogger(t)
	stub := &vmStub{agentHost: vmIdent} // 只有 agent_hostname
	cli := newVMReadClient(t, stub)

	rows, err := ReadN9E(cli, 1758000000, 1758003600, 300, IdentOpts{Layout: "auto"})
	if err != nil {
		t.Fatalf("应自动回退到 agent_hostname，实际报错：%v", err)
	}
	if got := cli.HostLabel(); got != HostLabelAgentHostname {
		t.Errorf("生效的主机标识标签应为 agent_hostname，实得 %q", got)
	}
	if len(rows) != 1 {
		t.Fatalf("应取回 1 台主机，实际 %d 台", len(rows))
	}
	if r := rows[0]; r.IP != "10.9.9.9" || r.Project != "隔离机房项目" || r.Role != "应用服务器" {
		t.Errorf("回退后的解析结果应与 ident 链路一致：%+v", r)
	}
	if !stub.sawLabelProbe(HostLabelIdent) || !stub.sawLabelProbe(HostLabelAgentHostname) {
		t.Errorf("应先探测 ident、再回退探测 agent_hostname，实际请求 %v", stub.snapshotPaths())
	}

	// 项目过滤（-n9e_project）在回退后同样按主机标识生效
	errMsg := ""
	_, err = ReadN9E(cli, 1758000000, 1758003600, 300, IdentOpts{Filter: "不存在的项目", Layout: "auto"})
	if err == nil {
		t.Fatal("按主机标识过滤未命中时应报错")
	}
	errMsg = err.Error()
	if !strings.Contains(errMsg, "不存在的项目") || !strings.Contains(errMsg, vmIdent) {
		t.Errorf("错误里应含过滤词与现有主机列表，实得：%v", err)
	}
}

// TestReadN9EVMKeepsIdentWhenPresent 库里两种标签并存（例如 categraf 显式配了
// ident 全局标签）：ident 更权威，不应被回退覆盖，也不必再探测第二个标签。
func TestReadN9EVMKeepsIdentWhenPresent(t *testing.T) {
	withQuietLogger(t)
	const other = "000042-10.9.9.8-隔离机房项目-数据库主"
	stub := &vmStub{ident: vmIdent, agentHost: other}
	cli := newVMReadClient(t, stub)

	rows, err := ReadN9E(cli, 1758000000, 1758003600, 300, IdentOpts{Layout: "auto"})
	if err != nil {
		t.Fatalf("采集失败：%v", err)
	}
	if got := cli.HostLabel(); got != HostLabelIdent {
		t.Errorf("两种标签并存时应固定用 ident，实得 %q", got)
	}
	if len(rows) != 1 || rows[0].Ident != vmIdent {
		t.Errorf("应按 ident 取主机，实际 %+v", rows)
	}
	if stub.sawLabelProbe(HostLabelAgentHostname) {
		t.Error("ident 可用时不应再探测 agent_hostname（省一次请求）")
	}
}

// TestHostLabelExplicitDisablesProbe 显式指定 -n9e_host_label 后不再探测。
func TestHostLabelExplicitDisablesProbe(t *testing.T) {
	withQuietLogger(t)
	stub := &vmStub{ident: vmIdent, agentHost: vmIdent}
	cli := newVMReadClient(t, stub)
	cli.SetHostLabel(HostLabelAgentHostname)

	if _, err := ReadN9E(cli, 1758000000, 1758003600, 300, IdentOpts{Layout: "auto"}); err != nil {
		t.Fatalf("采集失败：%v", err)
	}
	if stub.sawLabelProbe(HostLabelIdent) || stub.sawLabelProbe(HostLabelAgentHostname) {
		t.Errorf("显式指定标签名后不应探测，实际请求 %v", stub.snapshotPaths())
	}
}

// TestHostLabelNotProbedInN9EMode n9e 链路恒用 ident（pushgw 已把 agent_hostname
// 就地改名），不做任何探测，保证既有行为完全不变。
func TestHostLabelNotProbedInN9EMode(t *testing.T) {
	withQuietLogger(t)
	stub := &vmStub{ident: vmIdent}
	srv := httptest.NewServer(http.HandlerFunc(stub.handler))
	defer srv.Close()

	cli := NewN9EClient(srv.URL, "1", "", "", "", 5*time.Second, false)
	cli.SetMode(TSDBModeN9E)
	cli.SetHostLabel(HostLabelAuto)
	cli.SetRetry(1, 0)
	if got := cli.HostLabel(); got != HostLabelIdent {
		t.Errorf("n9e 模式下主机标识应为 ident，实得 %q", got)
	}
	if _, err := ReadN9E(cli, 1758000000, 1758003600, 300, IdentOpts{Layout: "auto"}); err != nil {
		t.Fatalf("采集失败：%v", err)
	}
	if stub.sawLabelProbe(HostLabelAgentHostname) {
		t.Errorf("n9e 模式不应探测 agent_hostname，实际请求 %v", stub.snapshotPaths())
	}
}

// TestHostLabelProbeFailureKeepsIdent 探测请求本身失败（地址不可达 / 接口不支持）时
// 保留 ident 不动：宁可走「未找到主机标识」的诊断，也不能误切到 agent_hostname
// 得出一份空报告。
func TestHostLabelProbeFailureKeepsIdent(t *testing.T) {
	withQuietLogger(t)
	cli := NewN9EClient("http://127.0.0.1:1", "1", "", "", "", time.Second, false)
	cli.SetMode(TSDBModeVM)
	cli.SetRetry(1, 0)
	cli.resolveHostLabel("1")
	if got := cli.HostLabel(); got != HostLabelIdent {
		t.Errorf("探测失败时应保留 ident，实得 %q", got)
	}
}

// TestHostLabelProbeRunsOnce 探测只做一次：一次运行可能连续生成多个周期的报告，
// 不应每个周期都重复探测。
func TestHostLabelProbeRunsOnce(t *testing.T) {
	withQuietLogger(t)
	stub := &vmStub{agentHost: vmIdent}
	cli := newVMReadClient(t, stub)

	cli.resolveHostLabel("1")
	before := len(stub.snapshotPaths())
	cli.resolveHostLabel("1")
	if after := len(stub.snapshotPaths()); after != before {
		t.Errorf("第二次调用不应再发探测请求（%d -> %d）", before, after)
	}
}

// TestSetHostLabelNormalization 取值容错：空 / auto 视为自动探测，其余按标签名原样使用。
func TestSetHostLabelNormalization(t *testing.T) {
	cases := []struct {
		in       string
		want     string
		wantAuto bool
	}{
		{"", HostLabelIdent, true},
		{"auto", HostLabelIdent, true},
		{"AUTO", HostLabelIdent, true},
		{"  auto  ", HostLabelIdent, true},
		{"ident", HostLabelIdent, false},
		{"agent_hostname", HostLabelAgentHostname, false},
		{" host ", "host", false}, // 其他标签名同样放行，便于非标准采集端
	}
	for _, c := range cases {
		cli := NewN9EClient("http://x", "1", "", "", "", time.Second, false)
		cli.SetHostLabel(c.in)
		if got := cli.HostLabel(); got != c.want {
			t.Errorf("SetHostLabel(%q) 标签应为 %q，实得 %q", c.in, c.want, got)
		}
		if cli.hostLabelAuto != c.wantAuto {
			t.Errorf("SetHostLabel(%q) 的自动探测标记应为 %v，实得 %v", c.in, c.wantAuto, cli.hostLabelAuto)
		}
	}
	// 零值客户端（未走构造函数）也按 ident 处理
	zero := &N9EClient{base: "http://x"}
	if got := zero.HostLabel(); got != HostLabelIdent {
		t.Errorf("零值客户端应按 ident 处理，实得 %q", got)
	}
}

// TestQueryOfReplacesHostLabel 表达式里的 ident 只作为标签名出现，整体替换是安全的。
func TestQueryOfReplacesHostLabel(t *testing.T) {
	if got := queryOf("cpu", HostLabelIdent); got != n9eQueries["cpu"] {
		t.Errorf("ident 模式下表达式不应变化：%s", got)
	}
	for _, key := range []string{"cpu", "net"} {
		got := queryOf(key, HostLabelAgentHostname)
		if strings.Contains(got, "ident") || !strings.Contains(got, "agent_hostname") {
			t.Errorf("%s 的分组标签应被替换为 agent_hostname，实得 %s", key, got)
		}
	}
	// 不含 ident 的表达式保持原样
	if got := queryOf("mem_pct", HostLabelAgentHostname); got != n9eQueries["mem_pct"] {
		t.Errorf("不含 ident 的表达式应保持原样，实得 %s", got)
	}
	// 未知 key 返回空串（与直接读 map 一致）
	if got := queryOf("nope", HostLabelAgentHostname); got != "" {
		t.Errorf("未知 key 应返回空串，实得 %q", got)
	}
}

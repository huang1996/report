package main

// n9e_retry_test.go —— n9e 请求重试机制单测
//
// 背景：夜莺经网关访问偶发 502 / 响应头超时（实测日志
// `HTTP 502: net/http: timeout awaiting response headers`），过一会儿重试即可成功；
// 而单次失败会让整份报告缺失资源巡检数据。这里验证：
//  1. 瞬时错误（5xx / 429 / 408 / 网络超时 / 200 但正文非 JSON）会重试；
//  2. 非瞬时错误（400 查询语法、401 鉴权等）立即返回，不浪费时间；
//  3. 退避为「首次 base、之后翻倍、单次上限 30s」，且带 ±20% 抖动；
//  4. 尝试次数用尽后返回错误并标注已重试次数。

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// n9eStub 按脚本依次返回响应；脚本用尽后沿用最后一档
type n9eStub struct {
	calls    int32
	statuses []int // 每档的 HTTP 状态码，200 时返回合法 JSON
	body     string
	delay    time.Duration
}

func (s *n9eStub) handler(w http.ResponseWriter, r *http.Request) {
	n := int(atomic.AddInt32(&s.calls, 1))
	if s.delay > 0 {
		time.Sleep(s.delay)
	}
	idx := n - 1
	if idx >= len(s.statuses) {
		idx = len(s.statuses) - 1
	}
	code := s.statuses[idx]
	if code == 200 {
		w.Header().Set("Content-Type", "application/json")
		body := s.body
		if body == "" {
			body = `{"status":"success","data":{"result":[{"metric":{"ident":"0-10.0.0.1-生产区-业务系统A-Web服务器-张三"},"values":[[1758000000,"12.5"]]}]}}`
		}
		fmt.Fprint(w, body)
		return
	}
	w.WriteHeader(code)
	if s.body != "" {
		fmt.Fprint(w, s.body)
	} else {
		fmt.Fprintf(w, "upstream error %d", code)
	}
}

// newStubClient 创建指向 stub 的客户端，退避压到 1ms 以免测试变慢
func newStubClient(t *testing.T, s *n9eStub, attempts int) (*N9EClient, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(s.handler))
	t.Cleanup(srv.Close)
	cli := NewN9EClient(srv.URL, "7", "", "", "", 5*time.Second, false)
	cli.SetRetry(attempts, time.Millisecond)
	return cli, srv
}

func (s *n9eStub) callCount() int { return int(atomic.LoadInt32(&s.calls)) }

// ---- 判定逻辑 ----

func TestRetryableStatus(t *testing.T) {
	retryable := []int{408, 429, 500, 502, 503, 504}
	for _, c := range retryable {
		if !retryableStatus(c) {
			t.Errorf("HTTP %d 应可重试", c)
		}
	}
	// 非瞬时：400 查询语法错误、401/403 鉴权、404 路由不存在、422 等
	for _, c := range []int{200, 301, 400, 401, 403, 404, 405, 422} {
		if retryableStatus(c) {
			t.Errorf("HTTP %d 不应重试", c)
		}
	}
}

func TestRetryDelay(t *testing.T) {
	base := time.Second
	// 首次退避 = base ±20%
	for i := 0; i < 200; i++ {
		d := retryDelay(base, 1)
		if d < 800*time.Millisecond || d > 1200*time.Millisecond {
			t.Fatalf("第 1 次退避 %v 超出 base 的 ±20%% 抖动范围", d)
		}
	}
	// 翻倍：第 2 次约 2×base、第 3 次约 4×base
	for i := 0; i < 200; i++ {
		if d := retryDelay(base, 2); d < 1600*time.Millisecond || d > 2400*time.Millisecond {
			t.Fatalf("第 2 次退避 %v 未按 2 倍递增", d)
		}
		if d := retryDelay(base, 3); d < 3200*time.Millisecond || d > 4800*time.Millisecond {
			t.Fatalf("第 3 次退避 %v 未按 4 倍递增", d)
		}
	}
	// 上限：base 足够大时，很后面的重试也不超过 maxRetryDelay
	for _, attempt := range []int{6, 10, 30} {
		if d := retryDelay(base, attempt); d > maxRetryDelay {
			t.Errorf("第 %d 次退避 %v 超过上限 %v", attempt, d, maxRetryDelay)
		}
	}
	if d := retryDelay(0, 3); d != 0 {
		t.Errorf("base=0 时应立即返回（0），实得 %v", d)
	}
}

// ---- 行为：瞬时错误重试 ----

func TestGetJSONRetriesUntilSuccess(t *testing.T) {
	withQuietLogger(t)
	// 前两次 502，第三次成功——正是线上「502 后重试即可」的场景
	stub := &n9eStub{statuses: []int{502, 502, 200}}
	cli, _ := newStubClient(t, stub, 4)

	var out promResp
	if err := cli.getJSON(cli.api("/query", "7"), &out); err != nil {
		t.Fatalf("两次 502 后应重试成功，实际报错：%v", err)
	}
	if stub.callCount() != 3 {
		t.Errorf("应共请求 3 次（2 次失败 + 1 次成功），实际 %d 次", stub.callCount())
	}
	if len(out.Data.Result) != 1 {
		t.Errorf("应解析出 1 条序列，实际 %d 条", len(out.Data.Result))
	}
}

func TestGetJSONGivesUpAfterAttempts(t *testing.T) {
	withQuietLogger(t)
	stub := &n9eStub{statuses: []int{502}}
	cli, _ := newStubClient(t, stub, 3)

	err := cli.getJSON(cli.api("/query", "7"), &promResp{})
	if err == nil {
		t.Fatal("持续 502 应最终返回错误")
	}
	if stub.callCount() != 3 {
		t.Errorf("应尝试 3 次后放弃，实际 %d 次", stub.callCount())
	}
	if !strings.Contains(err.Error(), "HTTP 502") {
		t.Errorf("错误应保留状态码信息，实得：%v", err)
	}
	if !strings.Contains(err.Error(), "已重试 2 次") {
		t.Errorf("错误应标注已重试次数，实得：%v", err)
	}
}

func TestGetJSONNoRetryOnClientError(t *testing.T) {
	withQuietLogger(t)
	stub := &n9eStub{statuses: []int{400}, body: "bad query"}
	cli, _ := newStubClient(t, stub, 4)

	err := cli.getJSON(cli.api("/query", "7"), &promResp{})
	if err == nil {
		t.Fatal("400 应返回错误")
	}
	if stub.callCount() != 1 {
		t.Errorf("400 属非瞬时错误，不应重试，实际请求 %d 次", stub.callCount())
	}
	if strings.Contains(err.Error(), "已重试") {
		t.Errorf("未发生重试时不应标注已重试，实得：%v", err)
	}
}

func TestGetJSONRetriesTimeout(t *testing.T) {
	withQuietLogger(t)
	// 服务端耗时 200ms > 客户端 30ms 超时 → 触发 "timeout awaiting response headers" 类错误
	stub := &n9eStub{statuses: []int{200, 200, 200}, delay: 200 * time.Millisecond}
	srv := httptest.NewServer(http.HandlerFunc(stub.handler))
	defer srv.Close()
	cli := NewN9EClient(srv.URL, "7", "", "", "", 30*time.Millisecond, false)
	cli.SetRetry(3, time.Millisecond)

	if err := cli.getJSON(cli.api("/query", "7"), &promResp{}); err == nil {
		t.Fatal("持续超时应返回错误")
	}
	if stub.callCount() != 3 {
		t.Errorf("超时应重试到次数用尽（3 次），实际 %d 次", stub.callCount())
	}
}

func TestGetJSONRetriesNonJSONBody(t *testing.T) {
	withQuietLogger(t)
	// 200 但正文是网关错误页 / 首页 HTML（n9e 网关被拦时会出现），属瞬时
	stub := &n9eStub{statuses: []int{200, 200}, body: "<html>Copyright 2022 Nightingale Team</html>"}
	cli, _ := newStubClient(t, stub, 3)

	err := cli.getJSON(cli.api("/query", "7"), &promResp{})
	if err == nil {
		t.Fatal("正文非 JSON 应返回错误")
	}
	if stub.callCount() != 3 {
		t.Errorf("正文解析失败应重试到次数用尽（3 次），实际 %d 次", stub.callCount())
	}
	if !strings.Contains(err.Error(), "响应解析失败") {
		t.Errorf("错误应说明是响应解析失败，实得：%v", err)
	}
}

// ---- 行为：开关与上层调用 ----

func TestSetRetryDisables(t *testing.T) {
	withQuietLogger(t)
	stub := &n9eStub{statuses: []int{502}}
	cli, _ := newStubClient(t, stub, 1) // attempts=1 即不重试

	err := cli.getJSON(cli.api("/query", "7"), &promResp{})
	if err == nil {
		t.Fatal("应返回错误")
	}
	if stub.callCount() != 1 {
		t.Errorf("关闭重试后只应请求 1 次，实际 %d 次", stub.callCount())
	}
	if strings.Contains(err.Error(), "已重试") {
		t.Errorf("关闭重试时不应标注已重试，实得：%v", err)
	}
}

func TestDefaultRetryConfig(t *testing.T) {
	cli := NewN9EClient("http://n9e:17000", "7", "", "", "", time.Second, false)
	a, b := cli.retryCfg()
	if a != defaultRetryAttempts {
		t.Errorf("默认重试次数应为 %d，实得 %d", defaultRetryAttempts, a)
	}
	if b != defaultRetryBase {
		t.Errorf("默认退避应为 %v，实得 %v", defaultRetryBase, b)
	}
	// 零值客户端（未走构造函数）也要回落到默认值，避免「静默不重试」
	zero := &N9EClient{}
	if a, b := zero.retryCfg(); a != defaultRetryAttempts || b != defaultRetryBase {
		t.Errorf("零值客户端应回落到默认重试参数，实得 %d/%v", a, b)
	}
	// SetRetry 只覆盖显式给定的项
	cli.SetRetry(2, 0)
	if a, b := cli.retryCfg(); a != 2 || b != defaultRetryBase {
		t.Errorf("SetRetry(2,0) 应只改次数，实得 %d/%v", a, b)
	}
}

// TestQueryRangeRetrySurvivesGatewayFlap 上层调用：网关抖一下就成功的场景下，
// QueryRange 应照常返回数据（而不是把错误抛给调用方导致报告缺失资源章节）。
func TestQueryRangeRetrySurvivesGatewayFlap(t *testing.T) {
	withQuietLogger(t)
	stub := &n9eStub{statuses: []int{502, 503, 200}}
	cli, _ := newStubClient(t, stub, 4)

	series, err := cli.QueryRange("avg by (ident) (cpu_usage_active)", 1758000000, 1758003000, 300)
	if err != nil {
		t.Fatalf("网关抖动后重试应成功，实际报错：%v", err)
	}
	if len(series) != 1 {
		t.Fatalf("应返回 1 条序列，实际 %d 条", len(series))
	}
	if got := series[0].Metric["ident"]; got == "" {
		t.Error("应解析出 ident 标签")
	}
	if stub.callCount() != 3 {
		t.Errorf("应共请求 3 次，实际 %d 次", stub.callCount())
	}
}

// promResp 与 QueryInstant / QueryRange 内部使用的匿名结构同形，
// 便于测试直接断言解码结果（JSON 解析只认字段名，不依赖具体类型）。
type promResp struct {
	Status string `json:"status"`
	Data   struct {
		Result []promSeries `json:"result"`
	} `json:"data"`
}

// TestReadN9ERetrySurvivesGatewayFlap 复现线上故障并验证修复：
// 采集是「任一查询失败即整段退出」的，线上那个 502 恰好落在第 2 个查询
// （cpu_usage_active）上，于是整份报告缺失资源巡检数据。
// 这里让桩服务对前两次请求返回 502（正文用线上网关的实际形态），
// 先断言「关掉重试必然失败」（证明该用例确实复现了故障），
// 再断言「开启重试后 ReadN9E 能取回完整数据（含 OS / 架构 / agent 版本元数据）」。
func TestReadN9ERetrySurvivesGatewayFlap(t *testing.T) {
	withQuietLogger(t)

	// 第一段：不重试 → 必然失败（复现线上「一次抖动丢整段数据」）
	stub1, calls1 := newFlakyN9E(t, 1)
	if _, err := ReadN9E(stub1, 1758000000, 1758003600, 300, IdentOpts{Layout: "auto"}); err == nil {
		t.Fatal("关闭重试时，首次 502 应直接失败（否则本用例没有复现故障）")
	}
	if n := atomic.LoadInt32(calls1); n != 1 {
		t.Errorf("关闭重试时只应请求 1 次，实际 %d 次", n)
	}

	// 第二段：允许重试 → 应自动恢复
	cli, calls2 := newFlakyN9E(t, 4)
	rows, err := ReadN9E(cli, 1758000000, 1758003600, 300, IdentOpts{Layout: "auto"})
	if err != nil {
		t.Fatalf("前两次 502 后应重试成功，实际报错：%v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("应取回 1 台主机，实际 %d 台", len(rows))
	}
	r := rows[0]
	if r.IP != "10.0.0.1" || r.Project != "测试项目" || r.Role != "应用服务器" {
		t.Errorf("ident 解析结果不符：%+v", r)
	}
	// 元数据（system_info / categraf_info）同样走 getJSON，重试对它们一并生效
	if r.OS == "" || r.AgentVer == "" {
		t.Errorf("应取到操作系统与 agent 版本，实际 OS=%q AgentVer=%q", r.OS, r.AgentVer)
	}
	if r.Arch != "amd64" {
		t.Errorf("应从容内核串解析出 amd64，实际 %q", r.Arch)
	}
	if n := atomic.LoadInt32(calls2); n <= flakyFailFirst+1 {
		t.Errorf("应发生重试（请求数 > %d），实际 %d 次", flakyFailFirst+1, n)
	}
}

const (
	flakyIdent     = "000042-10.0.0.1-测试项目-应用服务器"
	flakyFailFirst = 2 // 前 2 次请求返回 502，对齐线上「第 2 个查询 502」的情形
)

// newFlakyN9E 起一个「前 flakyFailFirst 次请求返回 502，之后正常」的桩服务。
// attempts 为客户端重试总次数（1=不重试）。
func newFlakyN9E(t *testing.T, attempts int) (*N9EClient, *int32) {
	t.Helper()
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(atomic.AddInt32(&calls, 1))
		if n <= flakyFailFirst {
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprint(w, "net/http: timeout awaiting response headers")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/label/ident/values") {
			fmt.Fprintf(w, `{"status":"success","data":[%q]}`, flakyIdent)
			return
		}
		// 各查询共用一条序列即可满足解析：cores/cpu/... 取同一个值，
		// system_info / categraf_info 的标签也一并给出，便于校验元数据链路
		fmt.Fprintf(w, `{"status":"success","data":{"result":[{"metric":{"ident":%q,`+
			`"host_ip":"10.0.0.1","os_name":"Kylin","os_version":"V10",`+
			`"kernel_version":"4.19.90-52.22.v2207.ky10.x86_64","version":"v0.4.36-84dcda76"},`+
			`"values":[[1758000000,"8"]]}]}}`, flakyIdent)
	}))
	t.Cleanup(srv.Close)

	cli := NewN9EClient(srv.URL, "42", "", "", "", 5*time.Second, false)
	cli.SetRetry(attempts, time.Millisecond)
	return cli, &calls
}

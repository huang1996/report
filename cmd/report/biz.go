package main

// biz.go —— 业务巡检：应用探活（http_response）与数据库 / 缓存 / 中间件实例指标采集
//
// 数据口径与报告版式约定：
//   · 实例命名：<业务系统简称>-<中间件类型>-<服务地址>（categraf 的 instance 标签）
//   · 每个组件类型先给「关键指标表」（一行一个实例），再对每个实例逐个做详细分析
//   · 状态只有三档：正常 / 关注 / 告警，取各项指标中最差的一项
//   · 采集失败或该类型无数据时整体跳过，不产出空表

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	bizNormal = "正常"
	bizWarn   = "关注"
	bizAlert  = "告警"

	// bizCertWarnDays 证书剩余有效期阈值：＜30 天标为关注，已过期标为告警
	bizCertWarnDays = 30
)

// BizInstance 由 instance 标签拆出的三元组
type BizInstance struct {
	Raw    string // 原始 instance
	System string // 业务系统简称
	Type   string // 中间件类型（与 kind 一致时以 kind 展示名为准）
	Addr   string // 服务地址（报告展示时截为 host:port）
}

// parseBizInstance 按「业务系统简称-中间件类型-服务地址」拆分。
// 业务系统简称与类型名都不允许含 "-"，地址部分允许（如 IP:port、域名带连字符）。
func parseBizInstance(s string) BizInstance {
	s = strings.TrimSpace(s)
	inst := BizInstance{Raw: s, System: s, Type: "—"}
	parts := strings.SplitN(s, "-", 3)
	switch len(parts) {
	case 3:
		inst.System, inst.Type, inst.Addr = parts[0], parts[1], parts[2]
	case 2:
		inst.System, inst.Type = parts[0], parts[1]
	}
	return inst
}

// BizRow 一个指标行
type BizRow struct {
	Label  string
	Value  string
	Peak   string
	Thresh string
	Status string
	Key    bool
}

// BizComponent 一个中间件 / 数据库实例
type BizComponent struct {
	Kind     string // 规范化类型（mysql / postgresql / redis …）
	Name     string // 展示名（MySQL / PostgreSQL / Redis …）
	Category string // 数据库 / 缓存 / 中间件
	Inst     BizInstance
	Rows     []BizRow
	Status   string
	Charts   []BizChart // 实例趋势图（按类型定制，渲染在该实例的详细分析之后）
}

// BizChartSeries 趋势图中的一条序列
type BizChartSeries struct {
	Name   string
	Axis   int // 0 左轴（默认）/ 1 右轴
	Fill   bool
	Points []BizPoint
}

// BizChart 一个实例的趋势图（一张图内可含多序列、双轴、堆叠填充）
type BizChart struct {
	Title       string
	YLabel      string  // 左轴单位
	YLabelRight string  // 右轴单位（空 = 单轴）
	YMax        float64 // 左轴上限，0 = 按数据自动
	YMaxRight   float64 // 右轴上限，0 = 按数据自动
	WarnLine    float64 // 阈值线，0 = 不画
	WarnAxis    int     // 阈值线所在轴
	WarnText    string
	Stacked     bool // 是否堆叠填充
	Series      []BizChartSeries
}

// KeyRows 取关键指标行（按配置顺序）
func (c *BizComponent) KeyRows() []BizRow {
	var out []BizRow
	for _, r := range c.Rows {
		if r.Key {
			out = append(out, r)
		}
	}
	return out
}

// BizPoint 响应时间采样点
type BizPoint struct {
	TS int64
	V  float64
}

// BizProbe 一个应用探活目标
type BizProbe struct {
	System     string
	Name       string // name-desc
	Addr       string
	Avail      float64
	Avg        float64
	Max        float64
	Std        float64
	Samples    int
	CertExpire int64 // HTTPS 证书到期时间（Unix 秒），0 = 该目标无证书信息（http / 插件未采集）
	CertDays   int   // 距报告期末的剩余天数，负值表示已过期
	Status     string
	Points     []BizPoint // 响应时间轨迹，用于折线图（已下采样）
}

// BizFinding 巡检发现
type BizFinding struct {
	Desc   string
	Basis  string
	Advice string
	Level  string
}

// BizData 业务巡检全量数据
type BizData struct {
	Probes   []BizProbe
	Comps    []BizComponent
	Findings []BizFinding
}

// HasData 是否采集到任何业务巡检数据（探活或中间件）
func (b *BizData) HasData() bool {
	return b != nil && (len(b.Probes) > 0 || len(b.Comps) > 0)
}

// Systems 返回涉及的业务系统（去重排序）
func (b *BizData) Systems() []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	for _, p := range b.Probes {
		add(p.System)
	}
	for _, c := range b.Comps {
		add(c.Inst.System)
	}
	sort.Strings(out)
	return out
}

// CompsByKind 取某类型的组件（按 instance 排序）
func (b *BizData) CompsByKind(kind string) []BizComponent {
	var out []BizComponent
	for _, c := range b.Comps {
		if c.Kind == kind {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Status != out[j].Status {
			return bizRank(out[i].Status) < bizRank(out[j].Status)
		}
		return out[i].Inst.Raw < out[j].Inst.Raw
	})
	return out
}

// bizRank 状态排序权重：告警在最前
func bizRank(s string) int {
	switch s {
	case bizAlert:
		return 0
	case bizWarn:
		return 1
	}
	return 2
}

// bizWorst 取最差状态
func bizWorst(statuses ...string) string {
	w := bizNormal
	for _, s := range statuses {
		if bizRank(s) < bizRank(w) {
			w = s
		}
	}
	return w
}

// ---------------- 指标格式化 ----------------

func fPct(v float64) string  { return sprintf("%.2f%%", v) }
func fPct1(v float64) string { return sprintf("%.1f%%", v) }

// fPctSmall 极小的百分比保留三位小数（如 Redis 占系统内存 0.003%）
func fPctSmall(v float64) string {
	if v > 0 && v < 0.01 {
		return sprintf("%.3f%%", v)
	}
	return sprintf("%.2f%%", v)
}
func fNum2(v float64) string { return sprintf("%.2f", v) }
func fNum3(v float64) string { return sprintf("%.3f", v) }
func fNum4(v float64) string { return sprintf("%.4f", v) }
func fNum1(v float64) string { return sprintf("%.1f", v) }
func fNum0(v float64) string { return sprintf("%.0f", v) }
func fInt(v float64) string  { return comma(int64(math.Round(v))) }
func fMs(v float64) string   { return sprintf("%.0f ms", v) }
func fUs(v float64) string   { return sprintf("%.0f 微秒", v) }
func fDay(v float64) string {
	if v <= 0 {
		return "—"
	}
	if v < 1 {
		return sprintf("%.1f 小时", v*24)
	}
	return sprintf("%.1f 天", v)
}
func fBytes(v float64) string {
	switch {
	case v >= 1<<30:
		return sprintf("%.2f GB", v/(1<<30))
	case v >= 1<<20:
		return sprintf("%.2f MB", v/(1<<20))
	case v >= 1<<10:
		return sprintf("%.1f KB", v/(1<<10))
	}
	return sprintf("%.0f B", v)
}
func fBytesRate(v float64) string { return fBytes(v) + "/s" }

// bizCertDate 证书到期日（本地时区，只到日）
func bizCertDate(ts int64) string {
	if ts <= 0 {
		return "—"
	}
	return time.Unix(ts, 0).Format("2006-01-02")
}

// ---------------- 状态判定 ----------------

// judgeAt 越大越差：≥warn 关注，≥alert 告警
func judgeAt(alert, warn float64) func(float64) string {
	return func(v float64) string {
		if v >= alert {
			return bizAlert
		}
		if v >= warn {
			return bizWarn
		}
		return bizNormal
	}
}

// judgeBelow 越小越差：<warn 关注，<alert 告警
func judgeBelow(alert, warn float64) func(float64) string {
	return func(v float64) string {
		if v < alert {
			return bizAlert
		}
		if v < warn {
			return bizWarn
		}
		return bizNormal
	}
}

// judgeAny 只要大于 0 就关注（死锁、慢查询、拒绝连接等）
func judgeAny(v float64) string {
	if v > 0 {
		return bizWarn
	}
	return bizNormal
}

// judgeAnyAlert 大于 0 直接告警（拒绝连接、持久化异常）
func judgeAnyAlert(v float64) string {
	if v > 0 {
		return bizAlert
	}
	return bizNormal
}

// judgeNone 仅作趋势观察
func judgeNone(float64) string { return bizNormal }

// ---------------- 指标定义 ----------------

type bizMetric struct {
	Label   string
	Expr    string // 含 {{inst}} 占位，替换为 instance 值
	Fmt     func(float64) string
	Thresh  string
	Judge   func(float64) string
	Key     bool // 是否进关键指标表
	NoPeak  bool // 状态 / 计数类指标不展示峰值
	Instant bool // 取瞬时值（gauge 当前值）
	// PairExpr 双计数器增量比值：按周期首末样本增量计算（如命中率 = Δhits/(Δhits+Δmisses)），
	// 比逐点 rate 比值取平均更贴近周期整体口径
	PairExpr [2]string
}

type bizKind struct {
	ID       string // mysql
	Name     string // MySQL
	Category string // 数据库 / 缓存 / 中间件
	Discover string // 用于发现实例的指标（取 instance 标签）
	Metrics  []bizMetric
}

func inst(metric string) string { return metric + `{instance="{{inst}}"}` }

// bizKinds 业务巡检支持的组件类型，顺序即报告中的出现顺序。
// 新增类型只需在此追加一段配置：发现指标 + 指标定义（表达式 / 阈值 / 判定）。
var bizKinds = []bizKind{
	{
		ID: "mysql", Name: "MySQL", Category: "数据库", Discover: "mysql_up",
		Metrics: []bizMetric{
			{Label: "连接使用率", Expr: inst("mysql_global_status_threads_connected") + "/" + inst("mysql_global_variables_max_connections") + "*100",
				Fmt: fPct, Thresh: "＜ 80%", Judge: judgeAt(90, 80), Key: true},
			{Label: "QPS", Expr: `rate(mysql_global_status_queries{instance="{{inst}}"}[5m])`,
				Fmt: fNum2, Thresh: "趋势观察", Judge: judgeNone, Key: true},
			{Label: "慢查询（/s）", Expr: `rate(mysql_global_status_slow_queries{instance="{{inst}}"}[5m])`,
				Fmt: fNum2, Thresh: "= 0", Judge: judgeAny, Key: true},
			{Label: "磁盘临时表（/s）", Expr: `rate(mysql_global_status_created_tmp_disk_tables{instance="{{inst}}"}[5m])`,
				Fmt: fNum2, Thresh: "＜ 1", Judge: judgeAt(2, 1), Key: true},
			{Label: "缓冲池命中率", Expr: inst("mysql_global_status_innodb_buffer_pool_read_requests") + "/(" + inst("mysql_global_status_innodb_buffer_pool_read_requests") + "+" + inst("mysql_global_status_innodb_buffer_pool_reads") + ")*100",
				Fmt: fPct, Thresh: "＞ 95%", Judge: judgeBelow(90, 95), Key: true},
			{Label: "全表扫描（/s）", Expr: `rate(mysql_global_status_select_scan{instance="{{inst}}"}[5m])`,
				Fmt: fNum2, Thresh: "趋势观察", Judge: judgeNone},
			{Label: "临时表落盘占比", Expr: `rate(mysql_global_status_created_tmp_disk_tables{instance="{{inst}}"}[5m])/(rate(mysql_global_status_created_tmp_tables{instance="{{inst}}"}[5m])+rate(mysql_global_status_created_tmp_disk_tables{instance="{{inst}}"}[5m]))*100`,
				Fmt: fPct1, Thresh: "＜ 10%", Judge: judgeAt(20, 10)},
			{Label: "缓冲池使用率", Expr: inst("mysql_global_status_buffer_pool_bytes_used") + "/" + inst("mysql_global_status_buffer_pool_bytes_total") + "*100",
				Fmt: fPct, Thresh: "—", Judge: judgeNone},
			{Label: "行锁等待（/s）", Expr: `rate(mysql_global_status_innodb_row_lock_waits{instance="{{inst}}"}[5m])`,
				Fmt: fNum3, Thresh: "＜ 0.01", Judge: judgeAt(0.1, 0.01)},
			{Label: "表锁等待（/s）", Expr: `rate(mysql_global_status_table_locks_waited{instance="{{inst}}"}[5m])`,
				Fmt: fNum3, Thresh: "= 0", Judge: judgeAny},
			{Label: "连接失败（/s）", Expr: `rate(mysql_global_status_aborted_connects{instance="{{inst}}"}[5m])`,
				Fmt: fNum4, Thresh: "= 0", Judge: judgeAny},
			{Label: "表缓存命中率", Expr: inst("mysql_global_status_table_open_cache_hits") + "/(" + inst("mysql_global_status_table_open_cache_hits") + "+" + inst("mysql_global_status_table_open_cache_misses") + ")*100",
				Fmt: fPct, Thresh: "＞ 90%", Judge: judgeBelow(80, 90)},
			{Label: "元数据抓取耗时", Expr: `mysql_scrape_use_seconds{instance="{{inst}}"}*1000`,
				Fmt: fMs, Thresh: "＜ 1000 ms", Judge: judgeAt(5000, 1000)},
		},
	},
	{
		ID: "postgresql", Name: "PostgreSQL", Category: "数据库", Discover: "postgresql_up",
		Metrics: []bizMetric{
			{Label: "连接数", Expr: `sum(postgresql_numbackends{instance="{{inst}}"})`,
				Fmt: fNum1, Thresh: "参考最大连接数", Judge: judgeNone, Key: true},
			{Label: "TPS（事务/s）", Expr: `sum(rate(postgresql_xact_commit{instance="{{inst}}"}[5m]))`,
				Fmt: fNum2, Thresh: "趋势观察", Judge: judgeNone, Key: true},
			{Label: "回滚率", Expr: `sum(rate(postgresql_xact_rollback{instance="{{inst}}"}[5m]))/clamp_min(sum(rate(postgresql_xact_commit{instance="{{inst}}"}[5m]))+sum(rate(postgresql_xact_rollback{instance="{{inst}}"}[5m])),1e-6)*100`,
				Fmt: fPct, Thresh: "＜ 1%", Judge: judgeAt(5, 1), Key: true},
			{Label: "缓存命中率", Expr: `sum(rate(postgresql_blks_hit{instance="{{inst}}"}[5m]))/(sum(rate(postgresql_blks_hit{instance="{{inst}}"}[5m]))+sum(rate(postgresql_blks_read{instance="{{inst}}"}[5m])))*100`,
				Fmt: fPct, Thresh: "＞ 95%", Judge: judgeBelow(90, 95), Key: true},
			{Label: "事务空闲占比", Expr: `sum(rate(postgresql_idle_in_transaction_time{instance="{{inst}}"}[5m]))/clamp_min(sum(rate(postgresql_session_time{instance="{{inst}}"}[5m])),1e-6)*100`,
				Fmt: fPct, Thresh: "＜ 20%", Judge: judgeAt(40, 20), Key: true},
			{Label: "死锁（/s）", Expr: `sum(rate(postgresql_deadlocks{instance="{{inst}}"}[5m]))`,
				Fmt: fNum4, Thresh: "= 0", Judge: judgeAny},
			{Label: "冲突（/s）", Expr: `sum(rate(postgresql_conflicts{instance="{{inst}}"}[5m]))`,
				Fmt: fNum4, Thresh: "= 0", Judge: judgeAny},
			{Label: "临时文件（/s）", Expr: `sum(rate(postgresql_temp_files{instance="{{inst}}"}[5m]))`,
				Fmt: fNum4, Thresh: "＜ 0.1", Judge: judgeAt(0.5, 0.1)},
			{Label: "临时写盘", Expr: `sum(rate(postgresql_temp_bytes{instance="{{inst}}"}[5m]))`,
				Fmt: fBytesRate, Thresh: "≈ 0", Judge: judgeNone},
			{Label: "后端刷盘（/s）", Expr: `sum(rate(postgresql_buffers_backend_fsync{instance="{{inst}}"}[5m]))`,
				Fmt: fNum4, Thresh: "= 0", Judge: judgeAny},
			{Label: "元组读取（/s）", Expr: `sum(rate(postgresql_tup_fetched{instance="{{inst}}"}[5m]))`,
				Fmt: fNum1, Thresh: "—", Judge: judgeNone},
			{Label: "元组写入（/s）", Expr: `sum(rate(postgresql_tup_inserted{instance="{{inst}}"}[5m])+rate(postgresql_tup_updated{instance="{{inst}}"}[5m])+rate(postgresql_tup_deleted{instance="{{inst}}"}[5m]))`,
				Fmt: fNum1, Thresh: "—", Judge: judgeNone},
		},
	},
	{
		ID: "redis", Name: "Redis", Category: "缓存", Discover: "redis_up",
		Metrics: []bizMetric{
			{Label: "内存使用", Expr: inst("redis_used_memory"),
				Fmt: fBytes, Thresh: "占系统内存 ＜ 70%", Judge: judgeNone, Key: true},
			{Label: "连接使用率", Expr: inst("redis_connected_clients") + "/" + inst("redis_maxclients") + "*100",
				Fmt: fPct, Thresh: "＜ 80%", Judge: judgeAt(90, 80), Key: true},
			{Label: "OPS", Expr: `rate(redis_total_commands_processed{instance="{{inst}}"}[5m])`,
				Fmt: fNum1, Thresh: "趋势观察", Judge: judgeNone, Key: true},
			{Label: "命中率（周期增量）", PairExpr: [2]string{inst("redis_keyspace_hits"), inst("redis_keyspace_misses")},
				Fmt: fPct1, Thresh: "＞ 80%", Judge: judgeBelow(0, 80), Key: true, NoPeak: true},
			{Label: "内存碎片率", Expr: inst("redis_mem_fragmentation_ratio"),
				Fmt: fNum2, Thresh: "＜ 1.5", Judge: judgeAt(5, 1.5), Key: true},
			{Label: "占系统内存", Expr: inst("redis_used_memory") + "/" + inst("redis_total_system_memory") + "*100",
				Fmt: fPctSmall, Thresh: "＜ 70%", Judge: judgeAt(90, 70)},
			{Label: "键总数", Expr: inst("redis_keyspace_keys"),
				Fmt: fInt, Thresh: "—", Judge: judgeNone, NoPeak: true, Instant: true},
			{Label: "带过期键占比", Expr: inst("redis_keyspace_expires") + "/" + inst("redis_keyspace_keys") + "*100",
				Fmt: fPct, Thresh: "过期占比 ＞ 10%", Judge: judgeBelow(1, 10), NoPeak: true, Instant: true},
			{Label: "平均剩余有效期", Expr: inst("redis_keyspace_avg_ttl") + "/1000/86400",
				Fmt: fDay, Thresh: "—", Judge: judgeNone, NoPeak: true, Instant: true},
			{Label: "过期速率（/s）", Expr: `rate(redis_expired_keys{instance="{{inst}}"}[5m])`,
				Fmt: fNum2, Thresh: "—", Judge: judgeNone},
			{Label: "淘汰速率（/s）", Expr: `rate(redis_evicted_keys{instance="{{inst}}"}[5m])`,
				Fmt: fNum3, Thresh: "= 0", Judge: judgeAny},
			{Label: "拒绝连接（/s）", Expr: `rate(redis_rejected_connections{instance="{{inst}}"}[5m])`,
				Fmt: fNum4, Thresh: "= 0", Judge: judgeAnyAlert},
			{Label: "阻塞客户端", Expr: inst("redis_blocked_clients"),
				Fmt: fNum0, Thresh: "= 0", Judge: judgeAny, NoPeak: true, Instant: true},
			{Label: "事件循环延迟", Expr: inst("redis_instantaneous_eventloop_duration_usec"),
				Fmt: fUs, Thresh: "＜ 5000 微秒", Judge: judgeAt(10000, 5000)},
			{Label: "RDB 状态", Expr: inst("redis_rdb_last_bgsave_status"),
				Fmt: func(v float64) string {
					if v == 1 {
						return "正常"
					}
					return "异常"
				}, Thresh: "= 正常", Judge: judgeBelow(1, 1), NoPeak: true, Instant: true},
			{Label: "AOF 状态", Expr: inst("redis_aof_last_write_status"),
				Fmt: func(v float64) string {
					if v == 1 {
						return "正常"
					}
					return "异常"
				}, Thresh: "= 正常", Judge: judgeBelow(1, 1), NoPeak: true, Instant: true},
			{Label: "从节点数", Expr: inst("redis_connected_slaves"),
				Fmt: fNum0, Thresh: "—", Judge: judgeNone, NoPeak: true, Instant: true},
		},
	},
	{
		ID: "nginx", Name: "Nginx", Category: "中间件", Discover: "nginx_requests",
		Metrics: []bizMetric{
			{Label: "活跃连接", Expr: inst("nginx_active"),
				Fmt: fNum0, Thresh: "趋势观察", Judge: judgeNone, Key: true, NoPeak: true},
			{Label: "请求速率（次/s）", Expr: `rate(nginx_requests{instance="{{inst}}"}[5m])`,
				Fmt: fNum1, Thresh: "趋势观察", Judge: judgeNone, Key: true},
			{Label: "读请求头连接", Expr: inst("nginx_reading"),
				Fmt: fNum0, Thresh: "—", Judge: judgeNone, Key: true, NoPeak: true},
			{Label: "写响应连接", Expr: inst("nginx_writing"),
				Fmt: fNum0, Thresh: "—", Judge: judgeNone, Key: true, NoPeak: true},
			{Label: "空闲连接", Expr: inst("nginx_waiting"),
				Fmt: fNum0, Thresh: "—", Judge: judgeNone, Key: true, NoPeak: true},
			{Label: "已接收连接（累计）", Expr: inst("nginx_accepts"),
				Fmt: fInt, Thresh: "—", Judge: judgeNone},
			{Label: "已处理连接（累计）", Expr: inst("nginx_handled"),
				Fmt: fInt, Thresh: "—", Judge: judgeNone},
		},
	},
	{
		ID: "tomcat", Name: "Tomcat", Category: "中间件", Discover: "tomcat_connector_request_count",
		Metrics: []bizMetric{
			{Label: "请求速率（次/s）", Expr: `rate(tomcat_connector_request_count{instance="{{inst}}"}[5m])`,
				Fmt: fNum1, Thresh: "趋势观察", Judge: judgeNone, Key: true},
			{Label: "繁忙线程数", Expr: inst("tomcat_connector_current_threads_busy"),
				Fmt: fNum0, Thresh: "＜ 最大线程 80%", Judge: judgeNone, Key: true, NoPeak: true},
			{Label: "最大线程数", Expr: inst("tomcat_connector_max_threads"),
				Fmt: fNum0, Thresh: "—", Judge: judgeNone, Key: true, NoPeak: true},
			{Label: "当前线程数", Expr: inst("tomcat_connector_current_threads"),
				Fmt: fNum0, Thresh: "—", Judge: judgeNone, NoPeak: true},
			{Label: "平均处理时间（ms）", Expr: `rate(tomcat_connector_processing_time{instance="{{inst}}"}[5m])/clamp_min(rate(tomcat_connector_request_count{instance="{{inst}}"}[5m]),1e-6)`,
				Fmt: fNum1, Thresh: "＜ 200 ms", Judge: judgeAt(1000, 200)},
			{Label: "错误数（累计）", Expr: inst("tomcat_connector_error_count"),
				Fmt: fInt, Thresh: "= 0", Judge: judgeAny},
		},
	},
	{
		ID: "kafka", Name: "Kafka", Category: "中间件", Discover: "kafka_brokers",
		Metrics: []bizMetric{
			{Label: "在线 broker 数", Expr: inst("kafka_brokers"),
				Fmt: fNum0, Thresh: "＝ 期望节点数", Judge: judgeNone, Key: true, NoPeak: true},
			{Label: "分区总数", Expr: inst("kafka_partitions"),
				Fmt: fNum0, Thresh: "—", Judge: judgeNone, Key: true, NoPeak: true},
			{Label: "副本不足分区", Expr: inst("kafka_partitions_under_replicated"),
				Fmt: fNum0, Thresh: "= 0", Judge: judgeAny, Key: true, NoPeak: true},
			{Label: "离线分区", Expr: inst("kafka_partitions_offline"),
				Fmt: fNum0, Thresh: "= 0", Judge: judgeAny, Key: true, NoPeak: true},
			{Label: "消费组堆积（条）", Expr: inst("kafka_consumergroup_lag"),
				Fmt: fInt, Thresh: "持续增长需关注", Judge: judgeNone, Key: true, NoPeak: true},
			{Label: "消息入速率（条/s）", Expr: `rate(kafka_topic_partition_current_offset{instance="{{inst}}"}[5m])`,
				Fmt: fNum1, Thresh: "趋势观察", Judge: judgeNone},
		},
	},
	{
		ID: "rabbitmq", Name: "RabbitMQ", Category: "中间件", Discover: "rabbitmq_queues",
		Metrics: []bizMetric{
			{Label: "队列数", Expr: inst("rabbitmq_queues"),
				Fmt: fNum0, Thresh: "—", Judge: judgeNone, Key: true, NoPeak: true},
			{Label: "待消费消息（条）", Expr: inst("rabbitmq_queue_messages_ready"),
				Fmt: fInt, Thresh: "趋势观察", Judge: judgeNone, Key: true, NoPeak: true},
			{Label: "未确认消息（条）", Expr: inst("rabbitmq_queue_messages_unacked"),
				Fmt: fInt, Thresh: "趋势观察", Judge: judgeNone, Key: true, NoPeak: true},
			{Label: "消费者数量", Expr: inst("rabbitmq_queue_consumers"),
				Fmt: fNum0, Thresh: "＞ 0", Judge: judgeBelow(1, 1), Key: true, NoPeak: true},
			{Label: "连接数", Expr: inst("rabbitmq_connections"),
				Fmt: fNum0, Thresh: "＜ 上限 80%", Judge: judgeNone, Key: true, NoPeak: true},
			{Label: "文件句柄占用", Expr: inst("rabbitmq_fd_used"),
				Fmt: fInt, Thresh: "＜ 上限 80%", Judge: judgeNone},
			{Label: "内存占用", Expr: inst("rabbitmq_mem_used"),
				Fmt: fBytes, Thresh: "＜ 内存水位", Judge: judgeNone},
			{Label: "磁盘可用", Expr: inst("rabbitmq_disk_free"),
				Fmt: fBytes, Thresh: "＞ 磁盘水位", Judge: judgeNone, NoPeak: true},
		},
	},
	{
		ID: "zookeeper", Name: "ZooKeeper", Category: "中间件", Discover: "zk_up",
		Metrics: []bizMetric{
			{Label: "实例存活", Expr: inst("zk_up"),
				Fmt: func(v float64) string {
					if v >= 1 {
						return "存活"
					}
					return "不可用"
				}, Thresh: "= 存活", Judge: judgeBelow(1, 1), Key: true, NoPeak: true, Instant: true},
			{Label: "客户端连接数", Expr: inst("zk_connections"),
				Fmt: fNum0, Thresh: "趋势观察", Judge: judgeNone, Key: true, NoPeak: true},
			{Label: "平均请求延迟（ms）", Expr: inst("zk_latency"),
				Fmt: fNum1, Thresh: "＜ 50 ms", Judge: judgeAt(200, 50), Key: true},
			{Label: "待处理请求", Expr: inst("zk_outstanding_requests"),
				Fmt: fNum0, Thresh: "＜ 20", Judge: judgeAt(50, 20), Key: true, NoPeak: true},
			{Label: "监听注册数", Expr: inst("zk_watch_count"),
				Fmt: fInt, Thresh: "趋势观察", Judge: judgeNone, Key: true},
			{Label: "事务 ID", Expr: inst("zk_zxid"),
				Fmt: fInt, Thresh: "持续增长", Judge: judgeNone},
		},
	},
	{
		ID: "haproxy", Name: "HAProxy", Category: "中间件", Discover: "haproxy_current_sessions",
		Metrics: []bizMetric{
			{Label: "当前会话", Expr: inst("haproxy_current_sessions"),
				Fmt: fNum0, Thresh: "＜ 会话上限 80%", Judge: judgeNone, Key: true, NoPeak: true},
			{Label: "会话上限", Expr: inst("haproxy_max_sessions"),
				Fmt: fNum0, Thresh: "—", Judge: judgeNone, Key: true, NoPeak: true},
			{Label: "当前排队请求", Expr: inst("haproxy_current_queue"),
				Fmt: fNum0, Thresh: "= 0", Judge: judgeAny, Key: true, NoPeak: true},
			{Label: "请求速率（次/s）", Expr: `rate(haproxy_http_requests_total{instance="{{inst}}"}[5m])`,
				Fmt: fNum1, Thresh: "趋势观察", Judge: judgeNone, Key: true},
			{Label: "响应错误速率（次/s）", Expr: `rate(haproxy_response_errors_total{instance="{{inst}}"}[5m])`,
				Fmt: fNum2, Thresh: "= 0", Judge: judgeAny, Key: true},
			{Label: "连接错误速率（次/s）", Expr: `rate(haproxy_connection_errors_total{instance="{{inst}}"}[5m])`,
				Fmt: fNum2, Thresh: "= 0", Judge: judgeAny},
			{Label: "平均响应时间（秒）", Expr: inst("haproxy_response_time_average"),
				Fmt: fNum2, Thresh: "＜ 0.5 秒", Judge: judgeAt(2, 0.5)},
		},
	},
}

// ---------------- 采集 ----------------

// quoteLabel 转义 PromQL 标签值中的反斜杠与双引号
func quoteLabel(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, `"`, `\"`)
}

// promValue 从 Prometheus 返回的样本值（字符串或数字）解析浮点数
func promValue(v interface{}) (float64, bool) {
	switch x := v.(type) {
	case string:
		f, err := strconv.ParseFloat(x, 64)
		if err != nil {
			return 0, false
		}
		return f, true
	case float64:
		return x, true
	}
	return 0, false
}

// bizSeriesStat 对区间序列求均值与峰值
func bizSeriesStat(series []promSeries) (mean, peak float64, ok bool) {
	var vs []float64
	for _, s := range series {
		for _, v := range s.Values {
			if len(v) < 2 {
				continue
			}
			f, good := promValue(v[1])
			if !good {
				continue
			}
			vs = append(vs, f)
		}
	}
	if len(vs) == 0 {
		return 0, 0, false
	}
	sum := 0.0
	peak = vs[0]
	for _, v := range vs {
		sum += v
		if v > peak {
			peak = v
		}
	}
	return sum / float64(len(vs)), peak, true
}

// bizEval 执行单条指标查询：区间查询取均值/峰值，瞬时查询取当前值
func bizEval(c *N9EClient, expr string, m bizMetric, start, end int64, step int) (mean, peak float64, ok bool) {
	if m.Instant {
		series, err := c.QueryInstant(expr, end)
		if err != nil {
			log.Debugf("业务巡检指标查询失败：%s（%v）", expr, err)
			return 0, 0, false
		}
		if len(series) == 0 {
			return 0, 0, false
		}
		sum, n := 0.0, 0
		for _, s := range series {
			if len(s.Values) == 0 || len(s.Values[0]) < 2 {
				continue
			}
			f, good := promValue(s.Values[0][1])
			if !good {
				continue
			}
			sum += f
			n++
		}
		if n == 0 {
			return 0, 0, false
		}
		v := sum / float64(n)
		return v, v, true
	}
	series, err := c.QueryRange(expr, start, end, step)
	if err != nil {
		log.Debugf("业务巡检指标查询失败：%s（%v）", expr, err)
		return 0, 0, false
	}
	return bizSeriesStat(series)
}

// bizDiscover 按发现指标枚举某类型的实例
func bizDiscover(c *N9EClient, k bizKind, ts int64) []string {
	series, err := c.QueryInstant(k.Discover, ts)
	if err != nil {
		log.Debugf("业务巡检：%s 无实例发现数据（%v）", k.Name, err)
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, s := range series {
		id := strings.TrimSpace(s.Metric["instance"])
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// bizDelta 周期内计数器增量（末样本 − 首样本）
func bizDelta(c *N9EClient, expr string, start, end int64, step int) (float64, bool) {
	series, err := c.QueryRange(expr, start, end, step)
	if err != nil || len(series) == 0 {
		return 0, false
	}
	var vs []float64
	for _, v := range series[0].Values {
		if len(v) < 2 {
			continue
		}
		if f, good := promValue(v[1]); good {
			vs = append(vs, f)
		}
	}
	if len(vs) < 2 {
		return 0, false
	}
	d := vs[len(vs)-1] - vs[0]
	if d < 0 {
		d = 0
	}
	return d, true
}

// bizComponent 采集单个实例的全部指标
func bizComponent(c *N9EClient, k bizKind, raw string, start, end int64, step int) *BizComponent {
	comp := &BizComponent{Kind: k.ID, Name: k.Name, Category: k.Category, Inst: parseBizInstance(raw)}
	var statuses []string
	for _, m := range k.Metrics {
		if m.PairExpr[0] != "" {
			e0 := strings.ReplaceAll(m.PairExpr[0], "{{inst}}", quoteLabel(raw))
			e1 := strings.ReplaceAll(m.PairExpr[1], "{{inst}}", quoteLabel(raw))
			a, ok1 := bizDelta(c, e0, start, end, step)
			b, ok2 := bizDelta(c, e1, start, end, step)
			if !ok1 || !ok2 || a+b <= 0 {
				continue
			}
			v := a / (a + b) * 100
			row := BizRow{Label: m.Label, Value: m.Fmt(v), Peak: "—", Thresh: m.Thresh,
				Status: m.Judge(v), Key: m.Key}
			comp.Rows = append(comp.Rows, row)
			statuses = append(statuses, row.Status)
			continue
		}
		expr := strings.ReplaceAll(m.Expr, "{{inst}}", quoteLabel(raw))
		mean, peak, ok := bizEval(c, expr, m, start, end, step)
		if !ok {
			continue
		}
		row := BizRow{Label: m.Label, Value: m.Fmt(mean), Peak: m.Fmt(peak), Thresh: m.Thresh,
			Status: m.Judge(mean), Key: m.Key}
		if m.NoPeak {
			row.Peak = "—"
		}
		comp.Rows = append(comp.Rows, row)
		statuses = append(statuses, row.Status)
	}
	if len(comp.Rows) == 0 {
		return nil
	}
	comp.Status = bizWorst(statuses...)
	comp.Charts = bizCharts(c, k, raw, start, end, step)
	return comp
}

// bizCharts 采集实例趋势图数据（按组件类型定制；未配置的类型返回 nil）。
//   - MySQL：QPS 与连接使用率（双轴，使用率走右轴并画 80% 关注线）
//   - Redis：吞吐量与内存占用（双轴，内存占用面积填充）
//   - PostgreSQL：各库事务提交速率（按 db 拆序列，堆叠填充）
func bizCharts(c *N9EClient, k bizKind, raw string, start, end int64, step int) []BizChart {
	repl := func(expr string) string { return strings.ReplaceAll(expr, "{{inst}}", quoteLabel(raw)) }
	qr := func(expr string) []promSeries {
		res, err := c.QueryRange(expr, start, end, step)
		if err != nil {
			log.Debugf("业务巡检：%s 趋势图查询失败（%v）", k.Name, err)
			return nil
		}
		return res
	}
	switch k.ID {
	case "mysql":
		qps := qr(repl(`rate(mysql_global_status_queries{instance="{{inst}}"}[5m])`))
		use := qr(repl(inst("mysql_global_status_threads_connected") + "/" +
			inst("mysql_global_variables_max_connections") + "*100"))
		if len(qps) == 0 && len(use) == 0 {
			return nil
		}
		return []BizChart{{
			Title:       "QPS 与连接使用率趋势",
			YLabel:      "次/s（QPS）",
			YLabelRight: "%（连接使用率）",
			YMaxRight:   100, // 固定 0~100%，与 80% 关注线同屏才能看出余量
			WarnLine:    80,
			WarnAxis:    1,
			WarnText:    "关注线 80%",
			Series: []BizChartSeries{
				{Name: "QPS", Points: bizPoints(qps)},
				{Name: "连接使用率", Axis: 1, Points: bizPoints(use)},
			},
		}}

	case "redis":
		ops := qr(repl(`rate(redis_total_commands_processed{instance="{{inst}}"}[5m])`))
		mem := qr(repl(inst("redis_used_memory") + "/1024/1024"))
		if len(ops) == 0 && len(mem) == 0 {
			return nil
		}
		return []BizChart{{
			Title:       "吞吐量与内存占用趋势",
			YLabel:      "次/s（吞吐量）",
			YLabelRight: "MB（内存占用）",
			Series: []BizChartSeries{
				{Name: "吞吐量", Points: bizPoints(ops)},
				{Name: "内存占用", Axis: 1, Fill: true, Points: bizPoints(mem)},
			},
		}}

	case "postgresql":
		// 一条查询取全部库：sum by (db) 让每个库独立成序列，时间点天然对齐，便于堆叠
		series := qr(repl(`sum by (db) (rate(postgresql_xact_commit{instance="{{inst}}"}[5m]))`))
		if len(series) == 0 {
			return nil
		}
		// 按周期总量降序：量大的库画在底部，堆叠图更稳
		totals := make([]float64, len(series))
		for i, sr := range series {
			for _, v := range sr.Values {
				if len(v) < 2 {
					continue
				}
				if f, good := promValue(v[1]); good {
					totals[i] += f
				}
			}
		}
		order := make([]int, len(series))
		for i := range order {
			order[i] = i
		}
		sort.SliceStable(order, func(a, b int) bool { return totals[order[a]] > totals[order[b]] })
		ordered := make([]promSeries, 0, len(order))
		for _, oi := range order {
			ordered = append(ordered, series[oi])
		}
		pts := bizPointsAligned(ordered, 400)
		var bs []BizChartSeries
		for i, sr := range ordered {
			name := strings.TrimSpace(sr.Metric["db"])
			if name == "" {
				name = "默认库"
			}
			// template0 / template1 是建库模板，日常无业务事务，留在图例里只会增加噪音
			if strings.HasPrefix(name, "template") {
				continue
			}
			if i >= len(pts) || len(pts[i]) == 0 {
				continue
			}
			bs = append(bs, BizChartSeries{Name: name, Fill: true, Points: pts[i]})
		}
		if len(bs) == 0 {
			return nil
		}
		return []BizChart{{
			Title:   "各库事务提交速率趋势",
			YLabel:  "事务/s",
			Stacked: true,
			Series:  bs,
		}}
	}
	return nil
}

// bizPointsAligned 多序列按同一批下标抽样，保证堆叠时各序列时间点对齐
func bizPointsAligned(series []promSeries, maxPoints int) [][]BizPoint {
	if len(series) == 0 {
		return nil
	}
	n := -1
	for _, s := range series {
		if n < 0 || len(s.Values) < n {
			n = len(s.Values)
		}
	}
	if n <= 0 {
		return nil
	}
	step := 1
	if n > maxPoints {
		step = n/maxPoints + 1
	}
	idxs := make([]int, 0, n/step+2)
	for i := 0; i < n; i += step {
		idxs = append(idxs, i)
	}
	if idxs[len(idxs)-1] != n-1 {
		idxs = append(idxs, n-1)
	}
	out := make([][]BizPoint, len(series))
	for si, s := range series {
		pts := make([]BizPoint, 0, len(idxs))
		for _, i := range idxs {
			v := s.Values[i]
			if len(v) < 2 {
				continue
			}
			ts, ok1 := promValue(v[0])
			val, ok2 := promValue(v[1])
			if !ok1 || !ok2 {
				continue
			}
			pts = append(pts, BizPoint{TS: int64(ts), V: val})
		}
		out[si] = pts
	}
	return out
}

// collectBizProbes 采集应用探活（http_response）
func collectBizProbes(c *N9EClient, start, end int64, step int) []BizProbe {
	series, err := c.QueryRange("http_response_response_code", start, end, step)
	if err != nil {
		log.Debugf("应用探活数据获取失败：%v", err)
		return nil
	}
	certs := collectBizCerts(c, start, end, step)
	var out []BizProbe
	seen := map[string]bool{}
	for _, s := range series {
		target := strings.TrimSpace(s.Metric["target"])
		if target == "" || seen[target] {
			continue
		}
		seen[target] = true

		codeVals := make([]float64, 0, len(s.Values))
		for _, v := range s.Values {
			if len(v) < 2 {
				continue
			}
			if f, good := promValue(v[1]); good {
				codeVals = append(codeVals, f)
			}
		}
		if len(codeVals) == 0 {
			continue
		}
		good := 0
		for _, code := range codeVals {
			if code >= 200 && code < 400 {
				good++
			}
		}
		p := BizProbe{
			System:  strings.TrimSpace(s.Metric["name"]),
			Name:    strings.TrimSpace(s.Metric["desc"]),
			Addr:    target,
			Avail:   float64(good) / float64(len(codeVals)) * 100,
			Samples: len(codeVals),
		}
		if p.Name == "" {
			p.Name = target
		}
		if p.System == "" {
			p.System = "—"
		}
		// 证书到期时间只在 https 目标上有输出；按报告期末算剩余天数，供表格与状态判定使用
		if ts, ok := certs[target]; ok && ts > 0 {
			p.CertExpire = ts
			p.CertDays = int(math.Floor(float64(ts-end) / 86400))
		}

		rt, err := c.QueryRange(sprintf(`http_response_response_time_ms{target="%s"}`, quoteLabel(target)), start, end, step)
		if err == nil {
			if mean, peak, ok := bizSeriesStat(rt); ok {
				p.Avg, p.Max = mean, peak
				p.Std = bizStd(rt)
			}
			p.Points = bizPoints(rt)
		}
		p.Status = bizProbeStatus(p)
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Status != out[j].Status {
			return bizRank(out[i].Status) < bizRank(out[j].Status)
		}
		return out[i].Avg > out[j].Avg
	})
	return out
}

// collectBizCerts 采集 HTTPS 证书到期时间（categraf http_response 的独立指标，仅 https 目标输出）。
// 指标不存在（全为 http 目标或插件版本较旧）时返回空表，报告中相应列自动隐藏。
func collectBizCerts(c *N9EClient, start, end int64, step int) map[string]int64 {
	out := map[string]int64{}
	series, err := c.QueryRange("http_response_cert_expire_timestamp", start, end, step)
	if err != nil {
		log.Debugf("证书到期时间获取失败：%v", err)
		return out
	}
	for _, s := range series {
		target := strings.TrimSpace(s.Metric["target"])
		if target == "" {
			continue
		}
		for _, v := range s.Values { // 取最后一条有效采样
			if len(v) < 2 {
				continue
			}
			if ts, ok := promValue(v[1]); ok && ts > 0 {
				out[target] = int64(ts)
			}
		}
	}
	return out
}

// bizPoints 提取采样点并限制数量（图表最多画 400 点，超出按等间隔抽样）
func bizPoints(series []promSeries) []BizPoint {
	if len(series) == 0 {
		return nil
	}
	var pts []BizPoint
	for _, v := range series[0].Values {
		if len(v) < 2 {
			continue
		}
		ts, ok1 := promValue(v[0])
		val, ok2 := promValue(v[1])
		if !ok1 || !ok2 {
			continue
		}
		pts = append(pts, BizPoint{TS: int64(ts), V: val})
	}
	if len(pts) > 400 {
		step := len(pts)/400 + 1
		var out []BizPoint
		for i := 0; i < len(pts); i += step {
			out = append(out, pts[i])
		}
		out = append(out, pts[len(pts)-1])
		pts = out
	}
	return pts
}

// bizStd 计算响应时间标准差（总体标准差）
func bizStd(series []promSeries) float64 {
	var vs []float64
	for _, s := range series {
		for _, v := range s.Values {
			if len(v) < 2 {
				continue
			}
			if f, good := promValue(v[1]); good {
				vs = append(vs, f)
			}
		}
	}
	if len(vs) < 2 {
		return 0
	}
	mean := 0.0
	for _, v := range vs {
		mean += v
	}
	mean /= float64(len(vs))
	sum := 0.0
	for _, v := range vs {
		sum += (v - mean) * (v - mean)
	}
	return math.Sqrt(sum / float64(len(vs)))
}

// bizProbeStatus 探活状态：可用率、平均响应、最大响应、标准差、证书有效期任一项越线即降档
func bizProbeStatus(p BizProbe) string {
	s := bizNormal
	switch {
	case p.Avail < 99.0, p.Max > 5000, p.Avg > 2000:
		s = bizAlert
	case p.Avail < 99.9, p.Avg > 500, p.Max > 2000, p.Std > 200:
		s = bizWarn
	}
	return bizWorst(s, bizProbeCertStatus(p))
}

// bizProbeCertStatus 证书状态：已过期告警，剩余 ＜30 天关注，无证书信息视为正常
func bizProbeCertStatus(p BizProbe) string {
	if p.CertExpire <= 0 {
		return bizNormal
	}
	switch {
	case p.CertDays < 0:
		return bizAlert
	case p.CertDays < bizCertWarnDays:
		return bizWarn
	}
	return bizNormal
}

// bizProbeRespBad 响应侧指标（不含证书）是否越线
func bizProbeRespBad(p BizProbe) bool {
	return p.Avail < 99.9 || p.Avg > 500 || p.Max > 2000 || p.Std > 200
}

// CollectBiz 采集业务巡检数据；无任何数据时返回空结构（HasData 为 false）
func CollectBiz(c *N9EClient, start, end int64, step int) *BizData {
	b := &BizData{}
	c.login()
	b.Probes = collectBizProbes(c, start, end, step)
	for _, k := range bizKinds {
		insts := bizDiscover(c, k, end)
		if len(insts) == 0 {
			continue
		}
		if len(insts) > 50 {
			log.Warnf("业务巡检：%s 实例数 %d 超过 50，仅统计前 50 个", k.Name, len(insts))
			insts = insts[:50]
		}
		for _, raw := range insts {
			if comp := bizComponent(c, k, raw, start, end, step); comp != nil {
				b.Comps = append(b.Comps, *comp)
			}
		}
	}
	b.Findings = bizFindings(b)
	log.Infof("业务巡检数据：应用探活 %d 个目标，中间件 / 数据库 %d 个实例",
		len(b.Probes), len(b.Comps))
	return b
}

// bizAdvice 各指标异常时的处置建议
var bizAdvice = map[string]string{
	"连接使用率":       "核查连接池上限与长连接占用，必要时上调最大连接数或引入连接池收敛空闲连接",
	"慢查询（/s）":     "开启慢查询日志定位具体语句，补充缺失索引或改写执行计划",
	"磁盘临时表（/s）":   "优化产生大量临时结果的排序 / 分组 / 子查询，必要时增大 tmp_table_size 与内部临时表内存",
	"缓冲池命中率":      "确认缓冲池容量是否小于热数据量，评估扩容 innodb_buffer_pool_size",
	"回滚率":         "排查应用侧事务边界与异常回滚逻辑，确认失败原因",
	"缓存命中率":       "核对热数据规模与缓存容量，检查是否频繁失效或缓存穿透",
	"事务空闲占比":      "定位长时间不提交的事务，避免长期 idle in transaction 阻塞清理并放大锁等待",
	"内存碎片率":       "开启 activedefrag，必要时择机重启实例回收碎片",
	"带过期键占比":      "推动业务侧为缓存键设置 TTL，避免内存只增不减",
	"行锁等待（/s）":    "定位高并发写入的热点行与长事务，评估拆分或调整事务隔离级别",
	"死锁（/s）":      "结合死锁日志排查事务加锁顺序，统一加锁次序",
	"临时文件（/s）":    "调大 work_mem 或优化排序 / 哈希连接，避免落盘",
	"拒绝连接（/s）":    "检查 maxclients 与连接风暴来源，必要时限流",
	"副本不足分区":      "确认 broker 存活与网络状况，等待 ISR 恢复并排查同步滞后原因",
	"离线分区":        "立即检查 broker 进程与磁盘状态，恢复后可自动重新上线",
	"当前排队请求":      "后端处理能力不足，评估扩容后端或调整 maxconn / 超时配置",
	"响应错误速率（次/s）": "核查后端应用健康状态与错误日志，定位 5xx 来源",
	"实例存活":        "实例不可用，立即检查进程、端口与集群状态",
	"RDB 状态":      "检查持久化失败原因（磁盘空间、fork 失败），确认数据安全",
	"AOF 状态":      "检查 AOF 写入失败原因（磁盘空间、权限），确认数据安全",
	"消费组堆积（条）":    "检查消费端实例与处理速率，必要时扩容消费者或排查阻塞",
}

// bizFindings 由实例明细与探活结果生成巡检发现
func bizFindings(b *BizData) []BizFinding {
	var out []BizFinding
	for _, comp := range b.Comps {
		for _, r := range comp.Rows {
			if r.Status == bizNormal {
				continue
			}
			adv := bizAdvice[r.Label]
			if adv == "" {
				adv = "结合业务峰值与历史基线复核参数配置，必要时扩容"
			}
			out = append(out, BizFinding{
				Desc: sprintf("%s（%s）%s超标", comp.Name, comp.Inst.Raw, r.Label),
				// 描述里已经有指标名，依据只给数值，避免「磁盘临时表 1.13，磁盘临时表 ＞1」式的重复
				Basis:  sprintf("周期均值 %s，参考阈值 %s", r.Value, r.Thresh),
				Advice: adv,
				Level:  r.Status,
			})
		}
	}
	for _, p := range b.Probes {
		// 证书问题单独成条：与响应指标分开描述，便于按证书续签流程处置
		if cs := bizProbeCertStatus(p); cs != bizNormal {
			desc := sprintf("应用探活 %s（%s）HTTPS 证书即将到期", p.Name, bizAddr(p.Addr))
			adv := "联系证书管理员在到期前完成续签与部署，并核对证书链是否完整"
			if p.CertDays < 0 {
				desc = sprintf("应用探活 %s（%s）HTTPS 证书已过期", p.Name, bizAddr(p.Addr))
				adv = "立即更换证书并重启服务，确认浏览器与客户端不再出现证书告警"
			}
			out = append(out, BizFinding{
				Desc:   desc,
				Basis:  sprintf("证书到期 %s，剩余 %d 天", bizCertDate(p.CertExpire), p.CertDays),
				Advice: adv,
				Level:  cs,
			})
		}
		if !bizProbeRespBad(p) {
			continue
		}
		lvl := bizWarn
		if p.Avail < 99.0 || p.Avg > 2000 || p.Max > 5000 {
			lvl = bizAlert
		}
		out = append(out, BizFinding{
			Desc:   sprintf("应用探活 %s（%s）响应异常", p.Name, p.Addr),
			Basis:  sprintf("可用率 %.2f%%，平均响应 %.1f ms，最大响应 %.0f ms，标准差 %.1f ms", p.Avail, p.Avg, p.Max, p.Std),
			Advice: "核查应用服务与网络链路，确认是否存在慢请求、超时或间歇性不可用",
			Level:  lvl,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return bizRank(out[i].Level) < bizRank(out[j].Level) })
	if len(out) > 8 {
		out = out[:8]
	}
	// 全部正常时补一条正向结论
	if len(out) == 0 && (len(b.Comps) > 0 || len(b.Probes) > 0) {
		out = append(out, BizFinding{
			Desc:   "业务组件整体运行正常",
			Basis:  sprintf("%d 个探活目标与 %d 个中间件实例各项指标均在参考阈值内", len(b.Probes), len(b.Comps)),
			Advice: "保持现有部署与监控策略",
			Level:  bizNormal,
		})
	}
	return out
}

// 供测试与日志使用：把时间窗格式化成可读文本
func bizWindow(start, end int64) string {
	return time.Unix(start, 0).Format("2006-01-02 15:04") + " ~ " + time.Unix(end, 0).Format("2006-01-02 15:04")
}

// demoBiz 内置业务巡检样例数据（-demo），用于本地验证章节版式
func demoBiz() *BizData {
	b := &BizData{
		Probes: []BizProbe{
			{System: "天空地森林防火", Name: "门户网站首页", Addr: "https://www.lzfire.gov.cn/portal", Avail: 100, Avg: 46.8, Max: 128, Std: 9.6, Samples: 528,
				CertExpire: time.Now().AddDate(0, 0, 12).Unix(), CertDays: 12, Points: demoBizPoints(46.8, 9.0, 5)},
			{System: "天空地森林防火", Name: "统一认证中心", Addr: "https://sso.lzfire.gov.cn", Avail: 100, Avg: 24.5, Max: 63, Std: 3.8, Samples: 527,
				CertExpire: time.Now().AddDate(0, 0, 186).Unix(), CertDays: 186, Points: demoBizPoints(24.5, 4.5, 2)},
			{System: "天空地森林防火", Name: "GeoServer", Addr: "http://10.82.1.2:28070/geoserver/web", Avail: 100, Avg: 31.4, Max: 71, Std: 5.9, Samples: 528,
				Status: bizNormal, Points: demoBizPoints(31.4, 6.0, 0)},
			{System: "天空地森林防火", Name: "业务系统后端Nginx访问", Addr: "http://10.82.1.2:9527/login", Avail: 100, Avg: 7.2, Max: 19, Std: 1.9, Samples: 525,
				Status: bizNormal, Points: demoBizPoints(7.2, 1.9, 3)},
		},
		Comps: []BizComponent{
			demoBizComp("mysql", "MySQL", "数据库", "天空地森林防火-mysql-10.82.1.2:13306", []BizRow{
				{Label: "连接使用率", Value: "7.29%", Peak: "7.95%", Thresh: "＜ 80%", Status: bizNormal, Key: true},
				{Label: "QPS", Value: "2.06", Peak: "2.16", Thresh: "趋势观察", Status: bizNormal, Key: true},
				{Label: "慢查询（/s）", Value: "0.00", Peak: "0.00", Thresh: "= 0", Status: bizNormal, Key: true},
				{Label: "磁盘临时表（/s）", Value: "1.13", Peak: "1.16", Thresh: "＜ 1", Status: bizWarn, Key: true},
				{Label: "缓冲池命中率", Value: "100.00%", Peak: "100.00%", Thresh: "＞ 95%", Status: bizNormal, Key: true},
				{Label: "全表扫描（/s）", Value: "0.53", Peak: "0.54", Thresh: "趋势观察", Status: bizNormal},
				{Label: "行锁等待（/s）", Value: "0.003", Peak: "0.020", Thresh: "＜ 0.01", Status: bizNormal},
				{Label: "表缓存命中率", Value: "92.68%", Peak: "—", Thresh: "＞ 90%", Status: bizNormal},
			}),
			demoBizComp("postgresql", "PostgreSQL", "数据库", "天空地森林防火-postgres-10.82.1.2:54321", []BizRow{
				{Label: "连接数", Value: "5.6", Peak: "7.0", Thresh: "参考最大连接数", Status: bizNormal, Key: true},
				{Label: "TPS（事务/s）", Value: "0.57", Peak: "0.68", Thresh: "趋势观察", Status: bizNormal, Key: true},
				{Label: "回滚率", Value: "0.01%", Peak: "—", Thresh: "＜ 1%", Status: bizNormal, Key: true},
				{Label: "缓存命中率", Value: "100.00%", Peak: "—", Thresh: "＞ 95%", Status: bizNormal, Key: true},
				{Label: "事务空闲占比", Value: "0.02%", Peak: "—", Thresh: "＜ 20%", Status: bizNormal, Key: true},
				{Label: "死锁（/s）", Value: "0.0000", Peak: "—", Thresh: "= 0", Status: bizNormal},
				{Label: "临时文件（/s）", Value: "0.0000", Peak: "—", Thresh: "＜ 0.1", Status: bizNormal},
			}),
			demoBizComp("redis", "Redis", "缓存", "天空地森林防火-redis-10.82.1.2:26739", []BizRow{
				{Label: "内存使用", Value: "2.21 MB", Peak: "2.97 MB", Thresh: "占系统内存 ＜ 70%", Status: bizNormal, Key: true},
				{Label: "连接使用率", Value: "1.26%", Peak: "1.55%", Thresh: "＜ 80%", Status: bizNormal, Key: true},
				{Label: "OPS", Value: "4.3", Peak: "7.0", Thresh: "趋势观察", Status: bizNormal, Key: true},
				{Label: "命中率（周期增量）", Value: "91.8%", Peak: "—", Thresh: "＞ 80%", Status: bizNormal, Key: true},
				{Label: "内存碎片率", Value: "4.53", Peak: "5.16", Thresh: "＜ 1.5", Status: bizWarn, Key: true},
				{Label: "带过期键占比", Value: "0.04%", Peak: "—", Thresh: "过期占比 ＞ 10%", Status: bizWarn},
				{Label: "事件循环延迟", Value: "717 微秒", Peak: "—", Thresh: "＜ 5000 微秒", Status: bizNormal},
				{Label: "拒绝连接（/s）", Value: "0.0000", Peak: "—", Thresh: "= 0", Status: bizNormal},
			}),
			demoBizComp("nginx", "Nginx", "中间件", "天空地森林防火-nginx-10.82.1.2:9527", []BizRow{
				{Label: "活跃连接", Value: "525", Peak: "—", Thresh: "趋势观察", Status: bizNormal, Key: true},
				{Label: "请求速率（次/s）", Value: "652.4", Peak: "1173.6", Thresh: "趋势观察", Status: bizNormal, Key: true},
				{Label: "读请求头连接", Value: "1", Peak: "—", Thresh: "—", Status: bizNormal, Key: true},
				{Label: "写响应连接", Value: "10", Peak: "—", Thresh: "—", Status: bizNormal, Key: true},
				{Label: "空闲连接", Value: "242", Peak: "—", Thresh: "—", Status: bizNormal, Key: true},
			}),
		},
	}
	// 状态统一按当前判定规则推导（含证书有效期），避免演示数据与规则脱节
	for i := range b.Probes {
		b.Probes[i].Status = bizProbeStatus(b.Probes[i])
	}
	b.Findings = bizFindings(b)
	return b
}

func demoBizComp(kind, name, cat, instance string, rows []BizRow) BizComponent {
	var sts []string
	for _, r := range rows {
		sts = append(sts, r.Status)
	}
	return BizComponent{Kind: kind, Name: name, Category: cat,
		Inst: parseBizInstance(instance), Rows: rows, Status: bizWorst(sts...),
		Charts: demoBizCharts(kind)}
}

// demoBizCharts 演示用趋势图（与真实采集同结构，用于校验版式）
func demoBizCharts(kind string) []BizChart {
	switch kind {
	case "mysql":
		return []BizChart{{
			Title:       "QPS 与连接使用率趋势",
			YLabel:      "次/s（QPS）",
			YLabelRight: "%（连接使用率）",
			YMaxRight:   100, // 固定 0~100%，与 80% 关注线同屏才能看出余量
			WarnLine:    80,
			WarnAxis:    1,
			WarnText:    "关注线 80%",
			Series: []BizChartSeries{
				{Name: "QPS", Points: demoSeries(2.1, 0.35, 0.1, 0)},
				{Name: "连接使用率", Axis: 1, Points: demoSeries(7.3, 0.6, 0.15, 1)},
			},
		}}
	case "postgresql":
		return []BizChart{{
			Title:   "各库事务提交速率趋势",
			YLabel:  "事务/s",
			Stacked: true,
			Series: []BizChartSeries{
				{Name: "postgres", Fill: true, Points: demoSeries(0.43, 0.12, 0.03, 0)},
				{Name: "ruoyi-vue-pro", Fill: true, Points: demoSeries(0.14, 0.05, 0.01, 2)},
			},
		}}
	case "redis":
		return []BizChart{{
			Title:       "吞吐量与内存占用趋势",
			YLabel:      "次/s（吞吐量）",
			YLabelRight: "MB（内存占用）",
			Series: []BizChartSeries{
				{Name: "吞吐量", Points: demoSeries(4.3, 1.4, 0.5, 1)},
				{Name: "内存占用", Axis: 1, Fill: true, Points: demoSeries(2.21, 0.4, 0.12, 0)},
			},
		}}
	}
	return nil
}

// demoSeries 生成演示轨迹（正弦叠加抖动，负值截零）
func demoSeries(base, amp, jitter float64, phase int) []BizPoint {
	start := time.Date(2026, 10, 19, 12, 0, 0, 0, time.Local).Unix()
	pts := make([]BizPoint, 0, 288)
	for i := 0; i < 288; i++ {
		t := float64(i) / 24.0
		v := base + amp*math.Sin(t*2*math.Pi+float64(phase)) + jitter*math.Sin(t*13+float64(phase)*2)
		if v < 0 {
			v = 0
		}
		pts = append(pts, BizPoint{TS: start + int64(i)*300, V: v})
	}
	return pts
}

// demoBizPoints 生成演示用响应时间轨迹（正弦叠加轻微抖动）
func demoBizPoints(base, amp float64, phase int) []BizPoint {
	start := time.Date(2026, 10, 19, 12, 0, 0, 0, time.Local).Unix()
	pts := make([]BizPoint, 0, 288)
	for i := 0; i < 288; i++ {
		t := float64(i) / 24.0
		v := base + amp*math.Sin(t*2*math.Pi+float64(phase)) + 0.6*math.Sin(t*11+float64(phase))
		if v < 0 {
			v = 0
		}
		pts = append(pts, BizPoint{TS: start + int64(i)*300, V: v})
	}
	return pts
}

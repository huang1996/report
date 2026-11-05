package main

// hostfilter.go —— 服务器巡检排除名单（-exclude_host_ips / EXCLUDE_HOST_IPS）
//
// 用途：某些 IP 是临时机器、测试机或不属于本次巡检范围，需要在生成报告时把它们从
// 「服务器巡检」章节的所有统计中剔除（关键指标概览、资源图表、资源明细、主机归属、
// 性能与网络、磁盘分区，以及由这些数据推出的总体结论）。
//
// 同一份名单也用于业务巡检：落在这些 IP 上的探活目标与中间件实例会被跳过
// （见 filterBizByIPs），避免「服务器章节已经排除、业务章节还把它的问题报出来」。
// WAF 章节不受影响：它统计的是攻击流量，与主机归属无关。

import (
	"net"
	"strings"
)

// validIPs 把配置里的排除项拆成「合法 IP」与「非法项」两部分。
// 只接受标准 IPv4 / IPv6 字面量：写主机名或网段都会带来歧义（同名多机、/24 到底排多少台），
// 发现非法项时由调用方 WARN 提示，而不是默默忽略。
func validIPs(ips []string) (ok, bad []string) {
	seen := map[string]bool{}
	for _, item := range ips {
		s := strings.TrimSpace(item)
		if s == "" {
			continue
		}
		if net.ParseIP(s) == nil {
			bad = append(bad, s)
			continue
		}
		if seen[s] {
			continue
		}
		seen[s] = true
		ok = append(ok, s)
	}
	return ok, bad
}

// excludeHosts 按 IP 列表剔除主机行。
//   - kept：保留的主机行（顺序不变）
//   - hit：实际被剔除的 IP（用于日志与报告脚注，说明排除确实生效）
//   - missing：配置了但本次结果里不存在的主机（多为写错 IP 或该机本期无数据），
//     单独返回以便 WARN，避免用户以为排除生效了其实没有
func excludeHosts(rows []HostRow, ips []string) (kept []HostRow, hit, missing []string) {
	if len(ips) == 0 {
		return rows, nil, nil
	}
	skip := make(map[string]bool, len(ips))
	for _, ip := range ips {
		skip[strings.TrimSpace(ip)] = true
	}
	hitSeen := map[string]bool{}
	for _, r := range rows {
		ip := strings.TrimSpace(r.IP)
		if skip[ip] {
			if !hitSeen[ip] { // 同一 IP 出现多行时只记一次，但每一行都要剔除
				hit = append(hit, ip)
				hitSeen[ip] = true
			}
			continue
		}
		kept = append(kept, r)
	}
	// 未命中的项按配置顺序回填，日志里与用户输入一致
	missSeen := map[string]bool{}
	for _, ip := range ips {
		s := strings.TrimSpace(ip)
		if s != "" && !hitSeen[s] && !missSeen[s] {
			missing = append(missing, s)
			missSeen[s] = true
		}
	}
	return kept, hit, missing
}

// filterBizByIPs 跳过落在排除 IP 上的业务巡检组件：
//   - 探活目标按地址里的主机名匹配（支持 host:port、scheme://host/path 等写法）；
//   - 中间件 / 数据库实例按实例名中的服务地址匹配（<系统>-<类型>-<host:port>）。
//
// 返回被跳过的组件数，供日志说明排除是否真的作用到了业务章节。
// 只按 IP 精确匹配：域名探活与域名实例不受影响，排除名单里的非法项已在 validIPs 阶段过滤。
func filterBizByIPs(b *BizData, ips []string) int {
	if b == nil || len(ips) == 0 {
		return 0
	}
	skip := make(map[string]bool, len(ips))
	for _, ip := range ips {
		skip[ip] = true
	}
	skipped := 0

	probes := b.Probes[:0]
	for _, p := range b.Probes {
		if skip[certHostOf(p.Addr)] {
			skipped++
			continue
		}
		probes = append(probes, p)
	}
	b.Probes = probes

	comps := b.Comps[:0]
	for _, c := range b.Comps {
		if skip[certHostOf(c.Inst.Addr)] {
			skipped++
			continue
		}
		comps = append(comps, c)
	}
	b.Comps = comps
	// 巡检发现是采集阶段预计算的，必须按过滤后的组件重算，
	// 否则风险台账里还会留着「已被排除实例」的条目
	if skipped > 0 {
		b.Findings = bizFindings(b)
	}
	return skipped
}

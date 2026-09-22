package main

// n9e_dedupe_test.go —— 同一主机的历史残留 ident 去重单测
//
// 背景：system_n_cpus 等指标在查询窗口内累计「出现过的」时间序列，
// 运维改过主机名时同一台物理机会同时存在多个 ident（ds39 实测 10.192.197.17 有三条）。
// 去重后每个 IP 只保留一条，且优先级为：system_info 当前快照 > ident 自带 IP 段 > 字符更长。

import "testing"

func TestDedupeByIdent(t *testing.T) {
	// 全部取自 ds39 线上真实数据（见 2026-09-22 排查记录）
	live := map[string]bool{
		"000026-10.192.197.17-公共信用信息共享平台-信用一期-前置机1": true,
		"000026-10.81.20.19-公共信用信息共享平台-六库建设1":       true,
	}
	hostIP := map[string]string{
		// system_info 里同 IP 只报现行 ident
		"000026-10.192.197.17-公共信用信息共享平台-信用一期-前置机1": "10.192.197.17",
		"000026-10.192.197.17-信用一期-前置机1":            "10.192.197.17",
		"000026-10.192.197.17公共信用信息共享平台-信用一期-前置机1":  "10.192.197.17",
		"000026-10.81.20.19-公共信用信息共享平台-六库建设1":       "10.81.20.19",
		"000026-10.81.20.19-六库建设1":                  "10.81.20.19",
		"000026-10.82.3.142-公共信用信息共享平台-信用泸州官网-waf":  "10.82.3.142",
		"000026-10.82.3.142-公共信用信息平台-WAF":           "10.82.3.142",
	}
	in := []string{
		"000026-10.192.197.17-信用一期-前置机1",            // 旧命名，非 live
		"000026-10.192.197.17公共信用信息共享平台-信用一期-前置机1",  // IP 与项目名粘连，非 live
		"000026-10.192.197.17-公共信用信息共享平台-信用一期-前置机1", // live，应胜出
		"000026-10.81.20.19-六库建设1",                  // 旧命名，非 live
		"000026-10.81.20.19-公共信用信息共享平台-六库建设1",       // live，应胜出
		"000026-10.82.3.142-公共信用信息平台-WAF",           // 旧命名（含大写 WAF），非 live
		"000026-10.82.3.142-公共信用信息共享平台-信用泸州官网-waf",  // 非 live 但更长，应胜出
		"公共信用信息共享平台-信用一期-前置机1",                      // 无 IP、无 host_ip、不在快照 → 残留，丢弃
		"公共信用信息共享平台-信用泸州官网-waf",                     // 同上，丢弃
	}
	got := dedupeByIdent(in, hostIP, live)

	want := map[string]bool{
		"000026-10.192.197.17-公共信用信息共享平台-信用一期-前置机1": true,
		"000026-10.81.20.19-公共信用信息共享平台-六库建设1":       true,
		"000026-10.82.3.142-公共信用信息共享平台-信用泸州官网-waf":  true,
	}
	if len(got) != len(want) {
		t.Fatalf("去重后条数 = %d（%v），期望 %d 条", len(got), got, len(want))
	}
	for _, id := range got {
		if !want[id] {
			t.Errorf("保留了不该保留的 ident：%q", id)
		}
	}
}

// TestDedupeByIdentDegradesSafely 校验 system_info 不可用（hostIP / live 为空）时
// 退化为仅按 ident 内 IP 段归并、且不丢弃无 IP 段的 ident，避免误删主机。
func TestDedupeByIdentDegradesSafely(t *testing.T) {
	in := []string{
		"000002-10.194.67.194-泸州市环保三级统筹项目-电子签章",
		"000002-10.194.67.194-泸州市环保三级统筹项目-电子签章-张工",
		"000011-192.168.4.6-泸州智慧治理平台", // 无 IP 段，应原样保留
	}
	got := dedupeByIdent(in, nil, nil)
	if len(got) != 2 {
		t.Fatalf("去重后条数 = %d（%v），期望 2 条", len(got), got)
	}
	// 同 IP 的两条保留更长的（含工程师段）
	found := false
	for _, id := range got {
		if id == "000002-10.194.67.194-泸州市环保三级统筹项目-电子签章-张工" {
			found = true
		}
	}
	if !found {
		t.Errorf("同 IP 未保留更完整的 ident，实际保留：%v", got)
	}

	// host_ip 可用但 system_info 快照为空（查询失败）时：无 IP 段的 ident 不可丢弃
	got2 := dedupeByIdent(in, map[string]string{}, map[string]bool{})
	if len(got2) != 2 {
		t.Fatalf("快照不可用时条数 = %d（%v），期望 2 条（不误删无 IP 段主机）", len(got2), got2)
	}
}

// TestIpKeyOf 校验 IP 段提取
func TestIpKeyOf(t *testing.T) {
	cases := map[string]string{
		"000026-10.192.197.17-公共信用信息共享平台-信用一期-前置机1": "10.192.197.17",
		"000011-192.168.4.6-泸州智慧治理平台":               "192.168.4.6",
		"000064-少数民族流动信息化平台-WAF":                    "", // 无 IP 段
		"公共信用信息共享平台-信用一期-前置机1":                      "", // 无 IP 段
		"000026-10.192.197.17公共信用信息共享平台-信用一期-前置机1":  "", // IP 与项目名粘连，切段后不成 IP
	}
	for ident, want := range cases {
		if got := ipKeyOf(ident); got != want {
			t.Errorf("ipKeyOf(%q) = %q，期望 %q", ident, got, want)
		}
	}
}

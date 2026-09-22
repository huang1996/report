package main

// n9e_ident_test.go —— ident 解析单测
//
// 约定：ident 里「角色」与「运维工程师姓名」在字面上无法区分（技术角色词如「数据库」
// 「中间件」「前后端」和姓名如「邹源」「佘发彬」都是 2~4 个纯汉字）。
// 因此**工程师不从 ident 推断**，只认 -report_engineer 手动指定；
// ident 末段一律并入角色，保证信息不丢失。

import "testing"

func TestParseIdentRoleVsEngineer(t *testing.T) {
	// 全部取自线上真实 ident（数据源 54 / 51 / 49 / 58 / 47）
	cases := []struct {
		name  string
		ident string
		want  IdentInfo
	}{
		{
			name:  "纯中文角色不被当成工程师（数据源54 电子签章）",
			ident: "000002-10.194.67.194-泸州市环保三级统筹项目-电子签章",
			want: IdentInfo{Tenant: "000002", IP: "10.194.67.194", Section: "—",
				Project: "泸州市环保三级统筹项目", Role: "电子签章", Engineer: "—"},
		},
		{
			name:  "三字角色（数据源49 前置机）",
			ident: "000106-192.168.57.131-泸州市工程造价数据库项目-前置机",
			want: IdentInfo{Tenant: "000106", IP: "192.168.57.131", Section: "—",
				Project: "泸州市工程造价数据库项目", Role: "前置机", Engineer: "—"},
		},
		{
			name:  "四字角色（数据源51 网关服务）",
			ident: "000025-10.192.173.91-数据资源管理平台-网关服务",
			want: IdentInfo{Tenant: "000025", IP: "10.192.173.91", Section: "—",
				Project: "数据资源管理平台", Role: "网关服务", Engineer: "—"},
		},
		{
			name:  "五字角色本来就正常（数据源54 附件服务器）",
			ident: "000002-10.194.67.154-泸州市环保三级统筹项目-附件服务器",
			want: IdentInfo{Tenant: "000002", IP: "10.194.67.154", Section: "—",
				Project: "泸州市环保三级统筹项目", Role: "附件服务器", Engineer: "—"},
		},
		{
			name:  "四段式：末段是人名也不再推断为工程师，并入角色（项目-角色-工程师）",
			ident: "000095-10.40.1.2-智慧民政-业务服务器8-张三",
			want: IdentInfo{Tenant: "000095", IP: "10.40.1.2", Section: "—",
				Project: "智慧民政", Role: "业务服务器8-张三", Engineer: "—"},
		},
		{
			name:  "非中文角色不受影响（数据源58 waf）",
			ident: "000058-172.16.1.1-河湖长制信息管理系统-waf",
			want: IdentInfo{Tenant: "000058", IP: "172.16.1.1", Section: "—",
				Project: "河湖长制信息管理系统", Role: "waf", Engineer: "—"},
		},
		{
			name:  "含连字符的角色整体保留（数据源51 前置库-互联网1）",
			ident: "000025-10.191.4.239-数据资源管理平台-前置库-互联网1",
			want: IdentInfo{Tenant: "000025", IP: "10.191.4.239", Section: "—",
				Project: "数据资源管理平台", Role: "前置库-互联网1", Engineer: "—"},
		},
		{
			name:  "三段式 section-first：分区-项目-角色（角色为三段中的末段）",
			ident: "000093-192.168.30.105-大数据生产区-天地图政务版-电子签章",
			want: IdentInfo{Tenant: "000093", IP: "192.168.30.105", Section: "大数据生产区",
				Project: "天地图政务版", Role: "电子签章", Engineer: "—"},
		},
		{
			name:  "四段式 section-first：末段是人名也不再推断为工程师，并入角色",
			ident: "000093-192.168.30.105-大数据生产区-天地图政务版-业务服务器1-邹源",
			want: IdentInfo{Tenant: "000093", IP: "192.168.30.105", Section: "大数据生产区",
				Project: "天地图政务版", Role: "业务服务器1-邹源", Engineer: "—"},
		},
		{
			name:  "无 IP 三段式：租户-项目-角色（角色为末段 ASCII 标识）",
			ident: "000064-少数民族流动信息化平台-WAF",
			want: IdentInfo{Tenant: "000064", IP: "—", Section: "—",
				Project: "少数民族流动信息化平台", Role: "WAF", Engineer: "—"},
		},
		{
			name:  "无 IP 且项目名含连字符：末段 ASCII 标识为角色，中间段并回项目",
			ident: "000063-泸州市委组织部-公务员培训网-db",
			want: IdentInfo{Tenant: "000063", IP: "—", Section: "—",
				Project: "泸州市委组织部-公务员培训网", Role: "db", Engineer: "—"},
		},
		{
			name:  "无 IP 且首段非租户 ID：整体作为项目名兜底",
			ident: "某业务平台-业务服务器1",
			want: IdentInfo{Tenant: "", IP: "某业务平台-业务服务器1", Section: "—",
				Project: "某业务平台-业务服务器1", Role: "—", Engineer: "—"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseIdent(c.ident, "auto", "auto")
			if got != c.want {
				t.Errorf("\nident = %s\n got  = %+v\n want = %+v", c.ident, got, c.want)
			}
		})
	}
}

// TestEngineerNeverInferred 锁定新约定：运维工程师不从 ident 推断，恒为占位值「—」，
// 且 engineerTail 三个取值（含已弃用的 always）都不再影响解析结果。
//
// 回归背景：ds39 的 `…-公共信用信息共享平台-信用二期-中间件` 曾被误判为
// 「角色=信用二期、工程师=中间件」，导致报告表 1 的「运维工程师」栏显示成一串角色词
// （中间件、主数据库、前后端、大屏、政务网、数据库）。根因见 parseIdent 注释。
func TestEngineerNeverInferred(t *testing.T) {
	// 典型的「角色词恰好是 2~4 个汉字」集合 —— 全部来自 ds39 真实 ident
	roleLikeIdents := []string{
		"000026-10.81.20.126-公共信用信息共享平台-信用二期-中间件",
		"000026-10.81.20.112-公共信用信息共享平台-信用二期-主数据库",
		"000026-10.192.197.5-公共信用信息共享平台-信用一期-前后端",
		"000026-10.192.197.7-公共信用信息共享平台-信用一期-大屏",
		"000026-10.192.197.40-公共信用信息共享平台-联合奖惩-政务网",
		"000026-10.192.197.14-公共信用信息共享平台-信用泸州官网-数据库",
		// 真实姓名同样不再被推断
		"000095-10.40.1.2-智慧民政-业务服务器8-张三",
		"000093-192.168.30.105-大数据生产区-天地图政务版-业务服务器1-邹源",
	}
	for _, tail := range []string{"auto", "always", "never"} {
		for _, id := range roleLikeIdents {
			got := parseIdent(id, "auto", tail)
			if got.Engineer != "—" {
				t.Errorf("engineerTail=%s 下 %q 仍推断出工程师 %q，应恒为「—」",
					tail, id, got.Engineer)
			}
			// 角色必须非空 —— 原被误当作工程师的那段应并入角色，信息不丢
			if got.Role == "" || got.Role == "—" {
				t.Errorf("engineerTail=%s 下 %q 的角色为空，末段应并入角色", tail, id)
			}
		}
	}

	// 具体校验一条：末段「中间件」应留在角色里
	got := parseIdent("000026-10.81.20.126-公共信用信息共享平台-信用二期-中间件", "auto", "auto")
	if got.Project != "公共信用信息共享平台" || got.Role != "信用二期-中间件" {
		t.Errorf("解析异常：%+v（期望 Project=公共信用信息共享平台 Role=信用二期-中间件）", got)
	}
}

func TestParseIdentLayoutOverride(t *testing.T) {
	ident := "000093-10.1.1.1-大数据生产区-天地图政务版-业务服务器1"
	// 显式 section-first：首段作为网络分区，其余按 项目-角色 解析
	got := parseIdent(ident, "section-first", "auto")
	if got.Section != "大数据生产区" || got.Project != "天地图政务版" || got.Role != "业务服务器1" {
		t.Errorf("显式 section-first 解析异常：%+v", got)
	}
	// 显式 project-first：不再切分网络分区，余下各段整体归入角色
	got = parseIdent(ident, "project-first", "auto")
	if got.Section != "—" || got.Project != "大数据生产区" || got.Role != "天地图政务版-业务服务器1" {
		t.Errorf("显式 project-first 解析异常：%+v", got)
	}
}

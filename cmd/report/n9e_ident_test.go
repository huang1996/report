package main

// n9e_ident_test.go —— ident 解析单测
//
// 重点覆盖「末尾 2~4 个纯汉字到底是角色还是运维工程师」这一歧义：
// 角色是必备字段，因此摘掉末尾段后若解析不出角色，就应把它当作角色本身。

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
			name:  "四段式：末尾姓名仍是工程师（项目-角色-工程师）",
			ident: "000095-10.40.1.2-智慧民政-业务服务器8-张三",
			want: IdentInfo{Tenant: "000095", IP: "10.40.1.2", Section: "—",
				Project: "智慧民政", Role: "业务服务器8", Engineer: "张三"},
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
			name:  "四段式 section-first：分区-项目-角色-工程师",
			ident: "000093-192.168.30.105-大数据生产区-天地图政务版-业务服务器1-邹源",
			want: IdentInfo{Tenant: "000093", IP: "192.168.30.105", Section: "大数据生产区",
				Project: "天地图政务版", Role: "业务服务器1", Engineer: "邹源"},
		},
		{
			name:  "无 IP 的 ident 退回占位值",
			ident: "000063-泸州市委组织部-公务员培训网-db",
			want: IdentInfo{Tenant: "", IP: "000063-泸州市委组织部-公务员培训网-db", Section: "—",
				Project: "000063-泸州市委组织部-公务员培训网-db", Role: "—", Engineer: "—"},
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

func TestParseIdentEngineerTailModes(t *testing.T) {
	ident := "000002-10.194.67.194-泸州市环保三级统筹项目-电子签章"

	// auto：角色必备，末尾段留作角色
	if got := parseIdent(ident, "auto", "auto"); got.Role != "电子签章" || got.Engineer != "—" {
		t.Errorf("auto 模式应把末尾段当角色，实际 %+v", got)
	}
	// always：沿用旧行为，末尾段被当作工程师（此处角色为空，正是本次修复前的错位现象）
	if got := parseIdent(ident, "auto", "always"); got.Engineer != "电子签章" || got.Role != "—" {
		t.Errorf("always 模式应把末尾段当工程师，实际 %+v", got)
	}
	// never：不做工程师识别，末尾段并入角色
	if got := parseIdent(ident, "auto", "never"); got.Role != "电子签章" || got.Engineer != "—" {
		t.Errorf("never 模式应把末尾段并入角色，实际 %+v", got)
	}
	// 四段式下 never 与 auto 的差异：never 会把姓名并入角色
	ident4 := "000095-10.40.1.2-智慧民政-业务服务器8-张三"
	if got := parseIdent(ident4, "auto", "never"); got.Role != "业务服务器8-张三" {
		t.Errorf("never 模式应把姓名并入角色，实际 %+v", got)
	}
	if got := parseIdent(ident4, "auto", "auto"); got.Role != "业务服务器8" || got.Engineer != "张三" {
		t.Errorf("auto 模式应摘出工程师，实际 %+v", got)
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

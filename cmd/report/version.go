package main

// version.go —— 版本信息

// AppVersion 当前程序版本；默认值仅用于未注入版本号的本地构建，
// CI / Docker 构建时通过 -ldflags "-X main.AppVersion=<tag>" 注入
var AppVersion = "0.1.5"

// AppName 程序名称
const AppName = "巡检周报生成器"

// Version 返回带 v 前缀的版本号（兼容注入值是否自带 v 前缀）
func Version() string {
	v := AppVersion
	if len(v) > 0 && (v[0] == 'v' || v[0] == 'V') {
		v = v[1:]
	}
	return "v" + v
}

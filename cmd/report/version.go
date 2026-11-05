package main

// version.go —— 版本信息

// AppVersion 当前程序版本；每次发布版本时同步改成新版本号（与 tag 保持一致），
// CI / Docker 构建时仍通过 -ldflags "-X main.AppVersion=<tag>" 注入覆盖
var AppVersion = "0.2.2"

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

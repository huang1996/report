package main

// charts_font_test.go —— 验证内嵌字体可被 freetype 解析（兜底渲染保障）

import (
	"testing"

	"github.com/golang/freetype/truetype"
)

func TestEmbeddedFontParsable(t *testing.T) {
	if len(embeddedFontData) == 0 {
		t.Fatal("内嵌字体数据为空")
	}
	f, err := truetype.Parse(embeddedFontData)
	if err != nil {
		t.Fatalf("内嵌字体解析失败：%v", err)
	}
	// 验证包含中文、数字、拉丁与常用符号字形（纯 CJK 回退字体缺数字，不合格）
	for _, r := range []rune{'巡', '检', '周', '报', '0', '9', '%', '.', ':', 'A', 'a', '-'} {
		if f.Index(r) == 0 {
			t.Errorf("内嵌字体缺少字形 U+%04X (%c)", r, r)
		}
	}
}

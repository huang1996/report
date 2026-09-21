package main

// charts.go —— 纯 Go 图表渲染（分组柱状图 / 饼图），输出 PNG 供 docx 嵌入。
// 文字渲染使用 freetype + 运行时加载的中文字体。

import (
	_ "embed"
	"image"
	"image/color"
	"image/draw"
	"io"
	"math"
	"sort"

	"github.com/golang/freetype"
	"github.com/golang/freetype/truetype"
	"golang.org/x/image/math/fixed"
)

// 内嵌中文字体 NotoSansSC（SIL Open Font License 1.1，可自由再分发）。
// 优先使用内嵌字体而非系统字体：一是保证任何环境（容器/裸机）下图表文字都能渲染，
// 二是渲染结果跨环境一致——Debian 的 fonts-droid-fallback 等纯 CJK 回退字体
// 不含数字/拉丁字形，会导致图表中的数值标签全部缺失。
//
//go:embed fonts/NotoSansSC-Regular.ttf
var embeddedFontData []byte

var chartFont *truetype.Font

// LoadChartFont 优先加载内嵌字体；失败时依次尝试系统字体路径。
// 返回值 fontSrc 为实际加载来源（用于日志），err 非空表示无任何可用字体。
func LoadChartFont(paths []string) (fontSrc string, err error) {
	// 内嵌字体优先：跨环境一致且覆盖数字/拉丁/CJK
	if f, err := truetype.Parse(embeddedFontData); err == nil {
		chartFont = f
		return "内嵌字体 NotoSansSC-Regular.ttf", nil
	}
	for _, p := range paths {
		data, err := readFile(p)
		if err != nil {
			continue
		}
		f, err := truetype.Parse(data)
		if err != nil {
			continue
		}
		chartFont = f
		return p, nil
	}
	return "", errFontNotFound
}

var errFontNotFound = fmtErr("未找到可用的中文字体，图表文字将缺失")

type strErr string

func (e strErr) Error() string { return string(e) }

func fmtErr(s string) error { return strErr(s) }

func readFile(p string) ([]byte, error) {
	f, err := openFile(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

var fontCandidates = []string{
	"/usr/share/fonts/truetype/droid/DroidSansFallbackFull.ttf",
	"/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
	"/usr/share/fonts/wqy-microhei/wqy-microhei.ttc",
	"/usr/share/fonts/truetype/wqy/wqy-microhei.ttc",
	"/usr/share/fonts/wqy-zenhei/wqy-zenhei.ttc",
	"C:\\Windows\\Fonts\\simhei.ttf",
	"C:\\Windows\\Fonts\\msyh.ttc",
}

var chartPalette = []string{
	"2E75B6", "ED7D31", "7F7F7F", "5B9BD5", "A5A5A5",
	"FFC000", "4472C4", "70AD47", "C00000", "7030A0",
}

func hexColor(h string) color.RGBA {
	var r, g, b uint8
	if len(h) == 7 && h[0] == '#' {
		h = h[1:]
	}
	if len(h) == 6 {
		r = hexByte(h[0], h[1])
		g = hexByte(h[2], h[3])
		b = hexByte(h[4], h[5])
	}
	return color.RGBA{r, g, b, 255}
}

func hexByte(a, b byte) uint8 {
	return hexNibble(a)<<4 | hexNibble(b)
}

func hexNibble(c byte) uint8 {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10
	}
	return 0
}

// textWidth 估算字符串像素宽度
func textWidth(s string, sizePx float64) int {
	if chartFont == nil {
		return len(s) * int(sizePx*0.7)
	}
	scale := sizePx / float64(chartFont.FUnitsPerEm())
	w := 0.0
	for _, r := range s {
		idx := chartFont.Index(r)
		hm := chartFont.HMetric(fixed.Int26_6(chartFont.FUnitsPerEm()), idx)
		w += float64(hm.AdvanceWidth) * scale
	}
	return int(math.Ceil(w))
}

// drawText align: 0=左对齐(x为左缘) 1=居中(x为中心) 2=右对齐(x为右缘)；y 为文字顶部
func drawText(dst *image.RGBA, s string, x, y int, sizePx float64, c color.RGBA, align int) {
	if chartFont == nil || s == "" {
		return
	}
	w := textWidth(s, sizePx)
	switch align {
	case 1:
		x -= w / 2
	case 2:
		x -= w
	}
	ctx := freetype.NewContext()
	ctx.SetDPI(72)
	ctx.SetFont(chartFont)
	ctx.SetFontSize(sizePx)
	ctx.SetClip(dst.Bounds())
	ctx.SetDst(dst)
	ctx.SetSrc(image.NewUniform(c))
	pt := freetype.Pt(x, y+int(sizePx*0.82))
	ctx.DrawString(s, pt)
}

// renderLabel 把文字渲染到透明底图（供旋转）
func renderLabel(s string, sizePx float64, c color.RGBA) *image.RGBA {
	w := textWidth(s, sizePx) + 4
	h := int(sizePx*1.4) + 4
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	drawText(img, s, 2, 0, sizePx, c, 0)
	return img
}

func rotateImg(src *image.RGBA, deg float64) *image.RGBA {
	rad := deg * math.Pi / 180
	sin, cos := math.Sin(rad), math.Cos(rad)
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	nw := int(math.Abs(float64(w)*cos)+math.Abs(float64(h)*sin)) + 2
	nh := int(math.Abs(float64(w)*sin)+math.Abs(float64(h)*cos)) + 2
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	cx, cy := float64(w)/2, float64(h)/2
	ncx, ncy := float64(nw)/2, float64(nh)/2
	for y := 0; y < nh; y++ {
		for x := 0; x < nw; x++ {
			dx := float64(x) - ncx
			dy := float64(y) - ncy
			sx := cos*dx + sin*dy + cx
			sy := -sin*dx + cos*dy + cy
			if sx < 0 || sy < 0 || sx >= float64(w) || sy >= float64(h) {
				continue
			}
			dst.Set(x, y, src.At(int(sx), int(sy)))
		}
	}
	return dst
}

// strokeW 辅助线笔宽（像素）；柱状图超采样渲染时调大以保持视觉线宽，
// 虚线/点线的段长与间距随之等比放大。
var strokeW = 1

func vline(img *image.RGBA, x, y0, y1 int, c color.RGBA) {
	for dx := 0; dx < strokeW; dx++ {
		for y := y0; y <= y1; y++ {
			img.Set(x+dx, y, c)
		}
	}
}

func hline(img *image.RGBA, y, x0, x1 int, c color.RGBA) {
	for dy := 0; dy < strokeW; dy++ {
		for x := x0; x <= x1; x++ {
			img.Set(x, y+dy, c)
		}
	}
}

// 虚线（线段 12px 间隔 8px，随笔宽等比放大）
func dashH(img *image.RGBA, y, x0, x1 int, c color.RGBA) {
	seg, gap := 12*strokeW, 8*strokeW
	for x := x0; x <= x1; x += seg + gap {
		for i := 0; i < seg && x+i <= x1; i++ {
			for dy := 0; dy < 2*strokeW; dy++ {
				img.Set(x+i, y+dy, c)
			}
		}
	}
}

// 点状网格线
func dotH(img *image.RGBA, y, x0, x1 int, c color.RGBA) {
	for x := x0; x <= x1; x += 6 * strokeW {
		for dy := 0; dy < strokeW; dy++ {
			img.Set(x, y+dy, c)
		}
	}
}

func fillRect(img *image.RGBA, x0, y0, x1, y1 int, c color.RGBA) {
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			img.Set(x, y, c)
		}
	}
}

// BarSeries 单个柱状序列
type BarSeries struct {
	Name   string
	Color  string
	Values []float64
	Fmt    string // 数值标签格式，如 "%.2f%%"
}

// DrawGroupedBarChart 分组柱状图（对应原脚本的 chart_cur_peak / chart_disk_io / chart_gap / chart_overview）
func DrawGroupedBarChart(title string, labels []string, series []BarSeries,
	warnLine float64, warnText, ylabel string, ymaxOverride float64) image.Image {

	// 2x 超采样渲染：画布与全部坐标/字号乘 S，插入文档后显示尺寸不变，
	// 有效像素密度翻倍（与饼图一致），避免缩略图式模糊
	const S = 2
	const W, H = 1960 * S, 800 * S
	const left, right, top, bottom = 110 * S, 50 * S, 95 * S, 120 * S
	// 辅助线（网格/坐标轴/预警线）笔宽同步放大，保持与放大前一致的视觉粗细
	oldStroke := strokeW
	strokeW = S
	defer func() { strokeW = oldStroke }()

	img := image.NewRGBA(image.Rect(0, 0, W, H))
	draw.Draw(img, img.Bounds(), &image.Uniform{color.White}, image.Point{}, draw.Src)

	plotW := W - left - right
	plotH := H - top - bottom
	n := len(labels)

	ymax := ymaxOverride
	if ymax <= 0 {
		mx := 100.0
		for _, s := range series {
			for _, v := range s.Values {
				if v*1.35 > mx {
					mx = v * 1.35
				}
			}
		}
		ymax = math.Ceil(mx/20) * 20
	}
	yOf := func(v float64) int {
		return top + plotH - int(v/ymax*float64(plotH))
	}

	// 网格与刻度
	grey := hexColor("D0D0D0")
	txtC := hexColor("404040")
	div := 5
	for i := 0; i <= div; i++ {
		v := ymax * float64(i) / float64(div)
		y := yOf(v)
		dotH(img, y, left, W-right, grey)
		lb := fmtNum(v)
		drawText(img, lb, left-10*S, y-10*S, 17*S, txtC, 2)
	}

	// 坐标轴
	axisC := hexColor("909090")
	hline(img, top+plotH, left, W-right, axisC)
	vline(img, left, top, top+plotH, axisC)

	if n == 0 {
		return img
	}

	groupW := float64(plotW) / float64(n)
	k := len(series)
	barGap := 4 * S
	barW := (groupW*0.68 - float64((k-1)*barGap)) / float64(k)
	if barW < 2 {
		barW = 2
	}

	for gi := 0; gi < n; gi++ {
		gx := float64(left) + groupW*float64(gi) + groupW*0.16
		for si := 0; si < k; si++ {
			v := series[si].Values[gi]
			x0 := int(gx + float64(si)*(barW+float64(barGap)))
			y0 := yOf(v)
			y1 := top + plotH
			fillRect(img, x0, y0, x0+int(barW), y1, hexColor(series[si].Color))
			// 数值标签
			lb := "0"
			if series[si].Fmt != "" {
				lb = sprintf(series[si].Fmt, v)
			} else {
				lb = fmtNum(v)
			}
			drawText(img, lb, x0+int(barW)/2, y0-22*S, 15*S, txtC, 1)
		}
	}

	// 预警线
	if warnLine > 0 {
		yw := yOf(warnLine)
		if yw > top && yw < top+plotH {
			red := hexColor("C00000")
			dashH(img, yw, left, W-right, red)
			drawText(img, warnText, W-right-8*S, yw-24*S, 17*S, red, 2)
		}
	}

	// X 轴标签（>10 个时旋转 30°）
	labC := txtC
	for gi := 0; gi < n; gi++ {
		cx := int(float64(left) + groupW*float64(gi) + groupW/2)
		if n > 10 {
			rot := rotateImg(renderLabel(labels[gi], 18*S, labC), 30)
			b := rot.Bounds()
			draw.Draw(img, image.Rect(cx-b.Dx()+6*S, top+plotH+8*S, cx+6*S, top+plotH+8*S+b.Dy()),
				rot, image.Point{}, draw.Over)
		} else {
			drawText(img, labels[gi], cx, top+plotH+10*S, 19*S, labC, 1)
		}
	}

	// 标题 / 图例 / 单位说明
	navy := hexColor("1F3864")
	drawText(img, title, W/2, 22*S, 25*S, navy, 1)
	lx := left + 10*S
	for _, s := range series {
		fillRect(img, lx, 62*S, lx+16*S, 78*S, hexColor(s.Color))
		drawText(img, s.Name, lx+24*S, 60*S, 19*S, txtC, 0)
		lx += 24*S + textWidth(s.Name, 19*S) + 30*S
	}
	if ylabel != "" {
		drawText(img, "单位："+ylabel, W-right-8*S, 60*S, 17*S, hexColor("808080"), 2)
	}
	return img
}

func fmtNum(v float64) string {
	if v >= 1000 {
		return sprintf("%.0f", v)
	}
	if v == math.Trunc(v) {
		return sprintf("%.0f", v)
	}
	return sprintf("%.1f", v)
}

// PieItem 饼图条目
type PieItem struct {
	Label string
	Value float64
}

// DrawPieChart 饼图（对应原脚本 get_attack_total_by_type 的攻击类型统计图）。
// 2x 超采样渲染：docx 中按固定 cm 宽插入，像素翻倍后显示尺寸不变、清晰度提升。
func DrawPieChart(title string, items []PieItem) image.Image {
	const S = 2
	linePen = 2 * S // 指示线笔宽同步放大
	defer func() { linePen = 2 }()
	// 画布高度按「标注占位」动态确定：小扇区集中在 6 点方向时，标注会被防重叠逻辑
	// 逐个向下推，固定高度会把最后一截标注挤出图片区域。因此先做一轮纯几何预演
	// （不依赖画布），算出标注纵向占用，再开画布；圆心随之调整，让内容纵向居中。
	const (
		pieR     = 300.0 * S  // 饼半径
		minExt   = 40.0 * S   // 指示线径向段最小延伸长度
		minGap   = 38.0 * S   // 同侧相邻标注最小纵向间距
		titleBot = 60.0 * S   // 标题区下缘（标注上边界）
		botPad   = 30.0 * S   // 画布下边距
		Hmax     = 1400.0 * S // 画布高度上限：极端数据时间距压缩兜底，避免画布无限拉长
	)
	W := 1400 * S
	cx := 560.0 * S

	total := 0.0
	for _, it := range items {
		total += it.Value
	}
	if len(items) == 0 || total <= 0 {
		img := image.NewRGBA(image.Rect(0, 0, W, 600*S))
		draw.Draw(img, img.Bounds(), &image.Uniform{color.White}, image.Point{}, draw.Src)
		drawText(img, title, W/2, 26*S, 30*S, hexColor("1F3864"), 1)
		return img
	}

	start := -math.Pi / 2 // 起始角：6 点方向，逐项逆时针推进（与 fillSector 的 y 翻转坐标系一致）
	mids := make([]float64, len(items))
	pcts := make([]float64, len(items))
	arcs := make([][2]float64, len(items))
	for i, it := range items {
		ang := it.Value / total * 2 * math.Pi
		arcs[i] = [2]float64{start, start + ang}
		mids[i] = start + ang/2
		pcts[i] = it.Value / total * 100
		start = arcs[i][1]
	}

	// 标注默认按扇区方位分列（classifySide），但正上/正下近垂直区域内的扇区，
	// 指示线水平段从圆外经过，放左放右均不会穿过饼面 —— 这类「可摆动」标注
	// 按两侧纵向拥挤程度动态分配，哪边宽松放哪边，避免小扇区在单侧挤压溢出。
	sides := make([]float64, len(items))
	var flex []int
	flexSin := (pieR + 12.0*S) / (pieR + minExt) // 水平段不擦碰饼缘的安全阈值
	for i, mid := range mids {
		sides[i] = classifySide(mid)
		if math.Abs(math.Sin(mid)) >= flexSin {
			flex = append(flex, i)
		}
	}
	// splitCol 把一列标注按上/下半圆分组并排序（由近圆心到远圆心）
	splitCol := func(inds []int) (above, below []int) {
		for _, i := range inds {
			if math.Sin(mids[i]) > 0 {
				above = append(above, i)
			} else {
				below = append(below, i)
			}
		}
		sort.Slice(above, func(a, b int) bool { return math.Sin(mids[above[a]]) < math.Sin(mids[above[b]]) })
		sort.Slice(below, func(a, b int) bool { return math.Sin(mids[below[a]]) > math.Sin(mids[below[b]]) })
		return above, below
	}
	// chainExtent 以圆心为原点、按 minGap 模拟一组标注的纵向排布，返回该组离圆心最远的延伸量
	chainExtent := func(inds []int, up bool) float64 {
		if len(inds) == 0 {
			return 0
		}
		prev := math.NaN()
		last := 0.0
		for _, i := range inds {
			f := (pieR + minExt) * -math.Sin(mids[i])
			if !math.IsNaN(prev) {
				if up {
					if f > prev-minGap {
						f = prev - minGap
					}
				} else {
					if f < prev+minGap {
						f = prev + minGap
					}
				}
			}
			prev = f
			last = f
		}
		if up {
			return -last
		}
		return last
	}
	// 标注几何常量（geomFor 求值与绘制共用）
	const (
		minGapMin = 30.0 * S   // 防溢出时间距压缩的下限（仍大于字高，不会叠字）
		maxExt    = 220.0 * S  // 径向段最大延伸长度
		colLeftX  = 80.0 * S   // 左列文字左对齐锚点（左侧页边距）
		colRightX = 1060.0 * S // 右列文字右对齐锚点（避开右侧图例）
	)

	// layoutGroup 计算同侧一组标注的纵向位置。up=true 上半圆（向上排布），
	// up=false 下半圆（向下排布）。inds 已按由近圆心到远圆心排序。
	layoutGroup := func(inds []int, up bool, cy, topLimit, botLimit float64) []float64 {
		n := len(inds)
		fys := make([]float64, n)
		natural := func(i int) float64 {
			return cy + (pieR+minExt)*-math.Sin(mids[i])
		}
		limit, gap := botLimit, minGap
		if up {
			limit = topLimit
		}
		// 自然间距放不下边界内时，先压缩间距（等分剩余空间，但不小于 minGapMin）
		if n > 1 {
			room := limit - natural(inds[0])
			if !up {
				room = -room
			}
			if room < float64(n-1)*gap {
				gap = room / float64(n-1)
				if gap < minGapMin {
					gap = minGapMin
				}
			}
		}
		for k, i := range inds {
			fy := natural(i)
			if k > 0 {
				if up {
					if fy > fys[k-1]-gap {
						fy = fys[k-1] - gap
					}
				} else {
					if fy < fys[k-1]+gap {
						fy = fys[k-1] + gap
					}
				}
			}
			fys[k] = fy
		}
		// 仍越界时整组向界内回移，但每项不越过自然径向位置（保证径向段不短于 minExt）
		last := fys[n-1]
		shift := 0.0
		if !up && last > limit {
			shift = last - limit
		} else if up && last < limit {
			shift = limit - last
		}
		if shift > 0 {
			for k, i := range inds {
				fy := fys[k]
				if up {
					fy += shift
					if fy > natural(i) {
						fy = natural(i)
					}
				} else {
					fy -= shift
					if fy < natural(i) {
						fy = natural(i)
					}
				}
				fys[k] = fy
			}
		}
		return fys
	}

	// pieLead 单个标注折线的端点：弧中点(ax,ay) → 折点(fx,fy) → 列锚点(ex,fy)
	type pieLead struct {
		ax, ay, fx, fy, ex float64
	}

	// geomFor 按给定分列方案完整求值布局：画布高度、圆心与每个标注的折线端点。
	// 换边方案的交叉检测与最终绘制都基于它，保证二者几何严格一致。
	geomFor := func(sds []float64) ([]pieLead, float64, float64) {
		var l, r []int
		for i := range items {
			if sds[i] < 0 {
				l = append(l, i)
			} else {
				r = append(r, i)
			}
		}
		la, lb := splitCol(l)
		ra, rb := splitCol(r)
		extAbove := math.Max(chainExtent(la, true), chainExtent(ra, true))
		extBelow := math.Max(chainExtent(lb, false), chainExtent(rb, false))
		legendH := 240.0*S + float64(len(items))*44.0*S + 20.0*S // 图例底端不超出画布
		h := math.Max(900*S, math.Max(titleBot+extAbove+extBelow+botPad+8*S, legendH))
		h = math.Min(h, Hmax)
		// 圆心：让标注内容在「标题以下 ~ 下边距以上」区间居中，并保证饼不顶进标题区
		c := titleBot + extAbove + math.Max(0, (h-botPad-titleBot-extAbove-extBelow)/2)
		if minCy := titleBot + pieR + 8*S; c < minCy {
			c = minCy
		}
		if maxCy := h - botPad - pieR; c > maxCy {
			c = maxCy
		}
		topLimit := titleBot + 8.0*S
		botLimit := h - botPad
		leads := make([]pieLead, len(items))
		for _, col := range [][]int{l, r} {
			above, below := splitCol(col)
			for gi, group := range [][]int{above, below} {
				if len(group) == 0 {
					continue
				}
				fys := layoutGroup(group, gi == 0, c, topLimit, botLimit)
				for k, i := range group {
					mid := mids[i]
					fy := fys[k]
					cosM, sinM := math.Cos(mid), math.Sin(mid)
					sn := -sinM // 折点 y = cy + (r+ext)·sn
					ext := minExt
					if math.Abs(sn) > 1e-6 {
						ext = (fy-c)/sn - pieR
					}
					if ext < minExt {
						ext = minExt
						fy = c + (pieR+ext)*sn
					}
					if ext > maxExt {
						ext = maxExt
						fy = c + (pieR+ext)*sn
					}
					ld := pieLead{
						ax: cx + (pieR-2.0*S)*cosM,
						ay: c - (pieR-2.0*S)*sinM,
						fx: cx + (pieR+ext)*cosM,
						fy: fy,
						ex: colLeftX,
					}
					if sds[i] > 0 {
						ld.ex = colRightX
					}
					// 罕见情形：折点已在锚点外侧（近 3 点方向且 ext 被推大），线段改为向外短延伸
					if sds[i] > 0 && ld.fx > ld.ex {
						ld.ex = ld.fx + 12*S
					}
					if sds[i] < 0 && ld.fx < ld.ex {
						ld.ex = ld.fx - 12*S
					}
					leads[i] = ld
				}
			}
		}
		return leads, h, c
	}

	// segsIntersect 判断两线段是否相交（含端点接触与共线重叠，按 0.5px 距离容差保守判定）
	segsIntersect := func(x0, y0, x1, y1, x2, y2, x3, y3 float64) bool {
		orient := func(ax, ay, bx, by, px, py float64) float64 {
			return (bx-ax)*(py-ay) - (by-ay)*(px-ax)
		}
		onSeg := func(px, py, ax, ay, bx, by float64) bool {
			if px < math.Min(ax, bx)-0.5 || px > math.Max(ax, bx)+0.5 ||
				py < math.Min(ay, by)-0.5 || py > math.Max(ay, by)+0.5 {
				return false
			}
			len2 := math.Hypot(bx-ax, by-ay)
			if len2 < 1e-9 {
				return math.Hypot(px-ax, py-ay) <= 0.5
			}
			return math.Abs(orient(ax, ay, bx, by, px, py))/len2 <= 0.5
		}
		d1 := orient(x2, y2, x3, y3, x0, y0)
		d2 := orient(x2, y2, x3, y3, x1, y1)
		d3 := orient(x0, y0, x1, y1, x2, y2)
		d4 := orient(x0, y0, x1, y1, x3, y3)
		if ((d1 > 0 && d2 < 0) || (d1 < 0 && d2 > 0)) && ((d3 > 0 && d4 < 0) || (d3 < 0 && d4 > 0)) {
			return true
		}
		return onSeg(x0, y0, x2, y2, x3, y3) || onSeg(x1, y1, x2, y2, x3, y3) ||
			onSeg(x2, y2, x0, y0, x1, y1) || onSeg(x3, y3, x0, y0, x1, y1)
	}

	// crossFree 检查分列方案下所有标注折线两两无交叉（换边准入条件）
	crossFree := func(sds []float64) bool {
		leads, _, _ := geomFor(sds)
		for i := range items {
			a := leads[i]
			for j := i + 1; j < len(items); j++ {
				b := leads[j]
				if segsIntersect(a.ax, a.ay, a.fx, a.fy, b.ax, b.ay, b.fx, b.fy) ||
					segsIntersect(a.ax, a.ay, a.fx, a.fy, b.fx, b.fy, b.ex, b.fy) ||
					segsIntersect(a.fx, a.fy, a.ex, a.fy, b.ax, b.ay, b.fx, b.fy) ||
					segsIntersect(a.fx, a.fy, a.ex, a.fy, b.fx, b.fy, b.ex, b.fy) {
					return false
				}
			}
		}
		return true
	}

	// extTotal 返回当前分列下标注的纵向总占用（上延伸+下延伸），作为均衡目标
	extTotal := func() float64 {
		var l, r []int
		for i := range items {
			if sides[i] < 0 {
				l = append(l, i)
			} else {
				r = append(r, i)
			}
		}
		la, lb := splitCol(l)
		ra, rb := splitCol(r)
		return math.Max(chainExtent(la, true), chainExtent(ra, true)) +
			math.Max(chainExtent(lb, false), chainExtent(rb, false))
	}
	// 逐个尝试把可摆动标注换到另一侧：仅当换边后纵向总占用更小、且全部折线两两无交叉时
	// 才保留换边结果，反复迭代直到稳定（可摆动标注通常只有一两个，开销可忽略）。
	best := extTotal()
	for changed := true; changed; {
		changed = false
		for _, i := range flex {
			sides[i] = -sides[i]
			if s := extTotal(); s < best-0.5 && crossFree(sides) {
				best = s
				changed = true
			} else {
				sides[i] = -sides[i]
			}
		}
	}

	// 按最终分列方案求值布局（绘制与检测结果严格一致）
	leaders, H, cy := geomFor(sides)

	img := image.NewRGBA(image.Rect(0, 0, W, int(H)))
	draw.Draw(img, img.Bounds(), &image.Uniform{color.White}, image.Point{}, draw.Src)
	drawText(img, title, W/2, 26*S, 30*S, hexColor("1F3864"), 1)

	for i := range items { // 扇区（第一块外扩强调）
		ocx, ocy := cx, cy
		if i == 0 {
			ocx = cx + 14.0*S*math.Cos(mids[i])
			ocy = cy - 14.0*S*math.Sin(mids[i]) // 像素 y 向下，与 fillSector 的角度约定一致
		}
		fillSector(img, int(ocx), int(ocy), int(pieR), arcs[i][0], arcs[i][1], hexColor(chartPalette[i%len(chartPalette)]))
	}

	// 外部文本标注：饼图内不显示占比，文字（名称+占比）落在第二段水平线段上；
	// 左列标注统一左对齐、右列标注统一右对齐（固定列锚点）；折线端点由 geomFor 求值。
	const (
		labelSize = 22.0 * S // 标注文字字号
		labelTop  = 27.0 * S // 文字绘制点相对线段上移量
		backTop   = 31.0 * S // 白色衬底上边界相对线段上移量
		backBot   = 4.0 * S  // 白色衬底下边界相对线段上移量（留出线段作下划线）
	)
	// 绘制全部标注折线与文字（端点取自 geomFor 求值结果，与交叉检测一致）
	for i := range items {
		ld := leaders[i]
		c := hexColor(chartPalette[i%len(chartPalette)])
		drawLine(img, int(ld.ax), int(ld.ay), int(ld.fx), int(ld.fy), c)
		drawLine(img, int(ld.fx), int(ld.fy), int(ld.ex), int(ld.fy), c)
		label := sprintf("%s %.2f%%", items[i].Label, pcts[i])
		tw := float64(textWidth(label, labelSize))
		// 文字对齐落位：白色衬底防压扇区边缘（衬底底边在线段上方，线段保持可见作下划线）
		tx, align := int(ld.ex), 0
		if sides[i] > 0 {
			tx = int(ld.ex) - int(tw)
			align = 2
		}
		fillRect(img, tx-4*S, int(ld.fy)-int(backTop), tx+int(tw)+4*S, int(ld.fy)-int(backBot), hexColor("FFFFFF"))
		drawText(img, label, int(ld.ex), int(ld.fy)-int(labelTop), labelSize, c, align)
	}

	// 图例（右侧）
	ly0 := 240 * S
	for i, it := range items {
		y := ly0 + i*44*S
		fillRect(img, 1090*S, y, 1106*S, y+16*S, hexColor(chartPalette[i%len(chartPalette)]))
		lb := sprintf("%s（%s）", it.Label, fmtNum(it.Value))
		drawText(img, lb, 1116*S, y-3*S, 21*S, hexColor("404040"), 0)
	}
	return img
}

// classifySide 决定饼图标注默认放左侧还是右侧（-1 左 / 1 右）。
// 正上方、正下方 ±~26° 区域内的扇区统一分配到固定一侧（上→右、下→左），
// 避免 cos 符号在 ±90° 附近抖动，把两个相邻小扇区的标注分到两侧而失去防重叠约束。
// 该默认值随后会被分列均衡逻辑修正：近垂直区域内的扇区可按拥挤程度摆动到另一侧。
func classifySide(mid float64) float64 {
	c, s := math.Cos(mid), math.Sin(mid)
	switch {
	case s < -0.9:
		return -1.0
	case s > 0.9:
		return 1.0
	case c < 0:
		return -1.0
	}
	return 1.0
}

// linePen 指示线笔宽（像素）；饼图超采样渲染时调大以保持视觉线宽
var linePen = 2

// drawLine 绘制任意方向线段（DDA 插值，用于饼图指示线）
func drawLine(img *image.RGBA, x0, y0, x1, y1 int, c color.RGBA) {
	dx, dy := x1-x0, y1-y0
	steps := dx
	if steps < 0 {
		steps = -steps
	}
	if dy < 0 && -dy > steps {
		steps = -dy
	} else if dy > steps {
		steps = dy
	}
	if steps == 0 {
		img.Set(x0, y0, c)
		return
	}
	for i := 0; i <= steps; i++ {
		x := x0 + dx*i/steps
		y := y0 + dy*i/steps
		for ox := 0; ox < linePen; ox++ {
			for oy := 0; oy < linePen; oy++ {
				img.Set(x+ox, y+oy, c)
			}
		}
	}
}

func fillSector(img *image.RGBA, cx, cy, r int, a0, a1 float64, c color.RGBA) {
	for y := -r; y <= r; y++ {
		for x := -r; x <= r; x++ {
			if x*x+y*y > r*r {
				continue
			}
			// 图像坐标 y 向下，角度取反
			ang := math.Atan2(float64(-y), float64(x))
			if angleIn(ang, a0, a1) {
				img.Set(cx+x, cy+y, c)
			}
		}
	}
}

func angleIn(ang, a0, a1 float64) bool {
	// a0<a1；ang 落在 [a0,a1]（处理跨 ±π）
	for ang < a0 {
		ang += 2 * math.Pi
	}
	return ang <= a1
}

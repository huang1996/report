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

func vline(img *image.RGBA, x, y0, y1 int, c color.RGBA) {
	for y := y0; y <= y1; y++ {
		img.Set(x, y, c)
	}
}

func hline(img *image.RGBA, y, x0, x1 int, c color.RGBA) {
	for x := x0; x <= x1; x++ {
		img.Set(x, y, c)
	}
}

// 虚线（线段 12px 间隔 8px）
func dashH(img *image.RGBA, y, x0, x1 int, c color.RGBA) {
	for x := x0; x <= x1; x += 20 {
		for i := 0; i < 12 && x+i <= x1; i++ {
			img.Set(x+i, y, c)
			img.Set(x+i, y+1, c)
		}
	}
}

// 点状网格线
func dotH(img *image.RGBA, y, x0, x1 int, c color.RGBA) {
	for x := x0; x <= x1; x += 6 {
		img.Set(x, y, c)
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

	const W, H = 1960, 800
	const left, right, top, bottom = 110, 50, 95, 120
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
		drawText(img, lb, left-10, y-10, 17, txtC, 2)
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
	barGap := 4
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
			drawText(img, lb, x0+int(barW)/2, y0-22, 15, txtC, 1)
		}
	}

	// 预警线
	if warnLine > 0 {
		yw := yOf(warnLine)
		if yw > top && yw < top+plotH {
			red := hexColor("C00000")
			dashH(img, yw, left, W-right, red)
			drawText(img, warnText, W-right-8, yw-24, 17, red, 2)
		}
	}

	// X 轴标签（>10 个时旋转 30°）
	labC := txtC
	for gi := 0; gi < n; gi++ {
		cx := int(float64(left) + groupW*float64(gi) + groupW/2)
		if n > 10 {
			rot := rotateImg(renderLabel(labels[gi], 18, labC), 30)
			b := rot.Bounds()
			draw.Draw(img, image.Rect(cx-b.Dx()+6, top+plotH+8, cx+6, top+plotH+8+b.Dy()),
				rot, image.Point{}, draw.Over)
		} else {
			drawText(img, labels[gi], cx, top+plotH+10, 19, labC, 1)
		}
	}

	// 标题 / 图例 / 单位说明
	navy := hexColor("1F3864")
	drawText(img, title, W/2, 22, 25, navy, 1)
	lx := left + 10
	for _, s := range series {
		fillRect(img, lx, 62, lx+16, 78, hexColor(s.Color))
		drawText(img, s.Name, lx+24, 60, 19, txtC, 0)
		lx += 24 + textWidth(s.Name, 19) + 30
	}
	if ylabel != "" {
		drawText(img, "单位："+ylabel, W-right-8, 60, 17, hexColor("808080"), 2)
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

// DrawPieChart 饼图（对应原脚本 get_attack_total_by_type 的攻击类型统计图）
func DrawPieChart(title string, items []PieItem) image.Image {
	const W, H = 1400, 900
	img := image.NewRGBA(image.Rect(0, 0, W, H))
	draw.Draw(img, img.Bounds(), &image.Uniform{color.White}, image.Point{}, draw.Src)

	drawText(img, title, W/2, 26, 28, hexColor("1F3864"), 1)
	if len(items) == 0 {
		return img
	}

	cx, cy, r := 560, 490, 300
	total := 0.0
	for _, it := range items {
		total += it.Value
	}
	if total <= 0 {
		return img
	}

	start := -math.Pi / 2 // 起始角：6 点方向，逐项逆时针推进（与 fillSector 的 y 翻转坐标系一致）
	mids := make([]float64, len(items))
	pcts := make([]float64, len(items))
	for i, it := range items {
		ang := it.Value / total * 2 * math.Pi
		end := start + ang
		mid := start + ang/2
		ocx, ocy := cx, cy
		if i == 0 { // 第一块外扩强调
			ocx = cx + int(14*math.Cos(mid))
			ocy = cy - int(14*math.Sin(mid)) // 像素 y 向下，与 fillSector 的角度约定一致
		}
		fillSector(img, ocx, ocy, r, start, end, hexColor(chartPalette[i%len(chartPalette)]))
		// 内部百分比（扇区过小时放不下，改并入外部标签文字）
		pct := it.Value / total * 100
		pctInline := pct < 6.0
		if !pctInline {
			px := ocx + int(0.62*float64(r)*math.Cos(mid))
			py := ocy - int(0.62*float64(r)*math.Sin(mid)) // 取 -sin：与扇形同一坐标系，否则标注垂直镜像落到对面扇区
			drawText(img, sprintf("%.2f%%", pct), px, py-10, 17, hexColor("FFFFFF"), 1)
		}
		mids[i] = mid
		pcts[i] = pct
		start = end
	}

	// 外部标签：按左右两侧分列排布 + 纵向防重叠，并用指示线连接到对应扇区
	const (
		labelR   = 1.32 // 标签列相对半径
		elbowR   = 1.18 // 折点半径
		anchorR  = 0.97 // 扇区边缘锚点半径
		labelTop = 160.0
		labelBot = 820.0
	)
	leftCol, rightCol := []pieLabel{}, []pieLabel{}
	for i, mid := range mids {
		ly := float64(cy) - float64(r)*math.Sin(mid) // 与扇形同坐标系
		if math.Cos(mid) < 0 {
			leftCol = append(leftCol, pieLabel{i, ly})
		} else {
			rightCol = append(rightCol, pieLabel{i, ly})
		}
	}
	layoutPieLabels(leftCol, labelTop, labelBot)
	layoutPieLabels(rightCol, labelTop, labelBot)

	lineC := hexColor("A6A6A6")
	for _, col := range [][]pieLabel{leftCol, rightCol} {
		for _, pl := range col {
			mid := mids[pl.idx]
			side, align := 1.0, 0
			if math.Cos(mid) < 0 {
				side, align = -1.0, 2
			}
			// 扇区边缘锚点 → 折点 → 标签（水平短线收尾）
			ax := cx + int(anchorR*float64(r)*math.Cos(mid))
			ay := cy - int(anchorR*float64(r)*math.Sin(mid))
			ey := int(pl.y)
			ex := cx + int(side*elbowR*float64(r))
			lx := cx + int(side*labelR*float64(r))
			drawLine(img, ax, ay, ex, ey, lineC)
			drawLine(img, ex, ey, lx-int(side*10), ey, lineC)
			label := items[pl.idx].Label
			if pcts[pl.idx] < 6.0 { // 小扇区的百分比并入外部标签
				label = sprintf("%s %.2f%%", label, pcts[pl.idx])
			}
			drawText(img, label, lx, ey-10, 19, hexColor("404040"), align)
		}
	}

	// 图例（右侧）
	ly0 := 240
	for i, it := range items {
		y := ly0 + i*44
		fillRect(img, 1090, y, 1106, y+16, hexColor(chartPalette[i%len(chartPalette)]))
		lb := sprintf("%s（%s）", it.Label, fmtNum(it.Value))
		drawText(img, lb, 1116, y-3, 19, hexColor("404040"), 0)
	}
	return img
}

// pieLabel 饼图外部标签布局项：idx 指向 items 下标，y 为标签中心线纵坐标
type pieLabel struct {
	idx int
	y   float64
}

// layoutPieLabels 纵向防重叠：按 y 排序后保证最小间距，再整体约束到 [top, bottom]
func layoutPieLabels(ls []pieLabel, top, bottom float64) {
	const minGap = 32.0
	if len(ls) == 0 {
		return
	}
	sort.Slice(ls, func(i, j int) bool { return ls[i].y < ls[j].y })
	for i := 1; i < len(ls); i++ {
		if ls[i].y-ls[i-1].y < minGap {
			ls[i].y = ls[i-1].y + minGap
		}
	}
	if over := ls[len(ls)-1].y - bottom; over > 0 {
		for i := range ls {
			ls[i].y -= over
		}
	}
	if ls[0].y < top {
		off := top - ls[0].y
		for i := range ls {
			ls[i].y += off
		}
	}
}

// drawLine 绘制任意方向线段（DDA 插值 + 2px 粗，用于饼图指示线）
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
		for ox := 0; ox < 2; ox++ {
			for oy := 0; oy < 2; oy++ {
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

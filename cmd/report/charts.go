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
		// 占比统一放在外部文本标注中，扇区内不再绘制百分比
		pct := it.Value / total * 100
		mids[i] = mid
		pcts[i] = pct
		start = end
	}

	// 外部文本标注：饼图内不显示占比，文字（名称+占比）落在第二段水平线段上，
	// 线段长度按文字宽度自动撑开；同侧标签按离圆心远近排序并拉开间隔，避免重叠。
	const (
		padX      = 14.0  // 文字与水平段两端的留白
		minGap    = 34.0  // 同侧相邻标注最小纵向间距
		minExt    = 12.0  // 径向段最小延伸长度（像素）
		maxExt    = 220.0 // 径向段最大延伸长度
		labelMinX = 15.0  // 左侧标注可延伸的最小 x
		labelMaxX = 1075.0 // 右侧标注可延伸的最大 x（避开右侧图例）
	)
	drawCol := func(inds []int) {
		if len(inds) == 0 {
			return
		}
		// 按上/下半圆分组，各自从靠近圆心的一端向外排序
		var above, below []int
		for _, i := range inds {
			if math.Sin(mids[i]) > 0 {
				above = append(above, i)
			} else {
				below = append(below, i)
			}
		}
		sort.Slice(above, func(a, b int) bool { return math.Sin(mids[above[a]]) < math.Sin(mids[above[b]]) })
		sort.Slice(below, func(a, b int) bool { return math.Sin(mids[below[a]]) > math.Sin(mids[below[b]]) })
		for _, group := range [][]int{above, below} {
			prev := math.NaN()
			for _, i := range group {
				mid := mids[i]
				side := classifySide(mid)
				c := hexColor(chartPalette[i%len(chartPalette)])
				cosM, sinM := math.Cos(mid), math.Sin(mid)
				s := -sinM // 折点 y = cy + (r+ext)·s
				// 使折点落在自然延伸位置，若与上一个标注间距不足则向外推
				desired := float64(cy) + (float64(r)+minExt)*s
				fy := desired
				if !math.IsNaN(prev) {
					if s > 0 { // 下半圆：只能向下推
						if fy < prev+minGap {
							fy = prev + minGap
						}
					} else { // 上半圆：只能向上推
						if fy > prev-minGap {
							fy = prev - minGap
						}
					}
				}
				ext := minExt
				if math.Abs(s) > 1e-6 {
					ext = (fy - float64(cy))/s - float64(r)
				}
				if ext > maxExt {
					ext = maxExt
					fy = float64(cy) + (float64(r)+ext)*s
				}
				prev = fy
				// 弧中点（扇区边缘）→ 折点：沿半径方向伸出
				ax := float64(cx) + (float64(r)-2)*cosM
				ay := float64(cy) - (float64(r)-2)*sinM
				fx := float64(cx) + (float64(r)+ext)*cosM
				// 折点 → 水平段：长度按文字宽度撑开，文字居中落在该段上
				label := sprintf("%s %.2f%%", items[i].Label, pcts[i])
				tw := float64(textWidth(label, 19))
				ex := fx + side*(tw+2*padX)
				if side > 0 && ex > labelMaxX {
					ex = labelMaxX
				}
				if side < 0 && ex < labelMinX {
					ex = labelMinX
				}
				drawLine(img, int(ax), int(ay), int(fx), int(fy), c)
				drawLine(img, int(fx), int(fy), int(ex), int(fy), c)
				// 文字居中落在水平段上（线段作下划线），加白色衬底保证压住扇区边缘时仍清晰
				tx := int((fx+ex)/2) - int(tw)/2
				fillRect(img, tx-4, int(fy)-27, tx+int(tw)+4, int(fy)-7, hexColor("FFFFFF"))
				drawText(img, label, int((fx+ex)/2), int(fy)-24, 19, c, 1)			}
		}
	}
	left, right := []int{}, []int{}
	for i, mid := range mids {
		if classifySide(mid) < 0 {
			left = append(left, i)
		} else {
			right = append(right, i)
		}
	}
	drawCol(left)
	drawCol(right)

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

// classifySide 决定饼图标注放左侧还是右侧（-1 左 / 1 右）。
// 正上方、正下方 ±~26° 区域内的扇区统一分配到固定一侧（上→右、下→左），
// 避免 cos 符号在 ±90° 附近抖动，把两个相邻小扇区的标注分到两侧而失去防重叠约束。
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

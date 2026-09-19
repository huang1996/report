package main

// docx.go —— 极简 DOCX（OOXML）生成器：段落 / 三线表 / 图片 / 页眉页脚 / 页码

import (
	"archive/zip"
	"bytes"
	"fmt"
	"image/png"
	"os"
	"strings"
)

const (
	fontName  = "微软雅黑"
	navy      = "1F3864"
	grey      = "595959"
	bandFill  = "F2F5FB"
	hdrFill   = "1F3864"
	ruleColor = "1F3864"
)

// Run 富文本片段
type Run struct {
	Text  string
	Size  float64 // pt
	Bold  bool
	Color string // hex
}

// Docx 文档构建器
type Docx struct {
	body      strings.Builder
	media     [][]byte
	imgID     int
	paraStyle strings.Builder // 复用的段落 XML 片段生成在内部函数
	forceCenter bool          // Table 期间强制所有单元格居中（不受文本长度影响）
	centerCols  map[int]bool  // Table 期间强制居中的列（按表头下标）
}

func NewDocx() *Docx {
	return &Docx{}
}

func esc(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		case '\'':
			b.WriteString("&apos;")
		default:
			if r < 0x20 && r != '\t' && r != '\n' {
				continue
			}
			b.WriteRune(r)
		}
	}
	return b.String()
}

func halfPt(size float64) string {
	return fmt.Sprintf("%d", int(size*2))
}

func (d *Docx) runXML(r Run, font string) string {
	if font == "" {
		font = fontName
	}
	size := r.Size
	if size <= 0 {
		size = 10.5
	}
	var b strings.Builder
	b.WriteString(`<w:r><w:rPr><w:rFonts w:ascii="` + font + `" w:eastAsia="` + font + `" w:hAnsi="` + font + `"/>`)
	if r.Bold {
		b.WriteString("<w:b/>")
	}
	if r.Color != "" {
		b.WriteString(`<w:color w:val="` + r.Color + `"/>`)
	}
	b.WriteString(`<w:sz w:val="` + halfPt(size) + `"/><w:szCs w:val="` + halfPt(size) + `"/></w:rPr>`)
	b.WriteString(`<w:t xml:space="preserve">` + esc(r.Text) + `</w:t></w:r>`)
	return b.String()
}

// pXML 组装段落。border=true 时加底边框（一级标题）；runsXML 为已生成的 run 片段
func (d *Docx) pXML(align string, beforePt, afterPt float64, runsXML string, border bool, indentCm float64) {
	var b strings.Builder
	b.WriteString(`<w:p><w:pPr>`)
	if align != "" {
		b.WriteString(`<w:jc w:val="` + align + `"/>`)
	}
	b.WriteString(fmt.Sprintf(`<w:spacing w:before="%d" w:after="%d"/>`, int(beforePt*20), int(afterPt*20)))
	if indentCm > 0 {
		b.WriteString(fmt.Sprintf(`<w:ind w:left="%d"/>`, int(indentCm*567)))
	}
	if border {
		b.WriteString(`<w:pBdr><w:bottom w:val="single" w:sz="8" w:space="2" w:color="` + ruleColor + `"/></w:pBdr>`)
	}
	b.WriteString(`</w:pPr>`)
	b.WriteString(runsXML)
	b.WriteString(`</w:p>`)
	d.body.WriteString(b.String())
}

func (d *Docx) Title(text string) {
	d.pXML("center", 0, 6, d.runXML(Run{Text: text, Size: 22, Bold: true, Color: navy}, ""), false, 0)
	// 标题下分隔粗线
	d.pXML("center", 0, 10, "", true, 0)
}

func (d *Docx) MetaLine(text string) {
	d.pXML("center", 0, 10, d.runXML(Run{Text: text, Size: 9.5, Color: grey}, ""), false, 0)
}

func (d *Docx) Heading(level int, text string) {
	size := 12.0
	before, after := 10.0, 6.0
	if level == 1 {
		size = 14
		before = 14
	}
	d.pXML("", before, after, d.runXML(Run{Text: text, Size: size, Bold: true, Color: navy}, ""), level == 1, 0)
}

func (d *Docx) Body(text string) {
	d.pXML("", 0, 4, d.runXML(Run{Text: text, Size: 10.5}, ""), false, 0)
}

func (d *Docx) BodyRuns(runs ...Run) {
	var b strings.Builder
	for _, r := range runs {
		b.WriteString(d.runXML(r, ""))
	}
	d.pXML("", 0, 4, b.String(), false, 0)
}

func (d *Docx) Bullet(text string) {
	var b strings.Builder
	b.WriteString(d.runXML(Run{Text: "· ", Size: 10.5, Bold: true, Color: navy}, ""))
	b.WriteString(d.runXML(Run{Text: text, Size: 10.5}, ""))
	d.pXML("", 0, 3, b.String(), false, 0.5)
}

func (d *Docx) Caption(text string) {
	d.pXML("center", 0, 10, d.runXML(Run{Text: text, Size: 9, Color: grey}, ""), false, 0)
}

func (d *Docx) Blank(n int) {
	for i := 0; i < n; i++ {
		d.pXML("", 0, 6, "", false, 0)
	}
}

// TableCentered 居中版三线表：所有单元格强制居中（用于 IP / 短文本统计表）
func (d *Docx) TableCentered(headers []string, rows [][]string, widthsCm []float64, statusCols map[int]bool, fontPt float64, hideEmptyCols bool) {
	d.forceCenter = true
	d.Table(headers, rows, widthsCm, statusCols, fontPt, hideEmptyCols)
	d.forceCenter = false
}

// TableCenterCols 指定列（按表头下标）强制居中，其余单元格按默认规则对齐
func (d *Docx) TableCenterCols(cols map[int]bool, headers []string, rows [][]string, widthsCm []float64, statusCols map[int]bool, fontPt float64, hideEmptyCols bool) {
	d.centerCols = cols
	d.Table(headers, rows, widthsCm, statusCols, fontPt, hideEmptyCols)
	d.centerCols = nil
}

// Table 三线表：宽 widthsCm（cm），statusCols 中的列按状态文字着色
func (d *Docx) Table(headers []string, rows [][]string, widthsCm []float64, statusCols map[int]bool, fontPt float64, hideEmptyCols bool) {
	if fontPt <= 0 {
		fontPt = 9.5
	}
	keep := make([]int, 0, len(headers))
	if hideEmptyCols && len(rows) > 0 {
		for i := range headers {
			used := false
			for _, r := range rows {
				if i < len(r) {
					s := strings.TrimSpace(r[i])
					if s != "" && s != "—" && s != "None" {
						used = true
						break
					}
				}
			}
			if used {
				keep = append(keep, i)
			}
		}
		if len(keep) == 0 {
			for i := range headers {
				keep = append(keep, i)
			}
		}
	} else {
		for i := range headers {
			keep = append(keep, i)
		}
	}
	// 等比压缩列宽到页面内（16.6cm 可用宽度）
	widths := make([]float64, len(keep))
	tot := 0.0
	for i, ki := range keep {
		if ki < len(widthsCm) {
			widths[i] = widthsCm[ki]
		} else {
			widths[i] = 16.6 / float64(len(keep))
		}
		tot += widths[i]
	}
	target := 16.6
	if tot > target {
		for i := range widths {
			widths[i] = widths[i] * target / tot
		}
	}

	var b strings.Builder
	b.WriteString(`<w:tbl><w:tblPr><w:tblW w:w="0" w:type="auto"/><w:jc w:val="center"/><w:tblLayout w:type="fixed"/>`)
	b.WriteString(`<w:tblBorders>`)
	b.WriteString(`<w:top w:val="single" w:sz="12" w:space="0" w:color="1F3864"/>`)
	b.WriteString(`<w:bottom w:val="single" w:sz="12" w:space="0" w:color="1F3864"/>`)
	b.WriteString(`<w:insideH w:val="single" w:sz="4" w:space="0" w:color="D9D9D9"/>`)
	b.WriteString(`<w:left w:val="nil"/><w:right w:val="nil"/><w:insideV w:val="nil"/>`)
	b.WriteString(`</w:tblBorders></w:tblPr>`)
	b.WriteString(`<w:tblGrid>`)
	for _, w := range widths {
		b.WriteString(fmt.Sprintf(`<w:gridCol w:w="%d"/>`, int(w*567)))
	}
	b.WriteString(`</w:tblGrid>`)

	cell := func(colIdx int, text string, wcm float64, bold bool, colHex, fillHex string) {
		align := "center"
		if !d.forceCenter && !d.centerCols[colIdx] && len([]rune(text)) > 14 {
			align = "left"
		}
		b.WriteString(`<w:tc><w:tcPr>`)
		b.WriteString(fmt.Sprintf(`<w:tcW w:w="%d" w:type="dxa"/>`, int(wcm*567)))
		if fillHex != "" {
			b.WriteString(`<w:shd w:val="clear" w:color="auto" w:fill="` + fillHex + `"/>`)
		}
		b.WriteString(`<w:vAlign w:val="center"/></w:tcPr>`)
		b.WriteString(`<w:p><w:pPr><w:jc w:val="` + align + `"/><w:spacing w:before="40" w:after="40"/></w:pPr>`)
		b.WriteString(d.runXML(Run{Text: text, Size: fontPt, Bold: bold, Color: colHex}, ""))
		b.WriteString(`</w:p></w:tc>`)
	}

	// 表头
	b.WriteString(`<w:tr>`)
	for i, ki := range keep {
		cell(ki, headers[ki], widths[i], true, "FFFFFF", hdrFill)
	}
	b.WriteString(`</w:tr>`)

	// 数据行
	for ri, r := range rows {
		b.WriteString(`<w:tr>`)
		for ci, ki := range keep {
			v := "—"
			if ki < len(r) && strings.TrimSpace(r[ki]) != "" {
				v = r[ki]
			}
			col := ""
			bold := false
			if statusCols[ki] {
				if c, ok := statusColor[v]; ok {
					col = c
					bold = true
				}
			}
			fill := ""
			if ri%2 == 1 {
				fill = bandFill
			}
			cell(ki, v, widths[ci], bold, col, fill)
		}
		b.WriteString(`</w:tr>`)
	}
	b.WriteString(`</w:tbl>`)
	d.body.WriteString(b.String())
	// 表后空段，避免表格粘连
	d.pXML("", 0, 2, "", false, 0)
}

// Image 插入 PNG 图片（宽 cm）
func (d *Docx) Image(data []byte, widthCm float64) error {
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return err
	}
	d.imgID++
	cx := int(widthCm * 360000)
	cy := int(float64(cx) * float64(cfg.Height) / float64(cfg.Width))
	relID := fmt.Sprintf("rIdImg%d", d.imgID)
	d.media = append(d.media, data)

	var b strings.Builder
	b.WriteString(`<w:p><w:pPr><w:jc w:val="center"/><w:spacing w:after="40"/></w:pPr><w:r><w:drawing>`)
	b.WriteString(`<wp:inline distT="0" distB="0" distL="0" distR="0">`)
	b.WriteString(fmt.Sprintf(`<wp:extent cx="%d" cy="%d"/><wp:effectExtent l="0" t="0" r="0" b="0"/>`, cx, cy))
	b.WriteString(fmt.Sprintf(`<wp:docPr id="%d" name="Picture %d"/>`, d.imgID, d.imgID))
	b.WriteString(`<wp:cNvGraphicFramePr><a:graphicFrameLocks xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" noChangeAspect="1"/></wp:cNvGraphicFramePr>`)
	b.WriteString(`<a:graphic xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><a:graphicData uri="http://schemas.openxmlformats.org/drawingml/2006/picture">`)
	b.WriteString(`<pic:pic xmlns:pic="http://schemas.openxmlformats.org/drawingml/2006/picture">`)
	b.WriteString(fmt.Sprintf(`<pic:nvPicPr><pic:cNvPr id="%d" name="img%d.png"/><pic:cNvPicPr/></pic:nvPicPr>`, d.imgID, d.imgID))
	b.WriteString(`<pic:blipFill><a:blip r:embed="` + relID + `"/><a:stretch><a:fillRect/></a:stretch></pic:blipFill>`)
	b.WriteString(fmt.Sprintf(`<pic:spPr><a:xfrm><a:off x="0" y="0"/><a:ext cx="%d" cy="%d"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom></pic:spPr>`, cx, cy))
	b.WriteString(`</pic:pic></a:graphicData></a:graphic></wp:inline></w:drawing></w:r></w:p>`)
	d.body.WriteString(b.String())
	return nil
}

func (d *Docx) Save(path string) error {
	var relsMedia strings.Builder
	for i := range d.media {
		relsMedia.WriteString(fmt.Sprintf(
			`<Relationship Id="rIdImg%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/image%d.png"/>`,
			i+1, i+1))
	}

	document := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"
 xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"
 xmlns:wp="http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing">
<w:body>` + d.body.String() + `
<w:sectPr>
<w:headerReference w:type="default" r:id="rIdHdr"/>
<w:footerReference w:type="default" r:id="rIdFtr"/>
<w:pgSz w:w="11906" w:h="16838"/>
<w:pgMar w:top="1247" w:right="1247" w:bottom="1134" w:left="1247" w:header="720" w:footer="720" w:gutter="0"/>
</w:sectPr></w:body></w:document>`

	styles := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
<w:docDefaults><w:rPrDefault><w:rPr>
<w:rFonts w:ascii="` + fontName + `" w:eastAsia="` + fontName + `" w:hAnsi="` + fontName + `"/>
<w:sz w:val="21"/><w:szCs w:val="21"/>
</w:rPr></w:rPrDefault>
<w:pPrDefault><w:pPr><w:spacing w:after="80" w:line="312" w:lineRule="auto"/></w:pPr></w:pPrDefault>
</w:docDefaults>
<w:style w:type="paragraph" w:default="1" w:styleId="Normal"><w:name w:val="Normal"/></w:style>
</w:styles>`

	header := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:hdr xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
<w:p><w:pPr><w:jc w:val="right"/></w:pPr>
<w:r><w:rPr><w:rFonts w:ascii="` + fontName + `" w:eastAsia="` + fontName + `"/><w:color w:val="` + grey + `"/><w:sz w:val="17"/></w:rPr>
<w:t>统筹运维项目</w:t></w:r></w:p></w:hdr>`

	footer := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:ftr xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
<w:p><w:pPr><w:jc w:val="center"/></w:pPr>
<w:fldSimple w:instr=" PAGE "><w:r><w:rPr><w:color w:val="` + grey + `"/><w:sz w:val="17"/></w:rPr><w:t>1</w:t></w:r></w:fldSimple>
</w:p></w:ftr>`

	contentTypes := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
<Default Extension="xml" ContentType="application/xml"/>
<Default Extension="png" ContentType="image/png"/>
<Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
<Override PartName="/word/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.styles+xml"/>
<Override PartName="/word/header1.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.header+xml"/>
<Override PartName="/word/footer1.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.footer+xml"/>
</Types>`

	rootRels := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
</Relationships>`

	docRels := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rIdHdr" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/header" Target="header1.xml"/>
<Relationship Id="rIdFtr" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/footer" Target="footer1.xml"/>
` + relsMedia.String() + `</Relationships>`

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	add := func(name, content string) error {
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		_, err = w.Write([]byte(content))
		return err
	}
	if err := add("[Content_Types].xml", contentTypes); err != nil {
		return err
	}
	if err := add("_rels/.rels", rootRels); err != nil {
		return err
	}
	if err := add("word/document.xml", document); err != nil {
		return err
	}
	if err := add("word/styles.xml", styles); err != nil {
		return err
	}
	if err := add("word/header1.xml", header); err != nil {
		return err
	}
	if err := add("word/footer1.xml", footer); err != nil {
		return err
	}
	if err := add("word/_rels/document.xml.rels", docRels); err != nil {
		return err
	}
	for i, data := range d.media {
		w, err := zw.Create(fmt.Sprintf("word/media/image%d.png", i+1))
		if err != nil {
			return err
		}
		if _, err := w.Write(data); err != nil {
			return err
		}
	}
	return zw.Close()
}

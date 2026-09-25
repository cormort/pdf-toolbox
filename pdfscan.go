package main

// 走訪 PDF 頁面與 Form XObject 的內容串流：盤點色彩空間與字型，
// 並可把 R=G=B 的 RGB 填色／筆畫改成 DeviceGray（Ghostscript 會把灰階轉成只有 K，黑字就是 K100）。

import (
	"bytes"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

type scan struct {
	xt      *model.XRefTable
	rewrite bool
	Spaces  map[string]bool // 色彩空間家族，如 DeviceRGB、ICC-CMYK
	Fonts   map[string]bool // 字型名稱 → 是否嵌入
	Changed int             // 改成灰階的次數
	Images  []imageUse      // 每一次畫圖（同一張圖畫兩次算兩筆，同 pdfimages）
	Pages   []pageBoxes
	// 透明、疊印、特別色所在頁碼
	Transparency, Overprint []int
	Spots                   map[string][]int
	SpotOrder               []string
	seen                    map[int]bool // 已處理的串流物件號（多頁共用同一串流時只改一次）
	ocOff                   map[int]bool // 預設關閉的圖層（OCG 物件號）
}

func newScan() *scan {
	return &scan{Spaces: map[string]bool{}, Fonts: map[string]bool{}, seen: map[int]bool{}, ocOff: map[int]bool{}, Spots: map[string][]int{}}
}

// scanPDF 盤點 in；out 非空時同時做單色黑改寫，有改到才寫出 out。
func scanPDF(in, out string) (s *scan, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("解析失敗：%v", r)
		}
	}()
	f, err := os.Open(in)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	ctx, err := api.ReadAndValidate(f, model.NewDefaultConfiguration())
	if err != nil {
		// 驗證太嚴（例如註解欄位格式不合）就不驗證直接讀：只要頁面樹讀得到就能盤點與改寫
		f.Seek(0, io.SeekStart)
		if ctx, err = api.ReadContext(f, model.NewDefaultConfiguration()); err == nil {
			err = ctx.EnsurePageCount()
		}
		if err != nil {
			return nil, err
		}
	}
	s = newScan()
	s.xt, s.rewrite = ctx.XRefTable, out != ""
	if cat, err := s.xt.Catalog(); err == nil {
		if off, ok := s.deref(s.dict(s.dict(cat["OCProperties"])["D"])["OFF"]).(types.Array); ok {
			for _, o := range off {
				if r, ok := o.(types.IndirectRef); ok {
					s.ocOff[r.ObjectNumber.Value()] = true
				}
			}
		}
	}
	for i := 1; i <= s.xt.PageCount; i++ {
		d, _, inh, err := s.xt.PageDict(i, false)
		if err != nil {
			return nil, err
		}
		var res types.Dict
		if inh != nil {
			res = inh.Resources
		}
		st := &gstate{}
		var page bytes.Buffer
		for _, ref := range s.contentRefs(d["Contents"]) {
			s.stream(ref, res, st)
			if sd, ok := s.deref(ref).(types.StreamDict); ok {
				page.Write(s.decoded(sd))
				page.WriteByte('\n')
			}
		}
		s.resources(res, 0)
		s.images(page.Bytes(), res, identity, i, 0)
		s.Pages = append(s.Pages, s.pageBoxes(d, inh))
		s.pageFlags(i, res)
	}
	if s.rewrite && s.Changed > 0 {
		return s, api.WriteContextFile(ctx, out)
	}
	return s, nil
}

func (s *scan) deref(o types.Object) types.Object {
	if r, ok := o.(types.IndirectRef); ok && s.xt != nil {
		v, _ := s.xt.Dereference(r)
		return v
	}
	return o
}

func (s *scan) dict(o types.Object) types.Dict {
	switch v := s.deref(o).(type) {
	case types.Dict:
		return v
	case types.StreamDict:
		return v.Dict
	}
	return nil
}

func (s *scan) contentRefs(o types.Object) []types.IndirectRef {
	switch v := o.(type) {
	case types.IndirectRef:
		if a, ok := s.deref(v).(types.Array); ok {
			return s.contentRefs(a)
		}
		return []types.IndirectRef{v}
	case types.Array:
		var refs []types.IndirectRef
		for _, e := range v {
			if r, ok := e.(types.IndirectRef); ok {
				refs = append(refs, r)
			}
		}
		return refs
	}
	return nil
}

// stream 解碼一個內容串流、盤點並視需要改寫後寫回原物件（保留多頁共用關係）。
func (s *scan) stream(ref types.IndirectRef, res types.Dict, st *gstate) {
	n := ref.ObjectNumber.Value()
	if s.seen[n] {
		return
	}
	s.seen[n] = true
	entry, ok := s.xt.FindTableEntryForIndRef(&ref)
	if !ok || entry.Object == nil {
		return
	}
	sd, ok := entry.Object.(types.StreamDict)
	if !ok {
		return
	}
	if sd.Content == nil && sd.Decode() != nil {
		return
	}
	data, changed := s.content(sd.Content, res, st)
	if data == nil {
		return
	}
	// 一律改用 Flate 重新壓縮，不必支援原本各種 filter 的編碼
	sd.Content = data
	sd.FilterPipeline = []types.PDFFilter{{Name: "FlateDecode"}}
	sd.Dict["Filter"] = types.Name("FlateDecode")
	delete(sd.Dict, "DecodeParms")
	if sd.Encode() == nil {
		entry.Object = sd
		s.Changed += changed
	}
}

// ponytail: 不走 Pattern 與 Type3 字型的內容串流，裡面的 RGB 黑不會改、也不列入盤點
func (s *scan) resources(res types.Dict, depth int) {
	if res == nil || depth > 8 {
		return
	}
	for _, cs := range s.dict(res["ColorSpace"]) {
		s.space(cs)
	}
	for _, sh := range s.dict(res["Shading"]) {
		if d := s.dict(sh); d != nil {
			s.space(d["ColorSpace"])
		}
	}
	for _, fo := range s.dict(res["Font"]) {
		s.font(fo)
	}
	for _, xo := range s.dict(res["XObject"]) {
		ref, isRef := xo.(types.IndirectRef)
		sd, ok := s.deref(xo).(types.StreamDict)
		if !ok {
			continue
		}
		switch sub, _ := sd.Dict["Subtype"].(types.Name); sub {
		case "Image":
			s.space(sd.Dict["ColorSpace"])
		case "Form":
			if !isRef || s.seen[ref.ObjectNumber.Value()] {
				continue
			}
			fres := s.dict(sd.Dict["Resources"])
			if fres == nil {
				fres = res
			}
			s.stream(ref, fres, &gstate{})
			s.resources(fres, depth+1)
		}
	}
}

// family 把色彩空間物件歸成一個家族名；ICCBased 依色版數分成 ICC-Gray／RGB／CMYK。
func (s *scan) family(o types.Object) string {
	switch v := s.deref(o).(type) {
	case types.Name:
		return string(v)
	case types.Array:
		if len(v) == 0 {
			return ""
		}
		kind, _ := s.deref(v[0]).(types.Name)
		if kind == "ICCBased" && len(v) > 1 {
			if n, ok := s.deref(s.dict(v[1])["N"]).(types.Integer); ok {
				if f, ok := map[int]string{1: "ICC-Gray", 3: "ICC-RGB", 4: "ICC-CMYK"}[int(n)]; ok {
					return f
				}
			}
		}
		return string(kind)
	}
	return ""
}

func (s *scan) space(o types.Object) {
	f := s.family(o)
	if f == "" {
		return
	}
	s.Spaces[f] = true
	// 索引色與特別色的底層／替代色彩空間也要算進去
	if a, ok := s.deref(o).(types.Array); ok {
		switch {
		case f == "Indexed" && len(a) > 1:
			s.space(a[1])
		case (f == "Separation" || f == "DeviceN") && len(a) > 2:
			s.space(a[2])
		}
	}
}

func (s *scan) font(o types.Object) {
	d := s.dict(o)
	if d == nil {
		return
	}
	name, _ := s.deref(d["BaseFont"]).(types.Name)
	sub, _ := d["Subtype"].(types.Name)
	embedded := sub == "Type3" // Type3 的字形就在檔案裡
	fd := d
	if sub == "Type0" {
		if a, ok := s.deref(d["DescendantFonts"]).(types.Array); ok && len(a) > 0 {
			fd = s.dict(a[0])
		}
	}
	if desc := s.dict(fd["FontDescriptor"]); desc != nil {
		for _, k := range []string{"FontFile", "FontFile2", "FontFile3"} {
			if _, ok := desc[k]; ok {
				embedded = true
			}
		}
	}
	key := string(name)
	if key == "" {
		key = "(" + string(sub) + ")"
	}
	if old, ok := s.Fonts[key]; ok {
		embedded = embedded && old
	}
	s.Fonts[key] = embedded
}

func (s *scan) isRGB(name string, res types.Dict) bool {
	var o types.Object = types.Name(name)
	if !strings.HasPrefix(name, "Device") {
		o = s.dict(res["ColorSpace"])[name]
	}
	switch s.family(o) {
	case "DeviceRGB", "CalRGB", "ICC-RGB":
		return true
	}
	return false
}

// ---- 內容串流 ----

type colorState struct {
	name     string // 目前色彩空間的資源名（不含 /）
	switched bool   // 輸出裡已被改成灰階，下一個非中性色要先把色彩空間設回來
}

type gstate struct {
	f, s  colorState // 填色、筆畫
	stack [][2]colorState
}

type tok struct {
	s, e  int
	num   float64
	isNum bool
	name  string
}

func isWS(c byte) bool    { return c == 0 || c == 9 || c == 10 || c == 12 || c == 13 || c == 32 }
func isDelim(c byte) bool { return strings.IndexByte("()<>[]{}/%", c) >= 0 }

func fmtNum(v float64) string {
	return strconv.FormatFloat(math.Round(v*10000)/10000, 'f', -1, 64)
}

// content 盤點一個內容串流；rewrite 時回傳改寫後的位元組（沒改到回傳 nil）。
// 只替換中性色那幾個 token，其餘位元組原樣保留。
func (s *scan) content(data []byte, res types.Dict, st *gstate) ([]byte, int) {
	var out bytes.Buffer
	last, changed := 0, 0
	replace := func(from, to int, text string) {
		out.Write(data[last:from])
		out.WriteString(text)
		last = to
	}
	neutral := func(ops []tok) (float64, bool) {
		if len(ops) != 3 || !ops[0].isNum || !ops[1].isNum || !ops[2].isNum {
			return 0, false
		}
		a, b, c := ops[0].num, ops[1].num, ops[2].num
		if math.Max(a, math.Max(b, c))-math.Min(a, math.Min(b, c)) < 0.004 {
			return (a + b + c) / 3, true
		}
		return 0, false
	}
	lex(data, func(op string, ops []tok, start, end int) {
		cur, gray, csOp := &st.f, "g", "cs"
		if strings.ContainsAny(op[:1], "RCSGK") { // 大寫開頭的色彩運算子是筆畫
			cur, gray, csOp = &st.s, "G", "CS"
		}
		switch op {
		case "q":
			st.stack = append(st.stack, [2]colorState{st.f, st.s})
		case "Q":
			if n := len(st.stack); n > 0 {
				st.f, st.s = st.stack[n-1][0], st.stack[n-1][1]
				st.stack = st.stack[:n-1]
			}
		case "rg", "RG":
			s.Spaces["DeviceRGB"] = true
			*cur = colorState{name: "DeviceRGB"}
			if v, ok := neutral(ops); ok && s.rewrite {
				replace(ops[0].s, end, fmtNum(v)+" "+gray)
				cur.switched = true
				changed++
			}
		case "g", "G":
			s.Spaces["DeviceGray"] = true
			*cur = colorState{name: "DeviceGray"}
		case "k", "K":
			s.Spaces["DeviceCMYK"] = true
			*cur = colorState{name: "DeviceCMYK"}
		case "cs", "CS":
			if len(ops) > 0 && ops[len(ops)-1].name != "" {
				name := ops[len(ops)-1].name
				*cur = colorState{name: name}
				if strings.HasPrefix(name, "Device") || name == "Pattern" {
					s.Spaces[name] = true
				} else {
					s.space(s.dict(res["ColorSpace"])[name])
				}
			}
		case "sc", "scn", "SC", "SCN":
			if cur.name == "" || !s.isRGB(cur.name, res) {
				break
			}
			if v, ok := neutral(ops); ok && s.rewrite {
				replace(ops[0].s, end, fmtNum(v)+" "+gray)
				cur.switched = true
				changed++
			} else if cur.switched {
				at := start
				if len(ops) > 0 {
					at = ops[0].s
				}
				replace(at, at, "/"+cur.name+" "+csOp+" ")
				cur.switched = false
			}
		}
	})
	if changed == 0 {
		return nil, 0
	}
	out.Write(data[last:])
	return out.Bytes(), changed
}

// lex 把內容串流切成 token，每遇到運算子就連同它的運算元呼叫 fn（start、end 是運算子本身的位置）。
// 行內圖片的 ID 也會呼叫一次（運算元就是 BI 之後的圖片字典），之後跳過二進位資料。
func lex(data []byte, fn func(op string, ops []tok, start, end int)) {
	var ops []tok
	for i := 0; i < len(data); {
		c := data[i]
		switch {
		case isWS(c):
			i++
		case c == '%':
			for i < len(data) && data[i] != '\n' && data[i] != '\r' {
				i++
			}
		case c == '(':
			start, depth := i, 0
			for ; i < len(data); i++ {
				if data[i] == '\\' {
					i++
				} else if data[i] == '(' {
					depth++
				} else if data[i] == ')' {
					if depth--; depth == 0 {
						i++
						break
					}
				}
			}
			ops = append(ops, tok{s: start, e: i})
		case (c == '<' || c == '>') && i+1 < len(data) && data[i+1] == c:
			ops = append(ops, tok{s: i, e: i + 2})
			i += 2
		case c == '<':
			start := i
			for i < len(data) && data[i] != '>' {
				i++
			}
			i++
			ops = append(ops, tok{s: start, e: i})
		case c == '/':
			start := i
			for i++; i < len(data) && !isWS(data[i]) && !isDelim(data[i]); i++ {
			}
			ops = append(ops, tok{s: start, e: i, name: string(data[start+1 : i])})
		case isDelim(c):
			ops = append(ops, tok{s: i, e: i + 1})
			i++
		default:
			start := i
			for i < len(data) && !isWS(data[i]) && !isDelim(data[i]) {
				i++
			}
			word := string(data[start:i])
			if v, err := strconv.ParseFloat(word, 64); err == nil {
				ops = append(ops, tok{s: start, e: i, num: v, isNum: true})
				continue
			}
			if word == "true" || word == "false" || word == "null" {
				ops = append(ops, tok{s: start, e: i})
				continue
			}
			fn(word, ops, start, i)
			if word == "ID" { // 跳過二進位資料直到前後都是空白的 EI
				for i++; i+1 < len(data) && !(data[i] == 'E' && data[i+1] == 'I' && isWS(data[i-1]) && (i+2 == len(data) || isWS(data[i+2]))); i++ {
				}
				i += 2
			}
			ops = ops[:0]
		}
	}
}

// ---- 圖片有效解析度 ----

type imageUse struct {
	page, w, h int
	xppi, yppi int
	kind       string // 圖片、1 位元遮色片、透明遮罩、遮罩、行內圖片
	color      string
}

type matrix [6]float64

var identity = matrix{1, 0, 0, 1, 0, 0}

// mul 回傳 m × n；PDF 的 cm 是「新 CTM = cm 矩陣 × 目前 CTM」
func (m matrix) mul(n matrix) matrix {
	return matrix{
		m[0]*n[0] + m[1]*n[2], m[0]*n[1] + m[1]*n[3],
		m[2]*n[0] + m[3]*n[2], m[2]*n[1] + m[3]*n[3],
		m[4]*n[0] + m[5]*n[2] + n[4], m[4]*n[1] + m[5]*n[3] + n[5],
	}
}

func (s *scan) num(o types.Object) float64 {
	switch v := s.deref(o).(type) {
	case types.Integer:
		return float64(v)
	case types.Float:
		return float64(v)
	}
	return 0
}

func (s *scan) decoded(sd types.StreamDict) []byte {
	if sd.Content == nil && sd.Decode() != nil {
		return nil
	}
	return sd.Content
}

// addImage 記錄一次繪圖。圖片畫在單位正方形裡，CTM 兩個基底向量的長度就是顯示寬高（pt）。
func (s *scan) addImage(page int, w, h float64, ctm matrix, kind, color string) {
	sx, sy := math.Hypot(ctm[0], ctm[1]), math.Hypot(ctm[2], ctm[3])
	if w <= 0 || h <= 0 || sx == 0 || sy == 0 {
		return
	}
	s.Images = append(s.Images, imageUse{page: page, w: int(w), h: int(h),
		xppi: int(math.Round(w * 72 / sx)), yppi: int(math.Round(h * 72 / sy)), kind: kind, color: color})
}

func (s *scan) image(page int, d types.Dict, ctm matrix) {
	kind, color := "圖片", friendly(s.family(d["ColorSpace"]))
	if b, _ := s.deref(d["ImageMask"]).(types.Boolean); b {
		kind, color = "1 位元遮色片", "—"
	}
	s.addImage(page, s.num(d["Width"]), s.num(d["Height"]), ctm, kind, color)
	// 同 pdfimages：透明遮罩與遮罩圖片也各算一張
	if m := s.dict(d["SMask"]); m != nil {
		s.addImage(page, s.num(m["Width"]), s.num(m["Height"]), ctm, "透明遮罩", "灰階")
	}
	if m, ok := s.deref(d["Mask"]).(types.StreamDict); ok {
		s.addImage(page, s.num(m.Dict["Width"]), s.num(m.Dict["Height"]), ctm, "遮罩", "—")
	}
}

// images 追蹤 CTM 走一段內容，記錄每次畫圖的有效解析度。
// Form 每被畫一次就以當下的 CTM 重走一次，同一張圖在不同地方縮放不同，ppi 也不同。
// ponytail: 同一個 Form 每次都重新解碼，一頁畫上千次的檔案才需要快取
func (s *scan) images(data []byte, res types.Dict, ctm matrix, page, depth int) {
	var stack []matrix
	var hidden []bool // 標記內容的巢狀：隱藏圖層裡的東西不會印出來，不列入
	isHidden := func() bool { return len(hidden) > 0 && hidden[len(hidden)-1] }
	lex(data, func(op string, ops []tok, _, _ int) {
		if isHidden() && (op == "Do" || op == "ID") {
			return
		}
		switch op {
		case "BMC", "BDC":
			h := isHidden()
			if op == "BDC" && len(ops) == 2 && ops[0].name == "OC" {
				h = h || s.ocHidden(s.dict(res["Properties"])[ops[1].name])
			}
			hidden = append(hidden, h)
		case "EMC":
			if len(hidden) > 0 {
				hidden = hidden[:len(hidden)-1]
			}
		case "q":
			stack = append(stack, ctm)
		case "Q":
			if n := len(stack); n > 0 {
				ctm, stack = stack[n-1], stack[:n-1]
			}
		case "cm":
			var m matrix
			if len(ops) != 6 {
				return
			}
			for i, t := range ops {
				if !t.isNum {
					return
				}
				m[i] = t.num
			}
			ctm = m.mul(ctm)
		case "Do":
			if len(ops) == 0 || ops[len(ops)-1].name == "" {
				return
			}
			sd, ok := s.deref(s.dict(res["XObject"])[ops[len(ops)-1].name]).(types.StreamDict)
			if !ok || s.ocHidden(sd.Dict["OC"]) {
				return
			}
			switch sub, _ := sd.Dict["Subtype"].(types.Name); sub {
			case "Image":
				s.image(page, sd.Dict, ctm)
			case "Form":
				s.formImages(sd, res, ctm, page, depth)
			}
		case "gs": // 軟遮罩群組裡的圖（瀏覽器列印的陰影、透明度常見），座標系是設定當下的 CTM
			if len(ops) == 0 {
				return
			}
			if g, ok := s.deref(s.dict(s.dict(s.dict(res["ExtGState"])[ops[len(ops)-1].name])["SMask"])["G"]).(types.StreamDict); ok {
				s.formImages(g, res, ctm, page, depth)
			}
		case "ID": // 行內圖片：BI 與 ID 之間是縮寫的圖片字典
			var w, h float64
			mask := false
			for i := 0; i+1 < len(ops); i += 2 {
				switch ops[i].name {
				case "W", "Width":
					w = ops[i+1].num
				case "H", "Height":
					h = ops[i+1].num
				case "IM", "ImageMask":
					mask = string(data[ops[i+1].s:ops[i+1].e]) == "true"
				}
			}
			if mask {
				s.addImage(page, w, h, ctm, "1 位元遮色片", "—")
			} else {
				s.addImage(page, w, h, ctm, "行內圖片", "—")
			}
		}
	})
}

// formImages 以 Form 自己的 /Matrix 與資源（沒有就沿用外層）走它的內容
func (s *scan) formImages(sd types.StreamDict, res types.Dict, ctm matrix, page, depth int) {
	if depth >= 8 {
		return
	}
	m := identity
	if a, ok := s.deref(sd.Dict["Matrix"]).(types.Array); ok && len(a) == 6 {
		for i := range m {
			m[i] = s.num(a[i])
		}
	}
	if r := s.dict(sd.Dict["Resources"]); r != nil {
		res = r
	}
	s.images(s.decoded(sd), res, m.mul(ctm), page, depth+1)
}

// ocHidden 依文件預設圖層設定判斷 OCG／OCMD 是否隱藏。
// ponytail: OCMD 一律當 AnyOn（預設策略），/P 其他策略與 /VE 運算式沒處理
func (s *scan) ocHidden(o types.Object) bool {
	d := s.dict(o)
	if d == nil {
		return false
	}
	if t, _ := d["Type"].(types.Name); t == "OCMD" {
		var refs []types.Object
		switch v := s.deref(d["OCGs"]).(type) {
		case types.Array:
			refs = v
		case nil:
			return false
		default:
			refs = []types.Object{d["OCGs"]}
		}
		for _, r := range refs {
			if !s.ocHidden(r) {
				return false
			}
		}
		return len(refs) > 0
	}
	r, ok := o.(types.IndirectRef)
	return ok && s.ocOff[r.ObjectNumber.Value()]
}

// ---- 頁面框 ----

type box [4]float64 // llx lly urx ury（pt）

type pageBoxes struct {
	trim, outer box
	hasTrim     bool // 頁面自己定義了 TrimBox 或 ArtBox
	rotate      int
}

func norm(b box) box {
	return box{math.Min(b[0], b[2]), math.Min(b[1], b[3]), math.Max(b[0], b[2]), math.Max(b[1], b[3])}
}

func (s *scan) boxOf(o types.Object) (box, bool) {
	a, ok := s.deref(o).(types.Array)
	if !ok || len(a) != 4 {
		return box{}, false
	}
	var b box
	for i := range b {
		b[i] = s.num(a[i])
	}
	return norm(b), true
}

// pageBoxes 同原 Python 版（pikepdf）：裁切框 TrimBox → CropBox → MediaBox；外框 BleedBox → MediaBox
func (s *scan) pageBoxes(d types.Dict, inh *model.InheritedPageAttrs) pageBoxes {
	media := box{0, 0, 612, 792} // 缺 MediaBox 時同一般閱讀器，當 Letter
	var p pageBoxes
	if inh != nil {
		if r := inh.MediaBox; r != nil {
			media = norm(box{r.LL.X, r.LL.Y, r.UR.X, r.UR.Y})
		}
		p.rotate = inh.Rotate
	}
	p.trim, p.outer = media, media
	if inh != nil && inh.CropBox != nil {
		r := inh.CropBox
		p.trim = norm(box{r.LL.X, r.LL.Y, r.UR.X, r.UR.Y})
	}
	if b, ok := s.boxOf(d["TrimBox"]); ok {
		p.trim = b
	}
	if b, ok := s.boxOf(d["BleedBox"]); ok {
		p.outer = b
	}
	// 規範：超出 MediaBox 的框以交集為準
	clip := func(b box) box {
		return box{max(b[0], media[0]), max(b[1], media[1]), min(b[2], media[2]), min(b[3], media[3])}
	}
	p.trim, p.outer = clip(p.trim), clip(p.outer)
	_, t := d["TrimBox"]
	_, a := d["ArtBox"]
	p.hasTrim = t || a
	return p
}

// ---- 透明、疊印、特別色 ----

var processColorant = map[string]bool{"All": true, "None": true, "Cyan": true, "Magenta": true, "Yellow": true, "Black": true}

// pageFlags 看頁面與其 Form 的資源字典（同原 Python 版）。
// 預檢對象是 gs 的輸出，gs 只寫出內容真的用到的資源，所以看資源就等於看實際使用。
func (s *scan) pageFlags(page int, res types.Dict) {
	transp, over := false, false
	spots := map[string]bool{}
	seen := map[int]bool{}
	numOr := func(o types.Object, def float64) float64 {
		switch v := s.deref(o).(type) {
		case types.Integer:
			return float64(v)
		case types.Float:
			return float64(v)
		}
		return def
	}
	isTrue := func(o types.Object) bool { b, _ := s.deref(o).(types.Boolean); return bool(b) }
	var walk func(res types.Dict, depth int)
	walk = func(res types.Dict, depth int) {
		if res == nil || depth > 8 {
			return
		}
		for _, g := range s.dict(res["ExtGState"]) {
			d := s.dict(g)
			bm := s.deref(d["BM"])
			if a, ok := bm.(types.Array); ok && len(a) > 0 { // 混合模式陣列：第一個是首選
				bm = s.deref(a[0])
			}
			sm, _ := s.deref(d["SMask"]).(types.Name)
			if numOr(d["ca"], 1) < 1 || numOr(d["CA"], 1) < 1 || (d["SMask"] != nil && sm != "None") ||
				(bm != nil && bm != types.Name("Normal") && bm != types.Name("Compatible")) {
				transp = true
			}
			if isTrue(d["OP"]) || isTrue(d["op"]) {
				over = true
			}
		}
		for _, xo := range s.dict(res["XObject"]) {
			sd, ok := s.deref(xo).(types.StreamDict)
			if !ok {
				continue
			}
			switch sub, _ := sd.Dict["Subtype"].(types.Name); sub {
			case "Image":
				if _, ok := sd.Dict["SMask"]; ok {
					transp = true
				}
			case "Form":
				if r, ok := xo.(types.IndirectRef); ok {
					if seen[r.ObjectNumber.Value()] {
						continue
					}
					seen[r.ObjectNumber.Value()] = true
				}
				walk(s.dict(sd.Dict["Resources"]), depth+1)
			}
		}
		for _, cs := range s.dict(res["ColorSpace"]) {
			a, ok := s.deref(cs).(types.Array)
			if !ok || len(a) < 2 {
				continue
			}
			var names []types.Object
			switch kind, _ := s.deref(a[0]).(types.Name); kind {
			case "Separation":
				names = []types.Object{a[1]}
			case "DeviceN":
				names, _ = s.deref(a[1]).(types.Array)
			}
			for _, n := range names {
				if nm, ok := s.deref(n).(types.Name); ok && !processColorant[string(nm)] {
					spots[string(nm)] = true
				}
			}
		}
	}
	walk(res, 0)
	if transp {
		s.Transparency = append(s.Transparency, page)
	}
	if over {
		s.Overprint = append(s.Overprint, page)
	}
	names := make([]string, 0, len(spots))
	for name := range spots {
		names = append(names, name)
	}
	sort.Strings(names) // 同一頁新出現的特別色依名稱排，報告順序才穩定
	for _, name := range names {
		if len(s.Spots[name]) == 0 {
			s.SpotOrder = append(s.SpotOrder, name)
		}
		s.Spots[name] = append(s.Spots[name], page)
	}
}

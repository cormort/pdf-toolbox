package main

// 走訪 PDF 頁面與 Form XObject 的內容串流：盤點色彩空間與字型，
// 並可把 R=G=B 的 RGB 填色／筆畫改成 DeviceGray（Ghostscript 會把灰階轉成只有 K，黑字就是 K100）。

import (
	"bytes"
	"fmt"
	"math"
	"os"
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
	seen    map[int]bool    // 已處理的串流物件號（多頁共用同一串流時只改一次）
}

func newScan() *scan {
	return &scan{Spaces: map[string]bool{}, Fonts: map[string]bool{}, seen: map[int]bool{}}
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
		return nil, err
	}
	s = newScan()
	s.xt, s.rewrite = ctx.XRefTable, out != ""
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
		for _, ref := range s.contentRefs(d["Contents"]) {
			s.stream(ref, res, st)
		}
		s.resources(res, 0)
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
	var ops []tok
	last, changed := 0, 0
	replace := func(from, to int, text string) {
		out.Write(data[last:from])
		out.WriteString(text)
		last = to
	}
	neutral := func() (float64, bool) {
		if len(ops) != 3 || !ops[0].isNum || !ops[1].isNum || !ops[2].isNum {
			return 0, false
		}
		a, b, c := ops[0].num, ops[1].num, ops[2].num
		if math.Max(a, math.Max(b, c))-math.Min(a, math.Min(b, c)) < 0.004 {
			return (a + b + c) / 3, true
		}
		return 0, false
	}
	operator := func(op string, start, end int) {
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
			if v, ok := neutral(); ok && s.rewrite {
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
			if v, ok := neutral(); ok && s.rewrite {
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
	}

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
			if word == "ID" { // 行內圖片：跳過二進位資料直到前後都是空白的 EI
				for i++; i+1 < len(data) && !(data[i] == 'E' && data[i+1] == 'I' && isWS(data[i-1]) && (i+2 == len(data) || isWS(data[i+2]))); i++ {
				}
				i += 2
			} else {
				operator(word, start, i)
			}
			ops = ops[:0]
		}
	}
	if changed == 0 {
		return nil, 0
	}
	out.Write(data[last:])
	return out.Bytes(), changed
}

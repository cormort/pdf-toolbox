package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

func TestImagePPI(t *testing.T) {
	img := types.StreamDict{Dict: types.Dict{"Subtype": types.Name("Image"), "Width": types.Integer(300), "Height": types.Integer(150)}}
	// Form 以 0.5 倍畫同一張圖：顯示尺寸減半，ppi 加倍
	form := types.StreamDict{Dict: types.Dict{"Subtype": types.Name("Form"),
		"Matrix": types.Array{types.Float(0.5), types.Integer(0), types.Integer(0), types.Float(0.5), types.Integer(0), types.Integer(0)}},
		Content: []byte("q 144 0 0 72 0 0 cm /Im1 Do Q")}
	res := types.Dict{"XObject": types.Dict{"Im1": img, "Fm1": form}}
	s := newScan()
	s.images([]byte("q 144 0 0 72 0 0 cm /Im1 Do Q /Fm1 Do "+
		"q 0 72 -72 0 0 0 cm BI /W 100 /H 100 /IM true ID \x00EI\x01 EI Q"), res, identity, 1, 0)
	var got []string
	for _, u := range s.Images {
		got = append(got, fmt.Sprintf("%dx%d %s", u.xppi, u.yppi, u.kind))
	}
	if want := "[150x150 圖片 300x300 圖片 100x100 1 位元遮色片]"; fmt.Sprint(got) != want {
		t.Errorf("got %v want %s", got, want)
	}
}

func TestNeutralRewrite(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"0 0 0 rg BT (Hi) Tj ET", "0 g BT (Hi) Tj ET"},
		{".5 .5 .5 RG 1 w", "0.5 G 1 w"},
		{"1 0 0 rg", ""}, // 非中性色不動
		{"(0 0 0 rg) Tj", ""},
		{"(a\\) 0 0 0 rg) Tj", ""},
		{"/DeviceRGB cs 0 0 0 sc 1 0 0 sc", "/DeviceRGB cs 0 g /DeviceRGB cs 1 0 0 sc"},
		{"0 0 0 rg 1 0 0 sc", "0 g /DeviceRGB cs 1 0 0 sc"},
		{"q 0 0 0 rg Q 1 0 0 sc", "q 0 g Q 1 0 0 sc"}, // Q 還原後不用補設色彩空間
		{"BI /W 1 /H 1 ID \x00 0 0 0 rg EI 0 0 0 rg", "BI /W 1 /H 1 ID \x00 0 0 0 rg EI 0 g"},
		{"/DeviceCMYK cs 0 0 0 1 sc", ""},
		{"0 0 0 rg 0.2 0.2 0.2 RG", "0 g 0.2 G"},
	} {
		s := newScan()
		s.rewrite = true
		got, _ := s.content([]byte(c.in), nil, &gstate{})
		if string(got) != c.want {
			t.Errorf("%q\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}

// 端到端：RGB 黑字不做前處理會變四色黑，做了之後不會。需要 Ghostscript。
func TestCMYKEndToEnd(t *testing.T) {
	workDir = t.TempDir()
	initCMYK()
	if gsPath == "" {
		t.Skip("沒有 Ghostscript")
	}
	ps := filepath.Join(workDir, "t.ps")
	os.WriteFile(ps, []byte("%!PS\n/Helvetica findfont 72 scalefont setfont\n"+
		"0 0 0 setrgbcolor 72 600 moveto (BLACK) show\n"+
		"0 0 0 setrgbcolor 72 450 moveto 500 450 lineto 20 setlinewidth stroke\n"+
		"0.2 0.4 0.8 setrgbcolor 72 300 moveto (BLUE) show showpage\n"), 0o644)
	in := filepath.Join(workDir, "in.pdf")
	if _, err := runGS(time.Minute, "-dBATCH", "-dNOPAUSE", "-dQUIET", "-sDEVICE=pdfwrite", "-sOutputFile="+in, ps); err != nil {
		t.Fatal(err)
	}

	before, err := scanPDF(in, filepath.Join(workDir, "k.pdf"))
	if err != nil || !before.Spaces["DeviceRGB"] || before.Changed == 0 {
		t.Fatalf("前處理：err=%v spaces=%v changed=%d", err, before.Spaces, before.Changed)
	}
	convert := func(src string) (string, *scan) {
		out := gsCMYK(t, src)
		s, err := scanPDF(out, "")
		if err != nil {
			t.Fatal(err)
		}
		return inspectInk(out, nil), s
	}

	ink, after := convert(in)
	if !strings.Contains(ink, "⚠️ 四色黑") || after.Spaces["DeviceRGB"] {
		t.Errorf("未前處理應有四色黑且無 RGB：%s %v", ink, after.Spaces)
	}
	ink, after = convert(filepath.Join(workDir, "k.pdf"))
	if !strings.Contains(ink, "✅ 四色黑") || after.Spaces["DeviceRGB"] || !after.Spaces["DeviceCMYK"] {
		t.Errorf("前處理後不應有四色黑：%s %v", ink, after.Spaces)
	}
	t.Log(formatFonts(after.Fonts))
}

func TestInspectBoxes(t *testing.T) {
	mm := func(v float64) float64 { return v * 72 / 25.4 }
	a4 := box{0, 0, mm(210), mm(297)}
	bleed := box{-mm(3), -mm(3), mm(213), mm(300)}
	for _, c := range []struct {
		name  string
		pages []pageBoxes
		want  []string
	}{
		{"A4 有出血", []pageBoxes{{a4, bleed, true, 0}, {a4, bleed, true, 0}, {a4, bleed, true, 0}, {a4, bleed, true, 0}},
			[]string{"✅ 成品尺寸一致：A4，共 4 頁", "✅ 出血"}},
		{"沒有裁切框", []pageBoxes{{a4, a4, false, 0}}, []string{"ℹ️ 出血：檔案未設定裁切框", "不是 4 的倍數"}},
		{"跨頁裁半、沒出血", []pageBoxes{{box{mm(210), 0, mm(420), mm(297)}, box{mm(210), 0, mm(420), mm(297)}, true, 0}},
			[]string{"❌ 出血不足 3 mm：第 1 頁（最少 0.0 mm）"}},
		{"書背側只要確認", []pageBoxes{{a4, box{-mm(3), -mm(3), mm(210), mm(300)}, true, 0}, {a4, box{0, -mm(3), mm(213), mm(300)}, true, 0}},
			[]string{"⚠️ 書背側無出血：第 1-2 頁"}},
		{"書背側之外也不足仍是錯誤", []pageBoxes{{a4, box{-mm(3), 0, mm(210), mm(300)}, true, 0}},
			[]string{"❌ 出血不足 3 mm：第 1 頁（最少 0.0 mm）"}},
		{"旋轉 90 度是橫式", []pageBoxes{{a4, bleed, true, 90}}, []string{"A4橫式"}},
		{"混用尺寸", []pageBoxes{{a4, bleed, true, 0}, {box{0, 0, mm(297), mm(420)}, box{0, 0, mm(297), mm(420)}, false, 0}},
			[]string{"⚠️ 成品尺寸不一致", "• A4：第 1 頁", "• A3：第 2 頁"}},
	} {
		got := inspectBoxes(c.pages)
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s：缺少 %q\n%s", c.name, w, got)
			}
		}
	}
}

// 貼邊：滿版色塊在沒有裁切框的頁面要警告，有裁切框（交給出血檢查）就不管
func TestEdgeCheck(t *testing.T) {
	workDir = t.TempDir()
	initCMYK()
	if gsPath == "" {
		t.Skip("沒有 Ghostscript")
	}
	ps := filepath.Join(workDir, "edge.ps")
	os.WriteFile(ps, []byte("%!PS\n0 0 0.5 0 setcmykcolor 0 0 612 100 rectfill showpage\n"), 0o644)
	pdf := filepath.Join(workDir, "edge.pdf")
	if _, err := runGS(time.Minute, "-dBATCH", "-dNOPAUSE", "-dQUIET", "-sDEVICE=pdfwrite", "-sOutputFile="+pdf, ps); err != nil {
		t.Fatal(err)
	}
	if got := inspectInk(pdf, []bool{false}); !strings.Contains(got, "⚠️ 貼邊物件：第 1 頁") {
		t.Errorf("沒有裁切框應警告貼邊：%s", got)
	}
	if got := inspectInk(pdf, []bool{true}); strings.Contains(got, "貼邊") {
		t.Errorf("有裁切框不應檢查貼邊：%s", got)
	}
}

// 多頁：串流讀 PAM 時頁碼要對得上，空白頁也不能讓後面的頁碼跑掉
func TestEdgeCheckPages(t *testing.T) {
	workDir = t.TempDir()
	initCMYK()
	if gsPath == "" {
		t.Skip("沒有 Ghostscript")
	}
	ps := filepath.Join(workDir, "edge3.ps")
	os.WriteFile(ps, []byte("%!PS\n0 0 0.5 0 setcmykcolor 0 0 612 100 rectfill showpage\n"+
		"showpage\n"+ // 第 2 頁空白
		"0 0 0.5 0 setcmykcolor 0 0 612 100 rectfill showpage\n"), 0o644)
	pdf := filepath.Join(workDir, "edge3.pdf")
	if _, err := runGS(time.Minute, "-dBATCH", "-dNOPAUSE", "-dQUIET", "-sDEVICE=pdfwrite", "-sOutputFile="+pdf, ps); err != nil {
		t.Fatal(err)
	}
	got := inspectInk(pdf, []bool{false, false, false})
	if !strings.Contains(got, "⚠️ 貼邊物件：第 1, 3 頁") {
		t.Errorf("應只有第 1、3 頁貼邊：%s", got)
	}
	if got := inspectInk(pdf, nil); strings.Contains(got, "貼邊") {
		t.Errorf("hasTrim 為 nil 時不檢查貼邊：%s", got)
	}
}

// 工作目錄只留最近用過的：上傳的檔案不該一直躺在磁碟上，但 icc/ 不能被清掉
func TestSweepJobs(t *testing.T) {
	workDir = t.TempDir()
	old, fresh := filepath.Join(workDir, randomID()), filepath.Join(workDir, randomID())
	for _, d := range []string{old, fresh} {
		os.MkdirAll(d, 0o755)
		os.WriteFile(filepath.Join(d, "in.pdf"), []byte("x"), 0o644)
	}
	icc := filepath.Join(workDir, "icc")
	os.MkdirAll(icc, 0o755)
	os.WriteFile(filepath.Join(icc, "JapanColor2011Coated.icc"), []byte("x"), 0o644)
	past := time.Now().Add(-2 * time.Hour)
	for _, p := range []string{old, filepath.Join(old, "in.pdf"), icc, filepath.Join(icc, "JapanColor2011Coated.icc")} {
		os.Chtimes(p, past, past)
	}
	sweepJobs(time.Hour)
	if fileExists(old) {
		t.Error("超過時限的工作目錄應該被刪掉")
	}
	if !fileExists(filepath.Join(fresh, "in.pdf")) {
		t.Error("新的工作目錄不該被刪掉")
	}
	if !fileExists(filepath.Join(icc, "JapanColor2011Coated.icc")) {
		t.Error("icc/ 不是工作目錄，不該被清掉")
	}
}

// gsCMYK 用與 handleCMYK 相同的參數轉檔
func gsCMYK(t *testing.T, src string) string {
	out := strings.TrimSuffix(src, ".pdf") + "_cmyk.pdf"
	if _, err := runGS(time.Minute, "-dSAFER", "--permit-file-read="+slash(iccDir)+"/", "-dBATCH", "-dNOPAUSE", "-dQUIET",
		"-sDEVICE=pdfwrite", "-dPDFSETTINGS=/prepress", "-sColorConversionStrategy=CMYK", "-dOverrideICC=true", "-dRenderIntent=1",
		"-sDefaultRGBProfile="+filepath.Join(iccDir, "sRGB_IEC61966-2-1_no_black_scaling.icc"),
		"-sOutputICCProfile="+filepath.Join(iccDir, "JapanColor2011Coated.icc"), "-sOutputFile="+out, src); err != nil {
		t.Fatal(err)
	}
	return out
}

// 透明、疊印、特別色：轉 CMYK 後仍在（gs 會保留特別色），而且要能在 Form 裡被找到
func TestPrintFlags(t *testing.T) {
	workDir = t.TempDir()
	initCMYK()
	if gsPath == "" {
		t.Skip("沒有 Ghostscript")
	}
	ps := filepath.Join(workDir, "flags.ps")
	os.WriteFile(ps, []byte(`%!PS
[/Separation (PANTONE 185 C) /DeviceCMYK {dup 0 exch dup 0.8 mul exch 0 mul 0 exch}] setcolorspace
1 setcolor 72 600 200 100 rectfill
[/DeviceN [/Cyan /Orange] /DeviceCMYK {pop 0 exch 0.6 mul 0.9 0}] setcolorspace
1 1 setcolor 72 400 200 100 rectfill showpage
true setoverprint 0 0 0 1 setcmykcolor 72 600 200 100 rectfill showpage
false setoverprint 0.5 .setfillconstantalpha 1 0 0 0 setcmykcolor 72 600 200 100 rectfill showpage
`), 0o644)
	in := filepath.Join(workDir, "flags.pdf")
	if _, err := runGS(time.Minute, "-dALLOWPSTRANSPARENCY", "-dBATCH", "-dNOPAUSE", "-dQUIET", "-sDEVICE=pdfwrite", "-sOutputFile="+in, ps); err != nil {
		t.Fatal(err)
	}
	s, err := scanPDF(gsCMYK(t, in), "")
	if err != nil {
		t.Fatal(err)
	}
	got := inspectFlags(s)
	for _, want := range []string{"⚠️ 透明效果：第 3 頁", "⚠️ 疊印：第 2 頁有", "⚠️ 特別色：Orange（第 1 頁）；PANTONE 185 C（第 1 頁）"} {
		if !strings.Contains(got, want) {
			t.Errorf("缺少 %q\n%s", want, got)
		}
	}
}

func TestTextLines(t *testing.T) {
	s := newScan()
	// 第 1 頁：10 pt 字縮成一半（5 pt）、0.1 pt 線、表格細框（矩形 0.2 × 0.5 放大 1 倍仍 < 0.25）
	s.textLines([]byte("q .5 0 0 .5 0 0 cm BT /F1 10 Tf (a) Tj ET Q 0.1 w 0 0 m 10 0 l S 0 0 100 0.2 re f"), nil, 1, 1, 0)
	// 第 2 頁：線寬 0；隱形 OCR 層的小字不算
	s.textLines([]byte("0 w 0 0 m 1 1 l S BT 3 Tr /F1 2 Tf (x) Tj ET"), nil, 1, 2, 0)
	got := inspectTextLines(s)
	for _, want := range []string{"⚠️ 小字：第 1 頁有小於 6 pt 的文字（最小 5.0 pt）", "❌ 極細線：第 2 頁", "⚠️ 細線：第 1 頁"} {
		if !strings.Contains(got, want) {
			t.Errorf("缺少 %q\n%s", want, got)
		}
	}
}

// pdf-lib 嵌入的中文字型經 gs 會掉 ToUnicode（文字無法搜尋），restoreToUnicode 要補回來。
// testdata/pdflib_cjk.pdf 由 pdf-lib 產生（Identity-H 的 CID 字型）。
func TestRestoreToUnicode(t *testing.T) {
	workDir = t.TempDir()
	initCMYK()
	if gsPath == "" {
		t.Skip("沒有 Ghostscript")
	}
	out := filepath.Join(workDir, "out.pdf")
	if _, err := runGS(time.Minute, "-dSAFER", "-dBATCH", "-dNOPAUSE", "-dQUIET", "-sDEVICE=pdfwrite", "-dPDFSETTINGS=/ebook", "-sOutputFile="+out, "testdata/pdflib_cjk.pdf"); err != nil {
		t.Fatal(err)
	}
	missing := func() int {
		ctx, err := readPDF(out)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, d := range cidFonts(ctx.XRefTable) {
			if _, ok := d["ToUnicode"]; !ok {
				n++
			}
		}
		return n
	}
	// 小檔經 gs 不一定會掉（實際會掉的是未子集化的整個字型，檔案太大不放進 repo），所以自己拿掉來模擬
	ctx, err := readPDF(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range cidFonts(ctx.XRefTable) {
		delete(d, "ToUnicode")
	}
	if err := api.WriteContextFile(ctx, out); err != nil || missing() == 0 {
		t.Fatalf("模擬失敗：%v", err)
	}
	if n, err := restoreToUnicode("testdata/pdflib_cjk.pdf", out); err != nil || n == 0 {
		t.Fatalf("補回 %d 個，err=%v", n, err)
	}
	if m := missing(); m != 0 {
		t.Errorf("仍有 %d 個字型缺 ToUnicode", m)
	}
}

// 密碼保護：加密後預設只有權限密碼能移除；勾選 openOnly 才改用開啟密碼（含列印保護的檔案）
func TestProtect(t *testing.T) {
	workDir = t.TempDir()
	post := func(path string, fields map[string]string) fileResult {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		fw, _ := mw.CreateFormFile("file", "測試.pdf")
		b, _ := os.ReadFile(path)
		fw.Write(b)
		for k, v := range fields {
			mw.WriteField(k, v)
		}
		mw.Close()
		req := httptest.NewRequest("POST", "/api/protect", &body)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		rec := httptest.NewRecorder()
		handleProtect(rec, req)
		var res fileResult
		json.Unmarshal(rec.Body.Bytes(), &res)
		return res
	}
	outOf := func(res fileResult) string {
		return filepath.Join(workDir, strings.Split(res.Download, "/")[3], "out.pdf")
	}

	enc := post("testdata/pdflib_cjk.pdf", map[string]string{"mode": "encrypt", "userPW": "u1", "ownerPW": "o1", "print": "1"})
	if !enc.OK || !isEncrypted(outOf(enc)) {
		t.Fatalf("加密失敗：%+v", enc)
	}
	if out, _ := exec.Command("pdfinfo", "-upw", "u1", outOf(enc)).Output(); len(out) > 0 && !strings.Contains(string(out), "print:yes copy:no") {
		t.Errorf("權限不對：%s", out)
	}
	for pw, ok := range map[string]bool{"o1": true, "u1": false, "": false, "wrong": false} {
		if res := post(outOf(enc), map[string]string{"mode": "decrypt", "password": pw}); res.OK != ok || (ok && isEncrypted(outOf(res))) {
			t.Errorf("用 %q 移除：%+v", pw, res)
		}
	}
	ownerOnly := post("testdata/pdflib_cjk.pdf", map[string]string{"mode": "encrypt", "ownerPW": "o1", "print": "0"})
	if res := post(outOf(ownerOnly), map[string]string{"mode": "decrypt", "password": ""}); res.OK {
		t.Error("只有權限密碼的檔案不應該不用密碼就移除")
	}
	if res := post(outOf(ownerOnly), map[string]string{"mode": "decrypt", "password": "o1"}); !res.OK || isEncrypted(outOf(res)) {
		t.Errorf("列印保護的檔案應該能用權限密碼移除：%+v", res)
	}

	// 明確勾選「我沒有權限密碼」之後才改用開啟密碼；不勾就還是一律要權限密碼
	if res := post(outOf(ownerOnly), map[string]string{"mode": "decrypt", "password": "", "openOnly": "1"}); !res.OK || isEncrypted(outOf(res)) {
		t.Errorf("勾選後，只鎖列印的檔案留空就該解得開：%+v", res)
	}
	if res := post(outOf(enc), map[string]string{"mode": "decrypt", "password": "u1", "openOnly": "1"}); !res.OK || isEncrypted(outOf(res)) {
		t.Errorf("勾選後應該能用開啟密碼移除：%+v", res)
	}
	if res := post(outOf(enc), map[string]string{"mode": "decrypt", "password": "wrong", "openOnly": "1"}); res.OK {
		t.Error("勾選後給錯的開啟密碼不該成功")
	}
	if res := post(outOf(enc), map[string]string{"mode": "decrypt", "password": "u1"}); res.OK {
		t.Error("沒勾選時用開啟密碼不該被接受")
	}
	if res := post(outOf(enc), map[string]string{"mode": "encrypt", "userPW": "x"}); res.OK {
		t.Error("已加密的檔案不應再加密")
	}

	// 三態權限：只允許「低解析度列印／無障礙讀取／組合頁面／填表單」時，只會設對應的單一位元
	draft := post("testdata/pdflib_cjk.pdf", map[string]string{"mode": "encrypt", "ownerPW": "o1",
		"print": "draft", "copy": "a11y", "edit": "assemble", "annot": "fill"})
	if !draft.OK {
		t.Fatalf("三態權限加密失敗：%+v", draft)
	}
	ctx, err := readPDF(outOf(draft))
	if err != nil {
		t.Fatal(err)
	}
	got := model.PermissionFlags(ctx.E.P)
	for _, c := range []struct {
		name string
		bit  model.PermissionFlags
		want bool
	}{
		{"低解析度列印", model.PermissionPrintRev2, true},
		{"高品質列印", model.PermissionPrintRev3, false},
		{"無障礙讀取", model.PermissionExtractRev3, true},
		{"複製文字與圖片", model.PermissionExtract, false},
		{"組合頁面", model.PermissionAssembleRev3, true},
		{"修改內容", model.PermissionModify, false},
		{"填表單", model.PermissionFillRev3, true},
		{"加註解", model.PermissionModAnnFillForm, false},
	} {
		if (got&c.bit != 0) != c.want {
			t.Errorf("%s 位元不對（P=0x%04X）", c.name, int(got))
		}
	}

	// 全部不允許：列印的兩個位元都不能留
	none := post("testdata/pdflib_cjk.pdf", map[string]string{"mode": "encrypt", "ownerPW": "o1",
		"print": "none", "copy": "none", "edit": "none", "annot": "none"})
	if ctx, err := readPDF(outOf(none)); err != nil {
		t.Fatal(err)
	} else if ctx.E.P&int(model.PermissionPrintRev2|model.PermissionPrintRev3) != 0 {
		t.Errorf("全部不允許時不該有列印位元（P=0x%04X）", ctx.E.P)
	}

	// 加密方式：AES-256 是 V5，AES-128 是 V4
	for bits, wantV := range map[string]int{"256": 5, "128": 4} {
		res := post("testdata/pdflib_cjk.pdf", map[string]string{"mode": "encrypt", "ownerPW": "o1", "aes": bits})
		ctx, err := readPDF(outOf(res))
		if err != nil {
			t.Fatal(err)
		}
		if ctx.E.V != wantV {
			t.Errorf("AES-%s 的 V=%d，想要 %d", bits, ctx.E.V, wantV)
		}
	}
}

func TestParsePages(t *testing.T) {
	for spec, want := range map[string]string{
		"": "[1 2 3 4 5]", "2": "[2]", "1-3": "[1 2 3]", "4-": "[4 5]", "5,1 , 3": "[1 3 5]",
		"2-9": "[2 3 4 5]", "3，1～2": "[1 2 3]", "1-2,2-3": "[1 2 3]",
	} {
		if got, err := parsePages(spec, 5); err != nil || fmt.Sprint(got) != want {
			t.Errorf("%q → %v %v，想要 %s", spec, got, err, want)
		}
	}
	for _, bad := range []string{"a", "9", "0", "1-x"} {
		if _, err := parsePages(bad, 5); err == nil {
			t.Errorf("%q 應該報錯", bad)
		}
	}
	// 前後相反的範圍要講清楚，不要只說「不在 1 到 5 頁之間」
	if _, err := parsePages("5-2", 5); err == nil || !strings.Contains(err.Error(), "前後相反") {
		t.Errorf("5-2 應該說前後相反：%v", err)
	}
}

// 3 頁 PDF 取第 2-3 頁 → ZIP 裡依實際頁碼命名；只取 1 頁 → 直接給圖
func TestImages(t *testing.T) {
	workDir = t.TempDir()
	initCMYK()
	if gsPath == "" {
		t.Skip("沒有 Ghostscript")
	}
	ps := filepath.Join(workDir, "3p.ps")
	os.WriteFile(ps, []byte("%!PS\n1 1 3 { pop 72 72 100 100 rectfill showpage } for\n"), 0o644)
	pdf := filepath.Join(workDir, "3p.pdf")
	if _, err := runGS(time.Minute, "-dBATCH", "-dNOPAUSE", "-dQUIET", "-sDEVICE=pdfwrite", "-sOutputFile="+pdf, ps); err != nil {
		t.Fatal(err)
	}
	post := func(fields map[string]string) fileResult {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		fw, _ := mw.CreateFormFile("file", "報告.pdf")
		b, _ := os.ReadFile(pdf)
		fw.Write(b)
		for k, v := range fields {
			mw.WriteField(k, v)
		}
		mw.Close()
		req := httptest.NewRequest("POST", "/api/images", &body)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		rec := httptest.NewRecorder()
		handleImages(rec, req)
		var res fileResult
		json.Unmarshal(rec.Body.Bytes(), &res)
		return res
	}
	res := post(map[string]string{"pages": "2-", "dpi": "72", "format": "png"})
	if !res.OK || !strings.HasSuffix(res.Download, url.PathEscape("報告_2張圖.zip")) {
		t.Fatalf("%+v", res)
	}
	zr, err := zip.OpenReader(filepath.Join(workDir, strings.Split(res.Download, "/")[3], "out.zip"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	zr.Close()
	if fmt.Sprint(names) != "[報告_p002.png 報告_p003.png]" {
		t.Errorf("ZIP 內容：%v", names)
	}
	if res := post(map[string]string{"pages": "3", "format": "jpg"}); !res.OK || !strings.HasSuffix(res.Download, url.PathEscape("報告_p003.jpg")) {
		t.Errorf("單頁：%+v", res)
	}
}

package main

// RGB → CMYK：單色黑前處理（pdfscan.go）→ Ghostscript 以 Japan Color 2011 Coated 轉換 → 印刷預檢。

import (
	"bytes"
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

//go:embed icc/*.icc
var iccFS embed.FS

// 印刷廠沒指定規格時，採台灣一般平版四色印刷（銅版紙）常見的送印要求
const (
	cmykProfileName = "Japan Color 2011 Coated"
	bleedMM         = 3   // 出血
	tacLimit        = 300 // 總墨量上限（C+M+Y+K，%）
	convertTimeout  = 5 * time.Minute
	inspectTimeout  = 90 * time.Second
)

var gsPath, gsVersion, iccDir string

func initCMYK() {
	gsPath = findGS()
	if gsPath != "" {
		out, _ := runGS(10*time.Second, "--version")
		gsVersion = strings.TrimSpace(string(out))
	}
	log.Printf("Ghostscript：%q %s", gsPath, gsVersion)
	iccDir = filepath.Join(workDir, "icc")
	os.MkdirAll(iccDir, 0o755)
	entries, _ := iccFS.ReadDir("icc")
	for _, e := range entries {
		b, _ := iccFS.ReadFile("icc/" + e.Name())
		os.WriteFile(filepath.Join(iccDir, e.Name()), b, 0o644)
	}
}

// 可攜版優先用 exe 旁邊的 gs\bin\gswin64c.exe，其次找 PATH（開發機上的 gs）
func findGS() string {
	if p := filepath.Join(exeDir, "gs", "bin", "gswin64c.exe"); fileExists(p) {
		return p
	}
	for _, n := range []string{"gswin64c", "gs"} {
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
	}
	return ""
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// Ghostscript 參數一律用正斜線：Windows 接受，也避開 gs 對反斜線的跳脫處理
func slash(p string) string { return filepath.ToSlash(p) }

func runGS(timeout time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, gsPath, args...)
	hideWindow(cmd)
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return out, fmt.Errorf("超過 %s 未完成", timeout)
	}
	if err != nil {
		return out, fmt.Errorf("%v\n%s", err, bytes.TrimSpace(out))
	}
	return out, nil
}

type cmykResult struct {
	OK       bool     `json:"ok"`
	Report   []string `json:"report"`
	Download string   `json:"download,omitempty"`
}

func handleCMYK(w http.ResponseWriter, r *http.Request) {
	res := cmykResult{}
	defer func() { w.Header().Set("Content-Type", "application/json"); json.NewEncoder(w).Encode(res) }()
	add := func(format string, a ...any) { res.Report = append(res.Report, fmt.Sprintf(format, a...)) }

	if gsPath == "" {
		add("%s", noGS)
		return
	}
	id, dir, in, name, msg := receivePDF(w, r)
	if msg != "" {
		add("%s", msg)
		return
	}

	src := in
	var before *scan
	var err error
	if r.FormValue("forceK") == "1" {
		k := filepath.Join(dir, "k.pdf")
		before, err = scanPDF(in, k)
		switch {
		case err != nil:
			add("⚠️ 單色黑前處理失敗，改用原檔轉換：%v", err)
		case before.Changed == 0:
			add("ℹ️ 單色黑前處理：沒有找到需要改寫的 RGB 黑色或灰色文字／線條。")
		default:
			src = k
			add("✅ 單色黑前處理：已將 %d 處 RGB 黑色／灰色文字、線條與色塊改為單色黑（只用 K 版）。", before.Changed)
		}
	} else {
		before, err = scanPDF(in, "")
	}
	if err == nil {
		res.Report = append([]string{"【轉換前】\n" + formatSpaces(before.Spaces)}, res.Report...)
	}

	out := filepath.Join(dir, "out.pdf")
	if _, err := runGS(convertTimeout, "-dSAFER", "--permit-file-read="+slash(iccDir)+"/",
		"-dBATCH", "-dNOPAUSE", "-dQUIET", "-sDEVICE=pdfwrite",
		// /prepress 已含 PDF 1.7、字型嵌入與子集化、彩色／灰階 300 ppi、單色 1200 ppi；
		// 它預設 LeaveColorUnchanged，所以色彩轉換要另外指定
		"-dPDFSETTINGS=/prepress", "-sColorConversionStrategy=CMYK",
		"-dOverrideICC=true", "-dRenderIntent=1", // 以承印廠指定的 ICC 為準
		"-sDefaultRGBProfile="+slash(filepath.Join(iccDir, "sRGB_IEC61966-2-1_no_black_scaling.icc")),
		"-sOutputICCProfile="+slash(filepath.Join(iccDir, "JapanColor2011Coated.icc")),
		"-sOutputFile="+slash(out), slash(src)); err != nil || !fileExists(out) {
		add("❌ Ghostscript 轉換失敗：%v", err)
		return
	}
	restoreToUnicode(src, out) // gs 會丟掉部分中文字型的文字對應，補回來才搜尋得到
	st, _ := os.Stat(out)
	add("✅ CMYK 轉換完成\n📦 檔案大小：%.2f MB\n🔧 Ghostscript：%s\n📐 ICC Profile：%s\n🖼️ 彩色／灰階圖片上限 300 ppi，單色 1200 ppi\n🔤 字型：要求嵌入及子集化",
		float64(st.Size())/1024/1024, gsVersion, cmykProfileName)

	after, err := scanPDF(out, "")
	if err != nil {
		add("⚠️ 無法分析輸出檔：%v", err)
		add("%s", printerReport(inspectInk(out, dir, nil)))
	} else {
		add("【轉換後】\n%s\n%s", formatSpaces(after.Spaces), evaluateSpaces(after.Spaces))
		hasTrim := make([]bool, len(after.Pages))
		for i, p := range after.Pages {
			hasTrim[i] = p.hasTrim
		}
		add("%s", printerReport(inspectBoxes(after.Pages), inspectInk(out, dir, hasTrim), inspectFlags(after), inspectTextLines(after)))
		add("%s", formatImages(after.Images))
		add("%s", formatFonts(after.Fonts))
	}
	add("⚠️ 本工具產生的是指定 ICC Profile 的 CMYK PDF，不等同於已通過 PDF/X 認證。\n⚠️ 若原始圖片解析度不足，轉成 300 ppi 不會憑空增加細節。")

	res.OK = true
	res.Download = "/api/file/" + id + "/" + url.PathEscape(name+"_cmyk.pdf")
}

const noGS = "❌ 找不到 Ghostscript：請把 gs 資料夾放在 PdfToolbox.exe 旁邊（gs\\bin\\gswin64c.exe）。"

// receivePDF 收下上傳的 PDF，存成工作目錄裡的 in.pdf。
// 暫存檔一律用 ASCII 檔名，原檔名（不含副檔名）只用在下載名稱，避開中文路徑的問題。
// 失敗時 msg 是給使用者看的訊息。
func receivePDF(w http.ResponseWriter, r *http.Request) (id, dir, in, name, msg string) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<30)
	file, hdr, err := r.FormFile("file")
	if err != nil {
		return "", "", "", "", "❌ 沒有收到檔案：" + err.Error()
	}
	defer file.Close()
	if !strings.EqualFold(filepath.Ext(hdr.Filename), ".pdf") {
		return "", "", "", "", "❌ 只支援 PDF；Word 檔請先在 Word 另存成 PDF（版面最準）。"
	}
	id = randomID()
	dir = filepath.Join(workDir, id)
	os.MkdirAll(dir, 0o755)
	in = filepath.Join(dir, "in.pdf")
	if err := saveTo(in, file); err != nil {
		return "", "", "", "", "❌ 無法儲存上傳檔：" + err.Error()
	}
	return id, dir, in, strings.TrimSuffix(hdr.Filename, filepath.Ext(hdr.Filename)), ""
}

func handleFile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := hex.DecodeString(id); err != nil || len(id) != 16 {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(r.PathValue("name")))
	http.ServeFile(w, r, filepath.Join(workDir, id, "out.pdf"))
}

func randomID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func saveTo(path string, r io.Reader) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// ---- 報告 ----

var friendlySpace = map[string]string{
	"DeviceRGB": "RGB", "CalRGB": "RGB（校準）", "ICC-RGB": "RGB（ICC）",
	"DeviceCMYK": "CMYK", "ICC-CMYK": "CMYK（ICC）",
	"DeviceGray": "灰階", "CalGray": "灰階（校準）", "ICC-Gray": "灰階（ICC）",
	"Separation": "特別色", "DeviceN": "DeviceN／多色版", "Indexed": "索引色",
	"ICCBased": "ICC 色彩空間", "Pattern": "圖樣", "Lab": "Lab",
}

func friendly(family string) string {
	if f, ok := friendlySpace[family]; ok {
		return f
	}
	return family
}

func formatSpaces(spaces map[string]bool) string {
	if len(spaces) == 0 {
		return "📄 未偵測到明確色彩空間；文件可能主要為純文字或預設黑色物件。"
	}
	var names []string
	for k := range spaces {
		names = append(names, friendly(k))
	}
	sort.Strings(names)
	return "🎨 偵測到的色彩空間：" + strings.Join(names, "、")
}

func evaluateSpaces(spaces map[string]bool) string {
	if spaces["DeviceRGB"] || spaces["CalRGB"] || spaces["ICC-RGB"] {
		return "⚠️ 色彩預檢：輸出仍偵測到 RGB。請勿僅依此判定可直接付印，建議再用專業預檢軟體確認。"
	}
	if spaces["DeviceCMYK"] || spaces["ICC-CMYK"] {
		return "✅ 色彩預檢：未偵測到 RGB，並已偵測到 CMYK。"
	}
	return "ℹ️ 色彩預檢：未偵測到 RGB，也未辨識出明確 CMYK；文件可能主要為灰階或純文字。"
}

func formatFonts(fonts map[string]bool) string {
	if len(fonts) == 0 {
		return "ℹ️ 字型預檢：沒有字型資源。"
	}
	var missing []string
	for name, emb := range fonts {
		if !emb {
			missing = append(missing, "• "+name)
		}
	}
	if len(missing) == 0 {
		return fmt.Sprintf("✅ 字型預檢：共 %d 個字型，均已嵌入。", len(fonts))
	}
	sort.Strings(missing)
	if len(missing) > 15 {
		missing = append(missing[:15], fmt.Sprintf("• 另有 %d 個", len(missing)-15))
	}
	return fmt.Sprintf("❌ 字型預檢：%d 個字型未嵌入。\n%s", len(missing), strings.Join(missing, "\n"))
}

const (
	warnPPI     = 300
	criticalPPI = 200
)

func formatImages(imgs []imageUse) string {
	if len(imgs) == 0 {
		return "ℹ️ 圖片解析度預檢：未偵測到點陣圖片，文件可能主要由文字或向量圖形構成。"
	}
	low := func(u imageUse) int { return min(u.xppi, u.yppi) }
	sorted := append([]imageUse(nil), imgs...)
	sort.SliceStable(sorted, func(i, j int) bool { return low(sorted[i]) < low(sorted[j]) })
	warn, crit := 0, 0
	for _, u := range sorted {
		if low(u) < warnPPI {
			warn++
		}
		if low(u) < criticalPPI {
			crit++
		}
	}
	level := fmt.Sprintf("✅ 所有偵測到的圖片均達 %d ppi。", warnPPI)
	if crit > 0 {
		level = fmt.Sprintf("❌ 有圖片低於 %d ppi，可能不適合高品質印刷。", criticalPPI)
	} else if warn > 0 {
		level = fmt.Sprintf("⚠️ 有圖片介於 %d 至 %d ppi，建議人工確認。", criticalPPI, warnPPI-1)
	}
	lines := []string{"🖼️ 圖片解析度預檢", level,
		fmt.Sprintf("圖片總數：%d（同一張圖畫在不同位置分開計）", len(imgs)),
		fmt.Sprintf("最低有效解析度：%d ppi", low(sorted[0])),
		fmt.Sprintf("低於 %d ppi：%d 張", warnPPI, warn),
		fmt.Sprintf("低於 %d ppi：%d 張", criticalPPI, crit)}
	for _, u := range sorted[:min(warn, 15)] {
		lines = append(lines, fmt.Sprintf("• 第 %d 頁：%d × %d ppi（%d × %d 像素）；%s；%s", u.page, u.xppi, u.yppi, u.w, u.h, u.kind, u.color))
	}
	if warn > 15 {
		lines = append(lines, fmt.Sprintf("• 另有 %d 張低於 %d ppi。", warn-15, warnPPI))
	}
	return strings.Join(lines, "\n")
}

// formatPages 把頁碼整理成「1-3, 5, 8」，最多列 12 段。pages 需已排序。
func formatPages(pages []int) string {
	var ranges []string
	for i := 0; i < len(pages); {
		j := i
		for j+1 < len(pages) && pages[j+1] == pages[j]+1 {
			j++
		}
		if i == j {
			ranges = append(ranges, fmt.Sprint(pages[i]))
		} else {
			ranges = append(ranges, fmt.Sprintf("%d-%d", pages[i], pages[j]))
		}
		i = j + 1
	}
	if len(ranges) > 12 {
		return strings.Join(ranges[:12], ", ") + fmt.Sprintf(" 等，共 %d", len(pages))
	}
	return strings.Join(ranges, ", ")
}

const ptPerMM = 72 / 25.4

// 常見成品尺寸（mm，直式）
var knownSizes = []struct {
	w, h float64
	name string
}{{210, 297, "A4"}, {297, 420, "A3"}, {148, 210, "A5"}, {182, 257, "B5"}, {257, 364, "B4"}, {190, 260, "16 開"}}

func describePageSize(w, h float64) string {
	short, long := math.Round(math.Min(w, h)), math.Round(math.Max(w, h))
	for _, k := range knownSizes {
		if math.Abs(short-k.w) <= 1 && math.Abs(long-k.h) <= 1 {
			if w > h {
				return k.name + "橫式"
			}
			return k.name
		}
	}
	return fmt.Sprintf("%.0f × %.0f mm", w, h)
}

// inspectBoxes 檢查成品尺寸是否一致、出血是否足夠。
func inspectBoxes(pages []pageBoxes) string {
	var order []string
	sizes := map[string][]int{}
	var short, spine []int
	least, anyTrim := math.Inf(1), false
	for i, p := range pages {
		w, h := (p.trim[2]-p.trim[0])/ptPerMM, (p.trim[3]-p.trim[1])/ptPerMM
		if p.rotate%180 != 0 { // 以顯示方向判斷直式／橫式
			w, h = h, w
		}
		size := describePageSize(w, h)
		if _, ok := sizes[size]; !ok {
			order = append(order, size)
		}
		sizes[size] = append(sizes[size], i+1)
		left, bottom := (p.trim[0]-p.outer[0])/ptPerMM, (p.trim[1]-p.outer[1])/ptPerMM
		right, top := (p.outer[2]-p.trim[2])/ptPerMM, (p.outer[3]-p.trim[3])/ptPerMM
		enough := func(v float64) bool { return v >= bleedMM-0.1 }
		switch {
		case enough(left) && enough(bottom) && enough(right) && enough(top):
		case enough(bottom) && enough(top) && enough(left) != enough(right):
			spine = append(spine, i+1) // 只有左或右一側不足：多半是書本內頁的裝訂側
		default:
			short = append(short, i+1)
			least = min(least, max(min(left, bottom, right, top), 0))
		}
		anyTrim = anyTrim || p.hasTrim
	}

	var lines []string
	if len(order) == 1 {
		lines = append(lines, fmt.Sprintf("✅ 成品尺寸一致：%s，共 %d 頁。", order[0], len(pages)))
	} else {
		lines = append(lines, "⚠️ 成品尺寸不一致，請確認是否刻意混用：")
		for _, size := range order {
			lines = append(lines, fmt.Sprintf("• %s：第 %s 頁", size, formatPages(sizes[size])))
		}
	}
	switch {
	case len(short) == 0 && len(spine) == 0:
		lines = append(lines, fmt.Sprintf("✅ 出血：每頁皆有至少 %d mm。", bleedMM))
	case !anyTrim:
		lines = append(lines, fmt.Sprintf("ℹ️ 出血：檔案未設定裁切框（TrimBox），視為無出血。若版面沒有滿版底色或貼邊圖片可以不用出血；有的話請加 %d mm 出血（見下方貼邊檢查）。", bleedMM))
	default:
		if len(short) > 0 {
			lines = append(lines, fmt.Sprintf("❌ 出血不足 %d mm：第 %s 頁（最少 %.1f mm）。", bleedMM, formatPages(short), least))
		}
		if len(spine) > 0 {
			lines = append(lines, fmt.Sprintf("⚠️ 書背側無出血：第 %s 頁只有左或右一側沒有出血。書本內頁的裝訂側不用出血；若是單張印刷品，這一側也要加 %d mm。", formatPages(spine), bleedMM))
		}
	}
	if len(pages)%4 != 0 {
		lines = append(lines, fmt.Sprintf("ℹ️ 總頁數 %d 不是 4 的倍數；若採騎馬釘裝訂需補空白頁（膠裝不受影響）。", len(pages)))
	}
	return strings.Join(lines, "\n")
}

// inspectFlags 報告透明效果、疊印與特別色。
func inspectFlags(s *scan) string {
	var lines []string
	if len(s.Transparency) > 0 {
		lines = append(lines, fmt.Sprintf("⚠️ 透明效果：第 %s 頁使用半透明、陰影或混合模式；較舊的印刷廠輸出系統（RIP）可能需要先平面化，請向印刷廠確認。", formatPages(s.Transparency)))
	} else {
		lines = append(lines, "✅ 透明效果：未使用。")
	}
	if len(s.Overprint) > 0 {
		lines = append(lines, fmt.Sprintf("⚠️ 疊印：第 %s 頁有設定疊印；白色物件若設為疊印，印出來會消失，請確認是刻意設定。", formatPages(s.Overprint)))
	} else {
		lines = append(lines, "✅ 疊印：未設定。")
	}
	if len(s.SpotOrder) > 0 {
		var spots []string
		for _, name := range s.SpotOrder {
			spots = append(spots, fmt.Sprintf("%s（第 %s 頁）", name, formatPages(s.Spots[name])))
		}
		lines = append(lines, "⚠️ 特別色："+strings.Join(spots, "；")+"。每個特別色會多出一個色版，若不是要印特別色，請改為 CMYK。")
	} else {
		lines = append(lines, "✅ 特別色：未使用，只有 CMYK 四色版。")
	}
	return strings.Join(lines, "\n")
}

// inspectTextLines 報告小於最小字級的文字與過細的線條。
func inspectTextLines(s *scan) string {
	pages := func(m map[int]bool) []int {
		var a []int
		for p := range m {
			a = append(a, p)
		}
		sort.Ints(a)
		return a
	}
	var lines []string
	if len(s.SmallText) > 0 {
		var ps []int
		least := math.Inf(1)
		for p, v := range s.SmallText {
			ps = append(ps, p)
			least = min(least, v)
		}
		sort.Ints(ps)
		lines = append(lines, fmt.Sprintf("⚠️ 小字：第 %s 頁有小於 %d pt 的文字（最小 %.1f pt），印出來可能難以閱讀或糊掉。", formatPages(ps), minTextPt, least))
	} else {
		lines = append(lines, fmt.Sprintf("✅ 字級：沒有小於 %d pt 的文字。", minTextPt))
	}
	if len(s.Hairlines) > 0 {
		lines = append(lines, fmt.Sprintf("❌ 極細線：第 %s 頁有線寬設為 0 的線條，印刷時可能細到看不見，請改為至少 %g pt。", formatPages(pages(s.Hairlines)), minLinePt))
	}
	if len(s.ThinLines) > 0 {
		lines = append(lines, fmt.Sprintf("⚠️ 細線：第 %s 頁有細於 %g pt（約 0.09 mm）的線條，可能印不清楚。", formatPages(pages(s.ThinLines)), minLinePt))
	}
	if len(s.Hairlines) == 0 && len(s.ThinLines) == 0 {
		lines = append(lines, fmt.Sprintf("✅ 線寬：沒有細於 %g pt 的線條。", minLinePt))
	}
	return strings.Join(lines, "\n")
}

// printerReport 把印刷廠預檢各段組起來，開頭統計需修正／需確認的項目數。
func printerReport(sections ...string) string {
	body := strings.Join(sections, "\n")
	verdict := "✅ 未發現印刷問題"
	if e, w := strings.Count(body, "❌"), strings.Count(body, "⚠️"); e+w > 0 {
		verdict = fmt.Sprintf("共 %d 項需修正、%d 項需確認", e, w)
	}
	return fmt.Sprintf("🏭 印刷廠預檢（台灣一般平版四色印刷預設：出血 %d mm、總墨量 %d%%、最小字 %d pt、最細線 %g pt）\n%s\n%s",
		bleedMM, tacLimit, minTextPt, minLinePt, verdict, body)
}

// inspectInk 以 72 dpi 點陣化 CMYK 輸出，檢查總墨量、四色黑，
// 以及沒有裁切框的頁面是否有圖文貼齊頁緣（hasTrim 為 nil 時不檢查貼邊）。
func inspectInk(pdf, dir string, hasTrim []bool) string {
	rd := filepath.Join(dir, "ink")
	os.MkdirAll(rd, 0o755)
	defer os.RemoveAll(rd)
	if _, err := runGS(inspectTimeout, "-dSAFER", "-dBATCH", "-dNOPAUSE", "-dQUIET", "-sDEVICE=pamcmyk32", "-r72",
		"-sOutputFile="+slash(filepath.Join(rd, "p%05d.pam")), slash(pdf)); err != nil {
		return fmt.Sprintf("⚠️ 總墨量檢查失敗：%v", err)
	}
	files, _ := filepath.Glob(filepath.Join(rd, "p*.pam"))
	sort.Strings(files)
	var tacPages, richPages, edgePages []int
	worst, worstPage := 0, 0
	limit := tacLimit*255/100 + 2
	const band = 3 // 72 dpi 下約 1 mm
	for n, f := range files {
		raw, _ := os.ReadFile(f)
		hdr, b, _ := bytes.Cut(raw, []byte("ENDHDR\n"))
		var w, h int
		for _, l := range strings.Split(string(hdr), "\n") {
			fmt.Sscanf(l, "WIDTH %d", &w)
			fmt.Sscanf(l, "HEIGHT %d", &h)
		}
		checkEdge := n < len(hasTrim) && !hasTrim[n] && w > 2*band && h > 2*band
		over, rich, edge := false, false, false
		for j := 0; j+3 < len(b); j += 4 {
			c, m, y, k := int(b[j]), int(b[j+1]), int(b[j+2]), int(b[j+3])
			if checkEdge && !edge {
				x, row := (j/4)%w, (j/4)/w
				if (x < band || x >= w-band || row < band || row >= h-band) && max(c, m, y, k) > 13 { // 任一色版超過約 5%
					edge = true
				}
			}
			if t := c + m + y + k; t > limit {
				over = true
				if t > worst {
					worst, worstPage = t, n+1
				}
			}
			// 四色黑：K ≥ 50% 且 C、M、Y 各 ≥ 30%（RGB 黑轉 Japan Color 約 C89 M87 Y82 K78）
			if k >= 128 && min(c, m, y) >= 77 {
				rich = true
			}
		}
		if over {
			tacPages = append(tacPages, n+1)
		}
		if rich {
			richPages = append(richPages, n+1)
		}
		if edge {
			edgePages = append(edgePages, n+1)
		}
	}
	var lines []string
	if len(tacPages) > 0 {
		lines = append(lines, fmt.Sprintf("❌ 總墨量超過 %d%%：第 %s 頁（最高 %d%%，在第 %d 頁）。墨量過高容易背印、乾燥不良。",
			tacLimit, formatPages(tacPages), worst*100/255, worstPage))
	} else {
		lines = append(lines, fmt.Sprintf("✅ 總墨量：全部未超過 %d%%。", tacLimit))
	}
	if len(richPages) > 0 {
		lines = append(lines, fmt.Sprintf("⚠️ 四色黑：第 %s 頁有由 C、M、Y、K 混成的黑色。若是文字或細線，套色稍有偏差就會出現彩色毛邊，建議黑字用單色黑 K100；大面積深色底圖可以忽略。", formatPages(richPages)))
	} else {
		lines = append(lines, "✅ 四色黑：未發現 C、M、Y、K 混成的黑色。")
	}
	if len(edgePages) > 0 {
		lines = append(lines, fmt.Sprintf("⚠️ 貼邊物件：第 %s 頁有圖文延伸到頁面邊緣，但沒有出血；裁切時邊緣可能露白，請加 %d mm 出血或內縮。", formatPages(edgePages), bleedMM))
	}
	return strings.Join(lines, "\n")
}

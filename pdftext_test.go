package main

// PDF 轉文字的測試。用的是 gs 的 txtwrite，所以需要 gs。

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func textPost(t *testing.T, path string, fields map[string]string) textResult {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "文件.pdf")
	b, _ := os.ReadFile(path)
	fw.Write(b)
	for k, v := range fields {
		mw.WriteField(k, v)
	}
	mw.Close()
	req := httptest.NewRequest("POST", "/api/text", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	handleText(rec, req)
	var res textResult
	json.Unmarshal(rec.Body.Bytes(), &res)
	return res
}

func TestTextExtract(t *testing.T) {
	workDir = t.TempDir()
	initCMYK()
	if gsPath == "" {
		t.Skip("沒有 Ghostscript")
	}
	// 中文（Identity-H + ToUnicode）抽得出來，而且是可讀的中文
	res := textPost(t, "testdata/pdflib_cjk.pdf", map[string]string{})
	if !res.OK {
		t.Fatalf("轉換失敗：%+v", res)
	}
	out := filepath.Join(workDir, strings.Split(res.Download, "/")[3], "out.txt")
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{"目錄", "第1章中文標題", "第一部分"} {
		if !strings.Contains(got, want) {
			t.Errorf("抽出的文字缺少 %q：\n%s", want, got)
		}
	}
	if res.Chars == 0 || res.Lines == 0 {
		t.Errorf("字元／行數沒算出來：%+v", res)
	}

	// 版面：gs 會依座標補空白，同一列的不同欄位會對齊
	dir := t.TempDir()
	ps := filepath.Join(dir, "t.ps")
	os.WriteFile(ps, []byte("%!PS\n/Helvetica findfont 14 scalefont setfont\n"+
		"72 700 moveto (Invoice Number: INV-2026-001) show\n"+
		"72 680 moveto (Customer) show 200 680 moveto (Wang Xiao Ming) show\n"+
		"1 1 3 { /p exch def 72 600 moveto (PAGE ) show p 3 string cvs show showpage } for\n"), 0o644)
	pdf := filepath.Join(dir, "t.pdf")
	if _, err := runGS(time.Minute, "-dBATCH", "-dNOPAUSE", "-dQUIET", "-sDEVICE=pdfwrite", "-sOutputFile="+pdf, ps); err != nil {
		t.Fatal(err)
	}
	all := textPost(t, pdf, map[string]string{})
	txt := readDownload(t, all)
	for _, want := range []string{"Invoice Number: INV-2026-001", "Wang Xiao Ming", "PAGE 1", "PAGE 2", "PAGE 3"} {
		if !strings.Contains(txt, want) {
			t.Errorf("缺少 %q：\n%s", want, txt)
		}
	}

	// 跳號的頁碼要分段跑：只選 1 和 3，不該出現 PAGE 2
	skip := readDownload(t, textPost(t, pdf, map[string]string{"pages": "1,3"}))
	if !strings.Contains(skip, "PAGE 1") || !strings.Contains(skip, "PAGE 3") {
		t.Errorf("應該要有第 1 和第 3 頁：\n%s", skip)
	}
	if strings.Contains(skip, "PAGE 2") {
		t.Errorf("不該出現第 2 頁：\n%s", skip)
	}

	// 掃描檔（只有圖、沒有文字）要老實說抽不到
	blk := filepath.Join(dir, "blank.ps")
	os.WriteFile(blk, []byte("%!PS\n0 0 0.5 0 setcmykcolor 72 72 200 100 rectfill showpage\n"), 0o644)
	blank := filepath.Join(dir, "blank.pdf")
	if _, err := runGS(time.Minute, "-dBATCH", "-dNOPAUSE", "-dQUIET", "-sDEVICE=pdfwrite", "-sOutputFile="+blank, blk); err != nil {
		t.Fatal(err)
	}
	if res := textPost(t, blank, map[string]string{}); res.OK || !strings.Contains(res.Message, "沒有可搜尋的文字") {
		t.Errorf("沒有文字層的檔案應該老實回報：%+v", res)
	}
}

func readDownload(t *testing.T, res textResult) string {
	t.Helper()
	if !res.OK {
		t.Fatalf("轉換失敗：%+v", res)
	}
	b, err := os.ReadFile(filepath.Join(workDir, strings.Split(res.Download, "/")[3], "out.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

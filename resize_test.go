package main

// 頁面尺寸統一的測試：內容要被縮放，頁面尺寸要真的變成目標尺寸。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pdfcpu/pdfcpu/pkg/api"
)

func TestResize(t *testing.T) {
	workDir = t.TempDir()
	initCMYK()
	if gsPath == "" {
		t.Skip("沒有 Ghostscript")
	}
	// 兩頁不同尺寸：400x300 與 800x600
	ps := filepath.Join(workDir, "r.ps")
	os.WriteFile(ps, []byte("%!PS\n<< /PageSize [400 300] >> setpagedevice 0 0 0 setrgbcolor 20 20 200 100 rectfill showpage\n"+
		"<< /PageSize [800 600] >> setpagedevice 0 0 0 setrgbcolor 40 40 400 200 rectfill showpage\n"), 0o644)
	pdf := filepath.Join(workDir, "r.pdf")
	if _, err := runGS(time.Minute, "-dBATCH", "-dNOPAUSE", "-dQUIET", "-sDEVICE=pdfwrite", "-sOutputFile="+pdf, ps); err != nil {
		t.Fatal(err)
	}
	post := func(fields map[string]string) resizeResult {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		fw, _ := mw.CreateFormFile("file", "大小不一.pdf")
		b, _ := os.ReadFile(pdf)
		fw.Write(b)
		for k, v := range fields {
			mw.WriteField(k, v)
		}
		mw.Close()
		req := httptest.NewRequest("POST", "/api/resize", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		rec := httptest.NewRecorder()
		handleResize(rec, req)
		var res resizeResult
		json.Unmarshal(rec.Body.Bytes(), &res)
		return res
	}
	outOf := func(res resizeResult) string {
		return filepath.Join(workDir, strings.Split(res.Download, "/")[3], "out.pdf")
	}

	res := post(map[string]string{"paper": "A4"})
	if !res.OK || res.Pages != 2 {
		t.Fatalf("縮放失敗：%+v", res)
	}
	if !strings.Contains(res.Message, "A4 直式") {
		t.Errorf("訊息不對：%s", res.Message)
	}
	for p := 1; p <= 2; p++ {
		w, h, err := outPageSize(outOf(res), p)
		if err != nil {
			t.Fatal(err)
		}
		if math.Abs(w-595.276) > 1 || math.Abs(h-841.89) > 1 {
			t.Errorf("第 %d 頁尺寸 = %.1f x %.1f，想要 A4 直式", p, w, h)
		}
	}
	// 橫式：長寬互換
	land := post(map[string]string{"paper": "A4", "landscape": "1"})
	w, h, err := outPageSize(outOf(land), 1)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(w-841.89) > 1 || math.Abs(h-595.276) > 1 {
		t.Errorf("橫式尺寸 = %.1f x %.1f", w, h)
	}
	// 別的紙張
	letter := post(map[string]string{"paper": "Letter"})
	if w, h, err := outPageSize(outOf(letter), 1); err != nil || math.Abs(w-612) > 1 || math.Abs(h-792) > 1 {
		t.Errorf("Letter 尺寸 = %.1f x %.1f（err=%v）", w, h, err)
	}
	// 內容還在（畫的方塊仍在頁面上 → 檔案大小不會趨近 0）
	if fi, err := os.Stat(outOf(res)); err != nil || fi.Size() < 1000 {
		t.Errorf("輸出檔太小，內容可能掉了：%v", err)
	}
}

// outPageSize 讀出輸出檔某一頁的實際尺寸（pt），測試用
func outPageSize(path string, page int) (float64, float64, error) {
	ctx, err := api.ReadContextFile(path)
	if err != nil {
		return 0, 0, err
	}
	_, _, inh, err := ctx.XRefTable.PageDict(page, false)
	if err != nil || inh == nil || inh.MediaBox == nil {
		return 0, 0, fmt.Errorf("第 %d 頁讀不到 MediaBox", page)
	}
	return inh.MediaBox.UR.X - inh.MediaBox.LL.X, inh.MediaBox.UR.Y - inh.MediaBox.LL.Y, nil
}

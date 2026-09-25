package main

// PDF 轉文字：用 gs 的 txtwrite 裝置。
// 這顆裝置會依文字在頁面上的位置補空白（表格欄位會對齊），也就是「版面重建」，
// 中文只要字型帶 ToUnicode 一樣抽得出來；掃描檔沒有文字層，抽出來會是空的。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

type textResult struct {
	fileResult
	Chars int `json:"chars,omitempty"`
	Lines int `json:"lines,omitempty"`
}

func handleText(w http.ResponseWriter, r *http.Request) {
	res := textResult{}
	defer func() { w.Header().Set("Content-Type", "application/json"); json.NewEncoder(w).Encode(res) }()

	if gsPath == "" {
		res.Message = noGS
		return
	}
	id, dir, in, name, msg := receivePDF(w, r)
	if msg != "" {
		res.Message = msg
		return
	}
	ctx, err := readPDF(in)
	if err != nil {
		res.Message = "無法讀取 PDF（有開啟密碼的檔案請先移除保護）：" + err.Error()
		return
	}
	pages, err := parsePages(r.FormValue("pages"), ctx.PageCount)
	if err != nil {
		res.Message = err.Error()
		return
	}
	out := filepath.Join(dir, "out.txt")
	if err := gsText(in, out, pages); err != nil {
		res.Message = "❌ Ghostscript 轉換失敗：" + err.Error()
		return
	}
	b, err := os.ReadFile(out)
	if err != nil {
		res.Message = "❌ 讀不到轉出的文字：" + err.Error()
		return
	}
	txt := strings.TrimSpace(string(b))
	if txt == "" {
		res.Message = "這個檔案沒有可搜尋的文字（掃描檔的內容是一張圖），需要 OCR 才取得出文字。"
		return
	}
	res.Chars = utf8.RuneCountInString(txt)
	res.Lines = len(strings.Split(txt, "\n"))
	res.OK = true
	res.Message = fmt.Sprintf("✅ 取出 %d 個字元、%d 行。", res.Chars, res.Lines)
	res.Download = "/api/file/" + id + "/" + url.PathEscape(name+"_文字.txt")
}

// gsText 逐段跑 txtwrite 再把結果接起來。
// txtwrite 只吃 FirstPage／LastPage 這種連續範圍，所以「1,3」要拆成兩段跑，
// 不然會把中間沒選到的頁也一起抽出來。
func gsText(in, out string, pages []int) error {
	ranges := pageRanges(pages)
	var all strings.Builder
	for i, rg := range ranges {
		first, last := rg, rg
		if a, b, ok := strings.Cut(rg, "-"); ok {
			first, last = a, b
		}
		part := out + fmt.Sprintf(".%d", i)
		if _, err := runGS(convertTimeout, "-dSAFER", "-dBATCH", "-dNOPAUSE", "-dQUIET", "-sDEVICE=txtwrite",
			"-dFirstPage="+first, "-dLastPage="+last, "-sOutputFile="+slash(part), slash(in)); err != nil {
			return err
		}
		b, err := os.ReadFile(part)
		os.Remove(part)
		if err != nil {
			return err
		}
		if all.Len() > 0 && len(b) > 0 {
			all.WriteString("\n")
		}
		all.Write(b)
	}
	return os.WriteFile(out, []byte(all.String()), 0o644)
}

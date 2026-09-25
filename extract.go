package main

// 取出 PDF 內嵌的圖片：跟「PDF 轉圖片」（把整頁點陣化）不同，這裡拿的是原本就嵌在檔案裡的圖，
// 解析度不變，也不會把文字一起變成點陣。用 pdfcpu 的 ExtractImagesRaw。

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

type extractResult struct {
	fileResult
	Count int `json:"count,omitempty"`
}

func handleExtractImages(w http.ResponseWriter, r *http.Request) {
	res := extractResult{}
	defer func() { w.Header().Set("Content-Type", "application/json"); json.NewEncoder(w).Encode(res) }()

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
	f, err := os.Open(in)
	if err != nil {
		res.Message = "❌ 無法開啟檔案：" + err.Error()
		return
	}
	defer f.Close()

	perPage, err := api.ExtractImagesRaw(f, []string{strings.Join(pageRanges(pages), ",")}, model.NewDefaultConfiguration())
	// pdfcpu 遇到不支援的圖片會把已找到的給我們、同時回一個錯誤，那種情況照樣交貨並提醒
	var unsupported *api.UnsupportedResourceError
	if err != nil && !errors.As(err, &unsupported) {
		res.Message = "取出圖片失敗：" + err.Error()
		return
	}

	imgDir := filepath.Join(dir, "img")
	os.MkdirAll(imgDir, 0o755)
	type shot struct{ path, name, ext string }
	var shots []shot
	for _, m := range perPage {
		// 同一頁的物件編號不會重複，依編號排序只是讓輸出順序穩定
		nrs := make([]int, 0, len(m))
		for n := range m {
			nrs = append(nrs, n)
		}
		sort.Ints(nrs)
		for _, n := range nrs {
			img := m[n]
			ext := strings.ToLower(strings.TrimPrefix(img.FileType, "."))
			if ext == "" {
				ext = "bin"
			}
			// 暫存檔名一律用序號：原檔名是使用者給的，不能直接當路徑用
			p := filepath.Join(imgDir, fmt.Sprintf("%05d.%s", len(shots)+1, ext))
			dst, err := os.Create(p)
			if err != nil {
				res.Message = "❌ 無法寫出圖片：" + err.Error()
				return
			}
			_, err = io.Copy(dst, img.Reader)
			dst.Close()
			if err != nil {
				res.Message = "❌ 讀取內嵌圖片失敗：" + err.Error()
				return
			}
			shots = append(shots, shot{p, fmt.Sprintf("%s_p%03d_%02d.%s", name, img.PageNr, n, ext), ext})
		}
	}
	switch {
	case len(shots) == 0:
		res.Message = "沒有找到內嵌的圖片。整頁掃描的檔案內容是一張大圖，用「PDF 轉圖片」輸出比較合用。"
		return
	case err != nil && unsupported != nil:
		res.Message = "ℹ️ 有部分圖片的格式不支援、已跳過，其餘都取出了。"
	}
	res.Count = len(shots)

	if len(shots) == 1 {
		os.Rename(shots[0].path, filepath.Join(dir, "out."+shots[0].ext))
		res.Download = "/api/file/" + id + "/" + url.PathEscape(shots[0].name)
	} else {
		names := make([]string, len(shots))
		paths := make([]string, len(shots))
		for i, s := range shots {
			names[i], paths[i] = s.name, s.path
		}
		if err := zipFiles(filepath.Join(dir, "out.zip"), paths, func(i int) string { return names[i] }); err != nil {
			res.Message = "打包 ZIP 失敗：" + err.Error()
			return
		}
		res.Download = "/api/file/" + id + "/" + url.PathEscape(fmt.Sprintf("%s_%d張圖.zip", name, len(shots)))
	}
	res.OK = true
}

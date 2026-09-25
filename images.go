package main

// PDF 轉圖片：Ghostscript 點陣化指定頁，一頁直接給圖，多頁打包成 ZIP。

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// parsePages 解析「1-3,5,8-」這種頁碼範圍（n 是總頁數），回傳排序、不重複的頁碼；空字串代表全部。
func parsePages(spec string, n int) ([]int, error) {
	spec = strings.NewReplacer(" ", "", "，", ",", "－", "-", "～", "-", "~", "-").Replace(spec)
	if spec == "" {
		spec = "1-"
	}
	seen := map[int]bool{}
	for _, part := range strings.Split(spec, ",") {
		if part == "" {
			continue
		}
		lo, hi, isRange := strings.Cut(part, "-")
		a, err := strconv.Atoi(lo)
		if err != nil {
			return nil, fmt.Errorf("看不懂頁碼「%s」", part)
		}
		b := a
		if isRange {
			if b = n; hi != "" {
				if b, err = strconv.Atoi(hi); err != nil {
					return nil, fmt.Errorf("看不懂頁碼「%s」", part)
				}
			}
		}
		for p := max(a, 1); p <= min(b, n); p++ {
			seen[p] = true
		}
	}
	pages := make([]int, 0, len(seen))
	for p := range seen {
		pages = append(pages, p)
	}
	sort.Ints(pages)
	if len(pages) == 0 {
		return nil, fmt.Errorf("頁碼範圍不在 1 到 %d 頁之間", n)
	}
	return pages, nil
}

func handleImages(w http.ResponseWriter, r *http.Request) {
	res := fileResult{}
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
	dpi := r.FormValue("dpi")
	if dpi != "72" && dpi != "300" && dpi != "600" {
		dpi = "150"
	}
	ext, device := "png", "png16m"
	if r.FormValue("format") == "jpg" {
		ext, device = "jpg", "jpeg"
	}
	list := make([]string, len(pages))
	for i, p := range pages {
		list[i] = strconv.Itoa(p)
	}
	imgDir := filepath.Join(dir, "img")
	os.MkdirAll(imgDir, 0o755)
	// gs 的輸出編號是依序的第幾張，不是頁碼；頁碼由 pages 對回去
	if _, err := runGS(convertTimeout, "-dSAFER", "-dBATCH", "-dNOPAUSE", "-dQUIET", "-sDEVICE="+device, "-r"+dpi,
		"-dTextAlphaBits=4", "-dGraphicsAlphaBits=4", "-dJPEGQ=90", "-sPageList="+strings.Join(list, ","),
		"-sOutputFile="+slash(filepath.Join(imgDir, "%05d."+ext)), slash(in)); err != nil {
		res.Message = fmt.Sprintf("❌ Ghostscript 轉換失敗：%v", err)
		return
	}
	files, _ := filepath.Glob(filepath.Join(imgDir, "*."+ext))
	sort.Strings(files)
	if len(files) != len(pages) {
		res.Message = fmt.Sprintf("❌ 預期 %d 張圖，實際產生 %d 張。", len(pages), len(files))
		return
	}
	pageName := func(i int) string { return fmt.Sprintf("%s_p%03d.%s", name, pages[i], ext) }

	if len(files) == 1 {
		os.Rename(files[0], filepath.Join(dir, "out."+ext))
		res.Download = "/api/file/" + id + "/" + url.PathEscape(pageName(0))
	} else {
		if err := zipFiles(filepath.Join(dir, "out.zip"), files, pageName); err != nil {
			res.Message = "打包 ZIP 失敗：" + err.Error()
			return
		}
		res.Download = "/api/file/" + id + "/" + url.PathEscape(fmt.Sprintf("%s_%d張圖.zip", name, len(files)))
	}
	os.RemoveAll(imgDir)
	res.OK = true
}

// zipFiles 把 files 依序寫進 ZIP，第 i 個檔名為 nameOf(i)。圖片本身已壓縮，只儲存不再壓。
func zipFiles(out string, files []string, nameOf func(int) string) error {
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(f)
	for i, src := range files {
		dst, err := zw.CreateHeader(&zip.FileHeader{Name: nameOf(i), Method: zip.Store}) // 中文檔名會自動標成 UTF-8
		if err != nil {
			f.Close()
			return err
		}
		s, err := os.Open(src)
		if err != nil {
			f.Close()
			return err
		}
		_, err = io.Copy(dst, s)
		s.Close()
		if err != nil {
			f.Close()
			return err
		}
	}
	if err := zw.Close(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

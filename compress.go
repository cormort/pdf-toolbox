package main

// 壓縮 PDF：Ghostscript 重寫（圖片降解析度、字型子集化、去除重複），色彩保持原樣。
// gs 有時會把字型的文字對應寫錯，壓縮頁面會在瀏覽器比對壓縮前後的文字並警告。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
)

// 壓縮程度對應 gs 的 PDFSETTINGS：彩色／灰階圖片 screen 72 ppi、ebook 150 ppi、printer 300 ppi
var compressLevels = map[string]string{"screen": "/screen", "ebook": "/ebook", "printer": "/printer"}

type compressResult struct {
	OK       bool   `json:"ok"`
	Message  string `json:"message,omitempty"`
	InSize   int64  `json:"inSize"`
	OutSize  int64  `json:"outSize"`
	Restored int    `json:"restored"` // 補回 ToUnicode 的字型數
	Download string `json:"download,omitempty"`
}

func handleCompress(w http.ResponseWriter, r *http.Request) {
	res := compressResult{}
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
	preset, ok := compressLevels[r.FormValue("level")]
	if !ok {
		preset = "/ebook"
	}
	out := filepath.Join(dir, "out.pdf")
	if _, err := runGS(convertTimeout, "-dSAFER", "-dBATCH", "-dNOPAUSE", "-dQUIET", "-sDEVICE=pdfwrite",
		"-dPDFSETTINGS="+preset,
		"-sColorConversionStrategy=LeaveColorUnchanged", // /screen、/ebook 預設會轉成 sRGB，印刷檔不能這樣改
		"-dDetectDuplicateImages=true",
		"-sOutputFile="+slash(out), slash(in)); err != nil || !fileExists(out) {
		res.Message = fmt.Sprintf("❌ Ghostscript 壓縮失敗：%v", err)
		return
	}
	res.Restored, _ = restoreToUnicode(in, out) // 補不回來也不影響畫面，文字比對會提醒
	si, _ := os.Stat(in)
	so, _ := os.Stat(out)
	res.InSize, res.OutSize = si.Size(), so.Size()
	res.OK = true
	if res.OutSize >= res.InSize {
		res.Message = "原檔已經很精簡，壓縮後沒有變小，建議直接用原檔。"
		return
	}
	res.Download = "/api/file/" + id + "/" + url.PathEscape(name+"_壓縮.pdf")
}

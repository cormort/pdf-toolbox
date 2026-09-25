package main

// 頁面尺寸縮放：用 gs 的 -dPDFFitPage 把每一頁統一成指定尺寸（內容等比縮放、置中）。
// 常見用途是把大小不一的掃描頁、合併進來的附件統一成 A4 再送印。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
)

// 常見紙張的點數（1 pt = 1/72 inch），直式
var papers = []struct {
	name string
	w, h float64
}{
	{"A4", 595.276, 841.89},
	{"A3", 841.89, 1190.55},
	{"A5", 419.528, 595.276},
	{"B5", 498.898, 708.661},
	{"Letter", 612, 792},
	{"Legal", 612, 1008},
}

type resizeResult struct {
	fileResult
	Pages int    `json:"pages,omitempty"`
	Paper string `json:"paper,omitempty"`
}

func handleResize(w http.ResponseWriter, r *http.Request) {
	res := resizeResult{}
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
	// 找目標紙張
	pw, ph, ok := 0.0, 0.0, false
	for _, p := range papers {
		if p.name == r.FormValue("paper") {
			pw, ph, ok = p.w, p.h, true
			res.Paper = p.name
			break
		}
	}
	if !ok {
		pw, ph, res.Paper = papers[0].w, papers[0].h, papers[0].name
	}
	orient := "直式"
	if r.FormValue("landscape") == "1" {
		pw, ph, orient = ph, pw, "橫式"
	}

	out := filepath.Join(dir, "out.pdf")
	if _, err := runGS(convertTimeout, "-dSAFER", "-dBATCH", "-dNOPAUSE", "-dQUIET", "-sDEVICE=pdfwrite",
		"-dFIXEDMEDIA", // 輸出頁面就用指定的尺寸
		"-dDEVICEWIDTHPOINTS="+strconv.FormatFloat(pw, 'f', -1, 64),
		"-dDEVICEHEIGHTPOINTS="+strconv.FormatFloat(ph, 'f', -1, 64),
		"-dPDFFitPage",            // 內容等比縮放到放得下、置中
		"-dAutoRotatePages=/None", // 不要讓 gs 自己把頁面轉向，尺寸才真的統一
		"-sOutputFile="+slash(out), slash(in)); err != nil {
		res.Message = "❌ Ghostscript 縮放失敗：" + err.Error()
		return
	}
	if !fileExists(out) {
		res.Message = "❌ Ghostscript 沒有產出檔案。"
		return
	}
	res.Pages = ctx.PageCount
	res.OK = true
	res.Message = fmt.Sprintf("✅ 已把 %d 頁統一成 %s %s（內容等比縮放、置中；小頁面也會放大）。", res.Pages, res.Paper, orient)
	res.Download = "/api/file/" + id + "/" + url.PathEscape(fmt.Sprintf("%s_%s.pdf", name, res.Paper))
}

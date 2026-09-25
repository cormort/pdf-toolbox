package main

// 取出內嵌圖片的測試：自己造 PNG／JPEG，用 pdfcpu 匯入成 PDF，再走一次擷取流程。

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"mime/multipart"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

func TestExtractImages(t *testing.T) {
	workDir = t.TempDir()
	dir := t.TempDir()
	img := image.NewRGBA(image.Rect(0, 0, 40, 30))
	for x := 0; x < 40; x++ {
		for y := 0; y < 30; y++ {
			img.Set(x, y, color.RGBA{uint8(x * 6), uint8(y * 8), 200, 255})
		}
	}
	pngPath := filepath.Join(dir, "a.png")
	jpgPath := filepath.Join(dir, "b.jpg")
	f1, _ := os.Create(pngPath)
	png.Encode(f1, img)
	f1.Close()
	f2, _ := os.Create(jpgPath)
	jpeg.Encode(f2, img, nil)
	f2.Close()

	pdf := filepath.Join(dir, "in.pdf")
	if err := api.ImportImagesFile([]string{pngPath, jpgPath}, pdf, nil, model.NewDefaultConfiguration()); err != nil {
		t.Fatalf("建立測試 PDF 失敗：%v", err)
	}
	body, _ := os.ReadFile(pdf)

	post := func(fields map[string]string) extractResult {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		fw, _ := mw.CreateFormFile("file", "測試檔.pdf")
		fw.Write(body)
		for k, v := range fields {
			mw.WriteField(k, v)
		}
		mw.Close()
		req := httptest.NewRequest("POST", "/api/extract-images", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		rec := httptest.NewRecorder()
		handleExtractImages(rec, req)
		var res extractResult
		json.Unmarshal(rec.Body.Bytes(), &res)
		return res
	}

	res := post(map[string]string{})
	if !res.OK || res.Count != 2 {
		t.Fatalf("應該取出 2 張：%+v", res)
	}
	if !strings.HasSuffix(res.Download, url.PathEscape("測試檔_2張圖.zip")) {
		t.Errorf("下載名稱不對：%s", res.Download)
	}
	jobDir := filepath.Join(workDir, strings.Split(res.Download, "/")[3])
	zr, err := zip.OpenReader(filepath.Join(jobDir, "out.zip"))
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	var types []string
	for _, zf := range zr.File {
		rc, err := zf.Open()
		if err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 8)
		n, _ := rc.Read(buf)
		rc.Close()
		switch {
		case bytes.HasPrefix(buf[:n], []byte{0x89, 'P', 'N', 'G'}):
			types = append(types, "png")
		case bytes.HasPrefix(buf[:n], []byte{0xFF, 0xD8, 0xFF}):
			types = append(types, "jpg")
		default:
			t.Errorf("%s 不是圖片：% x", zf.Name, buf[:n])
		}
	}
	if strings.Join(types, ",") != "png,jpg" {
		t.Errorf("ZIP 內容應該是一張 PNG 一張 JPEG，得到 %v", types)
	}

	// 只取第 2 頁 → 單一張圖直接下載，不是 ZIP
	one := post(map[string]string{"pages": "2"})
	if !one.OK || one.Count != 1 || !strings.HasSuffix(one.Download, ".jpg") {
		t.Fatalf("單頁應該直接給一張 jpg：%+v", one)
	}
	// 純文字 PDF 沒有內嵌圖片
	textPDF, _ := os.ReadFile("testdata/pdflib_cjk.pdf")
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "t.pdf")
	fw.Write(textPDF)
	mw.Close()
	req := httptest.NewRequest("POST", "/api/extract-images", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	handleExtractImages(rec, req)
	var none extractResult
	json.Unmarshal(rec.Body.Bytes(), &none)
	if none.OK || !strings.Contains(none.Message, "沒有找到內嵌的圖片") {
		t.Errorf("純文字檔應該是沒有圖片：%+v", none)
	}
}

func TestSafeName(t *testing.T) {
	for in, want := range map[string]string{
		"報告":           "報告",
		"a/b":          "a_b",
		`..\..\evil`:   "_.._evil", // 分隔字元換成 _ 之後，開頭的點會被 Trim 掉
		"  .hidden.  ": "hidden",
		"":             "file",
		"c:d*e?f":      "c_d_e_f",
		"tab\tname":    "tab_name",
		"trailing....": "trailing",
		"normal (1)":   "normal (1)", // 副檔名在 receivePDF 就先切掉了
	} {
		if got := safeName(in); got != want {
			t.Errorf("safeName(%q) = %q，想要 %q", in, got, want)
		}
	}
}

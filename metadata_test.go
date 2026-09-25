package main

// 中繼資料的測試：用 gs 產生帶 XMP 的 PDF，驗證讀取、修改與清除（含 XMP）。

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

	"github.com/pdfcpu/pdfcpu/pkg/api"
)

func metaPost(t *testing.T, path string, fields map[string]string) metadataResult {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "報告.pdf")
	b, _ := os.ReadFile(path)
	fw.Write(b)
	for k, v := range fields {
		mw.WriteField(k, v)
	}
	mw.Close()
	req := httptest.NewRequest("POST", "/api/metadata", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	handleMetadata(rec, req)
	var res metadataResult
	json.Unmarshal(rec.Body.Bytes(), &res)
	return res
}

func metaOut(res metadataResult) string {
	return filepath.Join(workDir, strings.Split(res.Download, "/")[3], "out.pdf")
}

func TestMetadata(t *testing.T) {
	workDir = t.TempDir()
	initCMYK()
	if gsPath == "" {
		t.Skip("沒有 Ghostscript")
	}
	// gs 產生的 PDF 帶 XMP（/Metadata），剛好可以測 XMP 有沒有被拆掉
	ps := filepath.Join(workDir, "m.ps")
	os.WriteFile(ps, []byte("%!PS\n/Helvetica findfont 12 scalefont setfont 72 700 moveto (meta) show showpage\n"), 0o644)
	pdf := filepath.Join(workDir, "m.pdf")
	if _, err := runGS(time.Minute, "-dBATCH", "-dNOPAUSE", "-dQUIET", "-sDEVICE=pdfwrite", "-sOutputFile="+pdf, ps); err != nil {
		t.Fatal(err)
	}

	read := metaPost(t, pdf, map[string]string{"action": "read"})
	if !read.OK {
		t.Fatalf("讀取失敗：%+v", read)
	}
	t.Logf("讀到：Producer=%q Creator=%q HasXMP=%v Pages=%d Extra=%v",
		read.Fields["Producer"], read.Fields["Creator"], read.HasXMP, read.PageCount, read.Extra)
	if read.PageCount != 1 {
		t.Errorf("頁數 = %d", read.PageCount)
	}
	if !read.HasXMP {
		t.Error("gs 產生的檔案應該有 XMP")
	}

	// 修改：改標題、留空的欄位會被清掉
	save := metaPost(t, pdf, map[string]string{"action": "save", "Title": "測試標題", "Author": "小編"})
	if !save.OK {
		t.Fatalf("修改失敗：%+v", save)
	}
	after := metaPost(t, metaOut(save), map[string]string{"action": "read"})
	if after.Fields["Title"] != "測試標題" || after.Fields["Author"] != "小編" {
		t.Errorf("標題與作者沒寫進去：%+v", after.Fields)
	}

	// 清除：欄位與 XMP 都要消失
	clear := metaPost(t, metaOut(save), map[string]string{"action": "clear"})
	if !clear.OK {
		t.Fatalf("清除失敗：%+v", clear)
	}
	cleared := metaPost(t, metaOut(clear), map[string]string{"action": "read"})
	// 原來的作者、標題等一定要消失；輸出一定會被寫入工具蓋上自己的 Producer 與時間戳，
	// 那是任何 PDF 寫入器都會做的事，不是沒清乾淨。
	for _, k := range []string{"Title", "Author", "Subject", "Keywords", "Creator"} {
		if cleared.Fields[k] != "" {
			t.Errorf("清除後 %s 還有值：%q", k, cleared.Fields[k])
		}
	}
	if strings.Contains(cleared.Fields["Producer"], "Ghostscript") {
		t.Errorf("清除後還留著原檔的 Producer：%q", cleared.Fields["Producer"])
	}
	t.Logf("清除後剩下的欄位：Producer=%q CreationDate=%q ModDate=%q",
		cleared.Fields["Producer"], cleared.Fields["CreationDate"], cleared.Fields["ModDate"])
	if cleared.HasXMP {
		t.Error("清除後不該還有 XMP")
	}
	if cleared.PageCount != 1 {
		t.Errorf("清除後頁數變了：%d", cleared.PageCount)
	}
	// 檔案要還能開、內容還在
	if _, err := api.ReadContextFile(metaOut(clear)); err != nil {
		t.Errorf("清除後的檔案打不開：%v", err)
	}

	// 有開啟密碼的檔案要給清楚的訊息
	enc := tmpProtectPost(t, pdf, map[string]string{"mode": "encrypt", "userPW": "u1", "ownerPW": "o1"})
	if res := metaPost(t, enc, map[string]string{"action": "read"}); res.OK || !strings.Contains(res.Message, "移除保護") {
		t.Errorf("加密檔應該提示先移除保護：%+v", res)
	}
}

// gsDecrypt 後援：gs 能把加密檔重寫成沒有加密的 PDF（就是 wctchen 那篇的「列印成 PDF」）
func TestGSDecryptFallback(t *testing.T) {
	workDir = t.TempDir()
	initCMYK()
	if gsPath == "" {
		t.Skip("沒有 Ghostscript")
	}
	src := "testdata/pdflib_cjk.pdf"
	// 只有權限密碼、禁止列印（開檔不用密碼）
	locked := tmpProtectPost(t, src, map[string]string{"mode": "encrypt", "ownerPW": "o1", "print": "0"})
	if !isEncrypted(locked) {
		t.Fatal("測試檔應該是加密的")
	}
	out := filepath.Join(workDir, "gs-out.pdf")
	if !gsDecrypt(locked, out, "") {
		t.Fatal("gs 應該能把這個檔案重寫成沒有加密的 PDF")
	}
	if isEncrypted(out) {
		t.Error("gs 重寫後不該還有加密")
	}
	if ctx, err := readPDF(out); err != nil || ctx.PageCount != 1 {
		t.Errorf("重寫後的檔案不對：%v", err)
	}
	// 開啟密碼錯的時候 gs 也要失敗，不能變成任何密碼都過
	locked2 := tmpProtectPost(t, src, map[string]string{"mode": "encrypt", "userPW": "u1", "ownerPW": "o1"})
	if gsDecrypt(locked2, filepath.Join(workDir, "bad.pdf"), "wrong") {
		t.Error("錯的密碼不該成功")
	}
}

// tmpProtectPost 借 protect 端點做一個加密檔
func tmpProtectPost(t *testing.T, path string, fields map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "e.pdf")
	b, _ := os.ReadFile(path)
	fw.Write(b)
	for k, v := range fields {
		mw.WriteField(k, v)
	}
	mw.Close()
	req := httptest.NewRequest("POST", "/api/protect", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	handleProtect(rec, req)
	var res fileResult
	json.Unmarshal(rec.Body.Bytes(), &res)
	if !res.OK {
		t.Fatalf("加密失敗：%+v", res)
	}
	return metaOut(metadataResult{fileResult: res})
}

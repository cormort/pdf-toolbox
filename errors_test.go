package main

// 錯誤紀錄與匯出的測試。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"
)

// resetErrs 清掉全域狀態，免得上一個測試的紀錄影響下一個
func resetErrs(t *testing.T) {
	t.Helper()
	errLog.Lock()
	errLog.items, errLog.dropped = nil, 0
	errLog.Unlock()
	t.Cleanup(func() {
		errLog.Lock()
		errLog.items, errLog.dropped = nil, 0
		errLog.Unlock()
		logPath = ""
	})
}

func TestRecordErrorRing(t *testing.T) {
	resetErrs(t)
	for i := 0; i < errKeep+5; i++ {
		recordError("後端", "POST /api/test", fmt.Sprintf("錯誤 %d", i), 500)
	}
	items, dropped := errSnapshot()
	if len(items) != errKeep || dropped != 5 {
		t.Fatalf("保留 %d 筆、捨棄 %d 筆，想要 %d 筆、5 筆", len(items), dropped, errKeep)
	}
	if items[0].Msg != "錯誤 5" || items[len(items)-1].Msg != fmt.Sprintf("錯誤 %d", errKeep+4) {
		t.Errorf("丟掉的不是最舊的：第一筆 %q、最後一筆 %q", items[0].Msg, items[len(items)-1].Msg)
	}
	if n := errCount(); n != errKeep {
		t.Errorf("errCount = %d，想要 %d", n, errKeep)
	}
	// gs 的 stderr 或前端送來的位元組不保證是 UTF-8，匯出檔一定要是合法 UTF-8
	recordError("前端", "x", string([]byte{0xb4, 0xfa, 0xb8, 0xd5}), 0)
	items, _ = errSnapshot()
	if last := items[len(items)-1].Msg; !utf8.ValidString(last) {
		t.Errorf("訊息不是合法 UTF-8：%q", last)
	}
}

func TestErrorReportSections(t *testing.T) {
	resetErrs(t)
	dir := t.TempDir()
	logPath = filepath.Join(dir, "pdf-toolbox.log")
	os.WriteFile(logPath, []byte("本次的第一行\n本次的最後一行\n"), 0o644)
	os.WriteFile(prevLogPath(), []byte("上次的紀錄\n"), 0o644)
	recordError("前端", "/recompose/", "TypeError: x is not a function\n    at foo", 0)

	got := errorReport()
	for _, want := range []string{
		"PDF Toolbox 錯誤紀錄",
		"── 環境 ──",
		"服務：http://" + addr,
		"── 錯誤紀錄 ──",
		"共 1 筆",
		"前端 /recompose/",
		"TypeError: x is not a function",
		"── 本次執行紀錄（" + logPath + "） ──",
		"本次的最後一行",
		"── 上次執行紀錄（" + prevLogPath() + "） ──",
		"上次的紀錄",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("匯出內容缺少 %q\n%s", want, got)
		}
	}
	// 沒有錯誤時也要講清楚，不要留白讓人以為壞了
	resetErrs(t)
	if got := errorReport(); !strings.Contains(got, "這次執行沒有記錄到錯誤") {
		t.Errorf("沒有錯誤時應該說明：%s", got)
	}
}

// 失敗的 API 呼叫（狀態 200 但 ok=false）也要被記下來
func TestAPIFailureCaptured(t *testing.T) {
	resetErrs(t)
	h := guard(http.HandlerFunc(handleProtect))
	req := httptest.NewRequest("POST", "/api/protect", strings.NewReader(""))
	req.Host = addr
	req.Header.Set("Origin", "http://"+addr)
	h.ServeHTTP(httptest.NewRecorder(), req)

	items, _ := errSnapshot()
	if len(items) != 1 {
		t.Fatalf("記了 %d 筆，想要 1 筆：%+v", len(items), items)
	}
	if items[0].Where != "後端" || items[0].Tool != "POST /api/protect" {
		t.Errorf("來源不對：%+v", items[0])
	}
	if !strings.Contains(items[0].Msg, "沒有收到檔案") {
		t.Errorf("訊息不對：%q", items[0].Msg)
	}
	// 成功的 API 呼叫不該留紀錄
	resetErrs(t)
	h2 := guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}))
	req2 := httptest.NewRequest("POST", "/api/cmyk", strings.NewReader(""))
	req2.Host = addr
	req2.Header.Set("Origin", "http://"+addr)
	h2.ServeHTTP(httptest.NewRecorder(), req2)
	if items, _ := errSnapshot(); len(items) != 0 {
		t.Errorf("成功不該留紀錄：%+v", items)
	}
}

func TestGuardRecordsRejectAndPanic(t *testing.T) {
	resetErrs(t)
	// 跨站 POST 被擋：要留紀錄，不然使用者只看到 403
	req := httptest.NewRequest("POST", "/api/cmyk", strings.NewReader(""))
	req.Host = addr
	guard(http.HandlerFunc(handleCMYK)).ServeHTTP(httptest.NewRecorder(), req)
	items, _ := errSnapshot()
	if len(items) != 1 || items[0].Code != http.StatusForbidden {
		t.Fatalf("403 沒有留紀錄：%+v", items)
	}

	// panic：紀錄之後照樣往上丟給 net/http，行為跟以前一樣
	resetErrs(t)
	boom := guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { panic("測試用 panic") }))
	req2 := httptest.NewRequest("GET", "/api/ping", nil)
	req2.Host = addr
	func() {
		defer func() {
			if v := recover(); v == nil {
				t.Error("panic 應該繼續往上丟")
			}
		}()
		boom.ServeHTTP(httptest.NewRecorder(), req2)
	}()
	items, _ = errSnapshot()
	if len(items) != 1 || items[0].Where != "panic" || !strings.Contains(items[0].Msg, "測試用 panic") {
		t.Fatalf("panic 沒有留紀錄：%+v", items)
	}
}

func TestClientErrorAndExport(t *testing.T) {
	resetErrs(t)
	logPath = filepath.Join(t.TempDir(), "pdf-toolbox.log")

	// 前端回報
	body := `{"where":"/cmyk/","message":"Uncaught TypeError: bad","stack":"at run","url":"http://x/cmyk/"}`
	req := httptest.NewRequest("POST", "/api/client-error", strings.NewReader(body))
	req.Host = addr
	req.Header.Set("Origin", "http://"+addr)
	rec := httptest.NewRecorder()
	guard(http.HandlerFunc(handleClientError)).ServeHTTP(rec, req)
	var res struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil || !res.OK {
		t.Fatalf("回報端點回應不對：%s", rec.Body.String())
	}
	items, _ := errSnapshot()
	if len(items) != 1 || items[0].Where != "前端" || items[0].Tool != "/cmyk/" {
		t.Fatalf("前端錯誤沒有記下來：%+v", items)
	}
	// 回報端點自己的回應不能被當成失敗再記一筆
	if len(items) != 1 {
		t.Errorf("重複記錄：%+v", items)
	}

	// 匯出
	rec2 := httptest.NewRecorder()
	handleErrorsExport(rec2, httptest.NewRequest("GET", "/api/errors/export", nil))
	head := rec2.Result().Header
	if ct := head.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q", ct)
	}
	cd := head.Get("Content-Disposition")
	if !strings.Contains(cd, "pdf-toolbox-errors-") || !strings.Contains(cd, ".txt") {
		t.Errorf("Content-Disposition = %q", cd)
	}
	out := rec2.Body.String()
	for _, want := range []string{"Uncaught TypeError: bad", "at run", "── 環境 ──"} {
		if !strings.Contains(out, want) {
			t.Errorf("匯出內容缺少 %q\n%s", want, out)
		}
	}
}

// 上一次的 log 要留著，當掉重開才追得到
func TestOpenLogRotates(t *testing.T) {
	resetErrs(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "pdf-toolbox.log"), []byte("上一次執行的內容\n"), 0o644)
	f, err := openLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if logPath != filepath.Join(dir, "pdf-toolbox.log") {
		t.Errorf("logPath = %q", logPath)
	}
	prev, err := os.ReadFile(prevLogPath())
	if err != nil || !strings.Contains(string(prev), "上一次執行的內容") {
		t.Errorf("上一次的 log 沒有留下來：%v %q", err, prev)
	}
	if fi, err := f.Stat(); err != nil || fi.Size() != 0 {
		t.Errorf("新的 log 應該是空的：%v %d", err, fi.Size())
	}
	// 沒有舊檔時也要能開
	dir2 := t.TempDir()
	f2, err := openLog(dir2)
	if err != nil {
		t.Fatal(err)
	}
	f2.Close()
	if _, err := os.Stat(prevLogPath()); !os.IsNotExist(err) {
		t.Errorf("沒有舊檔時不該生出 prev：%v", err)
	}
}

// Windows 版本字串要解對編碼：從管線讀 cmd 的輸出時，在地化的字是主控台字碼頁而不是 UTF-8
func TestOSVersion(t *testing.T) {
	got := osVersion()
	t.Logf("osVersion() = %q", got)
	if got == "" || strings.ContainsRune(got, 0) || strings.ContainsRune(got, 0xFFFD) {
		t.Fatalf("版本字串不能用：%q", got)
	}
	if runtime.GOOS == "windows" && !strings.ContainsAny(got, "0123456789") {
		t.Errorf("Windows 上應該有版本號：%q", got)
	}
}

func TestTailFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "t.log")
	var lines []string
	for i := 1; i <= 10; i++ {
		lines = append(lines, fmt.Sprintf("第 %d 行", i))
	}
	os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	got, err := tailFile(p, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "只列最後 3 行") || !strings.Contains(got, "第 10 行") || strings.Contains(got, "第 7 行") {
		t.Errorf("尾巴不對：%q", got)
	}
	if got, err := tailFile(p, 20); err != nil || strings.Contains(got, "只列最後") || !strings.Contains(got, "第 1 行") {
		t.Errorf("行數夠時不該截：%q %v", got, err)
	}
	if _, err := tailFile(filepath.Join(t.TempDir(), "nope.log"), 3); err == nil {
		t.Error("讀不到應該回錯誤")
	}
}

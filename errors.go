package main

// 錯誤紀錄與匯出：GUI 沒有主控台，出錯時使用者手上沒有東西可以回報。
// 這裡收集後端（HTTP 4xx／5xx、ok=false、panic）與前端（JS 例外、未處理的 promise）的錯誤，
// 使用者在首頁按「匯出錯誤紀錄」就能下載一份可以直接寄出的文字檔。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
)

const (
	errKeep      = 200 // 記憶體裡保留幾筆；超過就丟最舊的，匯出檔會註明丟了幾筆
	logTailLines = 300 // 匯出檔裡每份 log 附上的行數
)

var startTime = time.Now()

// logPath 由 openLog 設定，錯誤匯出要靠它附上執行紀錄
var logPath string

type errRecord struct {
	When  time.Time
	Where string // 後端、前端、panic、啟動
	Tool  string // 請求路徑或前端頁面
	Msg   string
	Code  int // HTTP 狀態碼；前端錯誤是 0
}

var errLog = struct {
	sync.Mutex
	items   []errRecord
	dropped int
}{}

// recordError 記一筆錯誤。訊息長度切短，避免一個巨大的 gs 輸出把紀錄撐爆；
// 內容一律轉成合法 UTF-8——前端送來的位元組、gs 用主控台字碼頁寫的訊息都不保證是 UTF-8，
// 匯出檔是要給人讀的純文字，不能有壞掉的位元組。
func recordError(where, tool, msg string, code int) {
	msg = strings.TrimSpace(strings.ToValidUTF8(msg, "\uFFFD"))
	if msg == "" {
		msg = "(沒有訊息)"
	}
	if len(msg) > 4000 {
		msg = msg[:4000] + "……"
	}
	errLog.Lock()
	defer errLog.Unlock()
	if len(errLog.items) >= errKeep {
		errLog.items = append(errLog.items[:0], errLog.items[1:]...)
		errLog.dropped++
	}
	errLog.items = append(errLog.items, errRecord{
		When: time.Now(), Where: where, Tool: strings.Join(strings.Fields(tool), " "), Msg: msg, Code: code,
	})
}

func errSnapshot() ([]errRecord, int) {
	errLog.Lock()
	defer errLog.Unlock()
	return append([]errRecord(nil), errLog.items...), errLog.dropped
}

func errCount() int {
	errLog.Lock()
	defer errLog.Unlock()
	return len(errLog.items)
}

// errCountHeader 讓首頁每 30 秒的 ping 順便知道有幾筆錯誤，按鈕上才顯示得出來
func errCountHeader(w http.ResponseWriter) {
	w.Header().Set("X-Error-Count", strconv.Itoa(errCount()))
}

// apiJSON 回傳這個請求的回應是不是小顆的 JSON。只有這種可以放心緩衝起來看內容；
// 下載檔（/api/file/）與靜態檔一律原樣通過，不要多一層包裝。
func apiJSON(r *http.Request) bool {
	return r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/")
}

// captureWriter 包住 POST /api/ 的回應，記下狀態碼與內容。
// API 失敗時狀態碼仍是 200、只有 ok=false，所以一定要看內容才知道有沒有出錯。
type captureWriter struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (c *captureWriter) WriteHeader(code int) {
	if c.status == 0 {
		c.status = code
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *captureWriter) Write(b []byte) (int, error) {
	if c.status == 0 {
		c.status = http.StatusOK
	}
	if c.body.Len() < 64<<10 { // 正常回應只有幾 KB；太大就不留，反正不是給人看的
		c.body.Write(b)
	}
	return c.ResponseWriter.Write(b)
}

// finish 把「使用者看到的失敗」寫進錯誤紀錄：HTTP 4xx／5xx，或 200 但 ok=false。
func (c *captureWriter) finish(r *http.Request) {
	body := c.body.Bytes()
	if c.status < 400 && !isFailedJSON(body) {
		return
	}
	recordError("後端", r.Method+" "+r.URL.Path, apiErrText(body, c.status), c.status)
}

func isFailedJSON(body []byte) bool {
	var m map[string]any
	if json.Unmarshal(body, &m) != nil {
		return false
	}
	ok, isBool := m["ok"].(bool)
	return isBool && !ok
}

// apiErrText 從 API 的回應挑出要留給使用者看的錯誤文字。
// CMYK 的失敗寫在 report 裡（ok=false 但沒有 message），所以要把 ❌ 那幾行挑出來。
func apiErrText(body []byte, status int) string {
	var r struct {
		Message string   `json:"message"`
		Report  []string `json:"report"`
	}
	if json.Unmarshal(body, &r) != nil {
		return fmt.Sprintf("HTTP %d：%s", status, strings.TrimSpace(string(body)))
	}
	if r.Message != "" {
		return r.Message
	}
	var bad []string
	for _, line := range r.Report {
		if strings.Contains(line, "❌") {
			bad = append(bad, line)
		}
	}
	if len(bad) == 0 && len(r.Report) > 0 {
		bad = r.Report[len(r.Report)-1:]
	}
	if len(bad) == 0 {
		return fmt.Sprintf("HTTP %d（回應沒有訊息）", status)
	}
	return strings.Join(bad, "\n")
}

// handleClientError 收前端回報的錯誤。內容一律當成不可信的字串，只切長度不解析。
func handleClientError(w http.ResponseWriter, r *http.Request) {
	var e struct {
		Where   string `json:"where"`
		Message string `json:"message"`
		Stack   string `json:"stack"`
		URL     string `json:"url"`
	}
	// 前端壞掉時可能亂送，這裡的錯誤不影響回應
	_ = json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&e)
	msg := e.Message
	if e.Stack != "" {
		msg += "\n" + e.Stack
	}
	if e.URL != "" {
		msg += "\n頁面：" + e.URL
	}
	where := strings.Join(strings.Fields(e.Where), " ")
	if where == "" {
		where = "(未具名頁面)"
	}
	recordError("前端", where, msg, 0)
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"ok":true}`+"\n")
}

// handleErrorsExport 下載錯誤紀錄。檔名固定用 ASCII，不必用到 filename*。
func handleErrorsExport(w http.ResponseWriter, r *http.Request) {
	name := "pdf-toolbox-errors-" + time.Now().Format("20060102-150405") + ".txt"
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename="+name)
	io.WriteString(w, errorReport())
}

// errorReport 組出給人看的匯出內容：環境、錯誤清單、兩份 log 的尾端。
func errorReport() string {
	items, dropped := errSnapshot()
	var b strings.Builder
	fmt.Fprintf(&b, "PDF Toolbox 錯誤紀錄\n匯出時間：%s\n\n", time.Now().Format("2006-01-02 15:04:05 -07:00"))

	b.WriteString("── 環境 ──\n")
	b.WriteString(envReport())

	b.WriteString("\n── 錯誤紀錄 ──\n")
	switch {
	case len(items) == 0:
		b.WriteString("（這次執行沒有記錄到錯誤）\n")
	default:
		fmt.Fprintf(&b, "共 %d 筆", len(items))
		if dropped > 0 {
			fmt.Fprintf(&b, "，另有 %d 筆較舊的已捨棄", dropped)
		}
		b.WriteString("\n")
		for _, e := range items {
			head := fmt.Sprintf("[%s] %s %s", e.When.Format("15:04:05"), e.Where, e.Tool)
			if e.Code >= 400 { // API 失敗時狀態碼仍是 200，只有 ok=false，那種就不顯示狀態碼
				head += " → HTTP " + strconv.Itoa(e.Code)
			}
			fmt.Fprintf(&b, "%s\n%s\n", head, indent(e.Msg))
		}
	}

	for _, f := range []struct{ label, path string }{
		{"本次執行紀錄", logPath},
		{"上次執行紀錄", prevLogPath()},
	} {
		fmt.Fprintf(&b, "\n── %s", f.label)
		if f.path != "" {
			fmt.Fprintf(&b, "（%s）", f.path)
		}
		b.WriteString(" ──\n")
		tail, err := tailFile(f.path, logTailLines)
		switch {
		case f.path == "":
			b.WriteString("（沒有這份檔案）\n")
		case os.IsNotExist(err):
			b.WriteString("（沒有這份檔案，通常是第一次執行）\n")
		case err != nil:
			fmt.Fprintf(&b, "（讀不到：%v）\n", err)
		case strings.TrimSpace(tail) == "":
			b.WriteString("（空的）\n")
		default:
			b.WriteString(tail)
			if !strings.HasSuffix(tail, "\n") {
				b.WriteString("\n")
			}
		}
	}
	return b.String()
}

// envReport 是匯出檔開頭的環境資訊：出問題時最先要看的就是版本與 gs 有沒有找到。
func envReport() string {
	var b strings.Builder
	exe, _ := os.Executable()
	fmt.Fprintf(&b, "程式：%s\n", exe)
	if fi, err := os.Stat(exe); err == nil {
		fmt.Fprintf(&b, "程式建置時間／大小：%s，%d bytes\n", fi.ModTime().Format("2006-01-02 15:04:05"), fi.Size())
	}
	fmt.Fprintf(&b, "啟動時間：%s（已執行 %s）\n", startTime.Format("2006-01-02 15:04:05"), time.Since(startTime).Round(time.Second))
	fmt.Fprintf(&b, "服務：http://%s\n", addr)
	fmt.Fprintf(&b, "作業系統：%s\n", osVersion())
	fmt.Fprintf(&b, "執行環境：%s %s，%s\n", runtime.GOOS, runtime.GOARCH, runtime.Version())
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if strings.HasPrefix(s.Key, "vcs.") {
				fmt.Fprintf(&b, "建置資訊 %s：%s\n", s.Key, s.Value)
			}
		}
	}
	if gsPath == "" {
		fmt.Fprintf(&b, "Ghostscript：找不到\n")
	} else {
		fmt.Fprintf(&b, "Ghostscript：%s（%s）\n", gsVersion, gsPath)
	}
	fmt.Fprintf(&b, "暫存目錄：%s\n", workDir)
	return b.String()
}

// osVersion 讀 Windows 的版本字串；非 Windows 或讀不到就退回 GOOS。
// cmd /u 的輸出是 UTF-16LE：直接讀管線時 cmd 用主控台字碼頁（繁中是 CP950），
// 版本字串裡在地化的字會變成非 UTF-8 的位元組，匯出檔就整段變亂碼。
// 要 hideWindow，否則 GUI 程式呼叫 cmd 會閃一個黑色視窗。
func osVersion() string {
	if runtime.GOOS != "windows" {
		return runtime.GOOS
	}
	cmd := exec.Command("cmd", "/u", "/c", "ver")
	hideWindow(cmd)
	out, err := cmd.Output()
	if err != nil {
		return runtime.GOOS
	}
	s := strings.Join(strings.Fields(decodeUTF16(out)), " ")
	if !strings.ContainsAny(s, "0123456789") { // 不是預期的編碼時，至少不要吐亂碼
		s = strings.Join(strings.Fields(asciiOnly(string(out))), " ")
	}
	if s == "" {
		return runtime.GOOS
	}
	return s
}

// decodeUTF16 解 cmd /u 的輸出（UTF-16LE，開頭可能有 BOM）；控制字元一律拿掉。
func decodeUTF16(b []byte) string {
	if len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE {
		b = b[2:]
	}
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		u = append(u, uint16(b[i])|uint16(b[i+1])<<8)
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7F || r == 0xFFFD {
			return ' '
		}
		return r
	}, string(utf16.Decode(u)))
}

func asciiOnly(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7E {
			return ' '
		}
		return r
	}, s)
}

func prevLogPath() string {
	if logPath == "" {
		return ""
	}
	return strings.TrimSuffix(logPath, ".log") + ".prev.log"
}

// openLog 開一份新的 pdf-toolbox.log，上一份先改名成 pdf-toolbox.prev.log。
// 當掉重開時，當掉那次的紀錄還在，才追得到。
func openLog(dir string) (*os.File, error) {
	logPath = filepath.Join(dir, "pdf-toolbox.log")
	if fileExists(logPath) {
		// 改名失敗（例如另一個實例還開著）就算了，不要因此開不了 log
		os.Rename(logPath, prevLogPath())
	}
	return os.Create(logPath)
}

// tailFile 回傳檔案最後 n 行。紀錄檔都不大，直接整份讀進來切。
func tailFile(path string, n int) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) > n {
		lines = append([]string{fmt.Sprintf("（只列最後 %d 行，完整內容見上面的路徑）", n)}, lines[len(lines)-n:]...)
	}
	return strings.Join(lines, "\n") + "\n", nil
}

func indent(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = "    " + l
	}
	return strings.Join(lines, "\n")
}

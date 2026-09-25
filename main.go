// PDF Toolbox：把幾個前端 PDF／表格工具與 CMYK 轉換包成可攜版。
// 啟動本機 HTTP 服務，再用 Edge 的 app 模式開視窗；視窗關掉後一段時間沒請求就自動結束。
package main

import (
	"embed"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"time"
)

//go:embed all:web
var webFS embed.FS

// ponytail: 固定 port——localStorage 以 origin（含 port）區分，換 port 各工具的設定就會消失
const addr = "127.0.0.1:17831"

var (
	exeDir  string
	workDir string // 本次執行的暫存（ICC、轉檔結果），結束時整個刪掉
	lastHit atomic.Int64
	busy    atomic.Int32
)

func main() {
	dev := flag.Bool("dev", false, "只啟動服務：不開視窗、不自動結束")
	flag.Parse()
	exe, _ := os.Executable()
	exeDir = filepath.Dir(exe)
	url := "http://" + addr + "/"

	// GUI 程式沒有主控台，訊息寫到 exe 旁邊的 log
	if f, err := os.Create(filepath.Join(exeDir, "pdf-toolbox.log")); err == nil {
		log.SetOutput(io.MultiWriter(os.Stderr, f))
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		if r, err := http.Get(url + "api/ping"); err == nil && r.StatusCode == 200 {
			openWindow(url) // 已經在跑：只開視窗
			return
		}
		log.Fatalf("port %s 被其他程式占用：%v", addr, err)
	}
	if workDir, err = os.MkdirTemp("", "pdf-toolbox-"); err != nil {
		log.Fatal(err)
	}
	initCMYK()

	// Windows 的登錄檔可能把 .js 對應成 text/plain，ES module 會因此載入失敗
	mime.AddExtensionType(".js", "text/javascript; charset=utf-8")
	mime.AddExtensionType(".mjs", "text/javascript; charset=utf-8")

	web, _ := fs.Sub(webFS, "web")
	mux := http.NewServeMux()
	mux.Handle("/", http.FileServerFS(web))
	mux.HandleFunc("GET /api/ping", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "pdf-toolbox") })
	mux.HandleFunc("GET /recompose/fonts/NotoSansTC-Regular.ttf", serveCJKFont)
	mux.HandleFunc("POST /api/cmyk", handleCMYK)
	mux.HandleFunc("POST /api/compress", handleCompress)
	mux.HandleFunc("POST /api/protect", handleProtect)
	mux.HandleFunc("POST /api/images", handleImages)
	mux.HandleFunc("GET /api/file/{id}/{name}", handleFile)

	lastHit.Store(time.Now().Unix())
	if !*dev {
		go openWindow(url)
		go exitWhenIdle(5 * time.Minute)
	}
	log.Println("PDF Toolbox 服務：", url)
	log.Fatal(http.Serve(ln, guard(mux)))
}

func guard(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 只接受自己的網址：擋 DNS rebinding，也擋其他網站對本服務的跨站 POST
		if r.Host != addr || (r.Method != http.MethodGet && r.Header.Get("Origin") != "http://"+addr) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		busy.Add(1)
		defer func() { lastHit.Store(time.Now().Unix()); busy.Add(-1) }()
		w.Header().Set("Cache-Control", "no-cache") // 換新版 exe 後不會吃到舊檔
		h.ServeHTTP(w, r)
	})
}

// ponytail: 靠首頁每 30 秒 ping 判斷視窗還開著；背景分頁的計時器最慢一分鐘一次，所以門檻抓 5 分鐘
func exitWhenIdle(idle time.Duration) {
	for range time.Tick(30 * time.Second) {
		if busy.Load() == 0 && time.Since(time.Unix(lastHit.Load(), 0)) > idle {
			os.RemoveAll(workDir)
			os.Exit(0)
		}
	}
}

func openWindow(url string) {
	switch runtime.GOOS {
	case "darwin":
		exec.Command("open", url).Start()
		return
	case "linux":
		exec.Command("xdg-open", url).Start()
		return
	}
	for _, p := range []string{
		os.Getenv("ProgramFiles(x86)") + `\Microsoft\Edge\Application\msedge.exe`,
		os.Getenv("ProgramFiles") + `\Microsoft\Edge\Application\msedge.exe`,
		os.Getenv("ProgramFiles") + `\Google\Chrome\Application\chrome.exe`,
		os.Getenv("LocalAppData") + `\Google\Chrome\Application\chrome.exe`,
	} {
		if _, err := os.Stat(p); err == nil {
			// 獨立 profile 放在 exe 旁邊：設定跟著資料夾走，也不干擾平常用的瀏覽器
			exec.Command(p, "--app="+url, "--user-data-dir="+filepath.Join(exeDir, "profile"),
				"--no-first-run", "--window-size=1400,900").Start()
			return
		}
	}
	exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}

// serveCJKFont 讓 pdf_recompose 用電腦內建的中文字型，不必在包裡帶 7 MB 的思源黑體。
// 標楷體是單一 .ttf（正黑體、細明體是 .ttc 集合，pdf-lib 不能直接嵌入）；Arial Unicode 只給 Mac 開發用。
// 都找不到時回 404，pdf_recompose 會退回只用英數字型。
func serveCJKFont(w http.ResponseWriter, r *http.Request) {
	for _, p := range []string{
		filepath.Join(os.Getenv("WINDIR"), "Fonts", "kaiu.ttf"),
		"/System/Library/Fonts/Supplemental/Arial Unicode.ttf",
	} {
		if fileExists(p) {
			http.ServeFile(w, r, p)
			return
		}
	}
	http.NotFound(w, r)
}

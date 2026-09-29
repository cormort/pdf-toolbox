package main

import (
	"path/filepath"
	"runtime"

	webview2 "github.com/jchv/go-webview2"
)

// WebView2 的視窗必須跑在主執行緒
func init() { runtime.LockOSThread() }

// showWindow 用 WebView2 開視窗，關掉才回傳 true；電腦沒有 WebView2 執行階段時立刻回傳 false，
// 呼叫端改走 Edge --app。Win10 21H2 起與 Win11 都內建執行階段。
func showWindow(url string) bool {
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		// ponytail: Debug 同時打開右鍵選單（複製、貼上）與開發者工具，套件沒辦法只開前者
		Debug:     true,
		AutoFocus: true,
		// 跟 Edge 版分開放：兩者的 profile 格式不同，混用會互相弄壞
		DataPath: filepath.Join(exeDir, "profile", "webview2"),
		WindowOptions: webview2.WindowOptions{
			Title: "PDF Toolbox", Width: 1400, Height: 900, Center: true,
			IconId: 1, // rsrc_windows_amd64.syso 裡的圖示群組 ID
		},
	})
	if w == nil {
		return false
	}
	defer w.Destroy()
	w.Navigate(url)
	w.Run()
	return true
}

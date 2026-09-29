//go:build !windows

package main

// 非 Windows（開發用）沒有 WebView2，一律用預設瀏覽器開
func showWindow(string) bool { return false }

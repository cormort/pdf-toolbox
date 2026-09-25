# pdf-toolbox

把幾個 PDF／表格小工具包成 Windows x64 可攜版（免安裝、可離線）。

| 工具 | 來源 | 型態 |
|---|---|---|
| pdf-row-shifter | [cormort/pdf-row-shifter](https://github.com/cormort/pdf-row-shifter) | 靜態 HTML |
| xls2spread | [cormort/xls2spread](https://github.com/cormort/xls2spread) | 靜態 HTML |
| diffpdf-web | [cormort/diffpdf-web](https://github.com/cormort/diffpdf-web) | 靜態 HTML |
| pdfviewer_v2 | [cormort/pdfviewer_v2](https://github.com/cormort/pdfviewer_v2) | 靜態網站 + Service Worker |
| PDF_RGB2CMYK | [HF Space](https://huggingface.co/spaces/cormort/PDF_RGB2CMYK) | Python + Gradio + Ghostscript |

## 架構

四個前端工具不改架構，外殼只提供「本機 HTTP + 瀏覽器視窗」：

- `PdfToolbox.exe`（Go）：`embed` 網頁、`net/http` 服務於 `127.0.0.1:<固定 port>`，
  以 `msedge --app=... --user-data-dir=.\profile` 開視窗。
- 不能用 `file://`：ES module import、Service Worker、pdf.js worker 都會被擋。
- port 固定：localStorage 以 origin（含 port）區分，換 port 設定會消失。
- 在 macOS 交叉編譯：`GOOS=windows GOARCH=amd64 go build`。

```
PdfToolbox\
  PdfToolbox.exe
  profile\      ← Edge 設定（首次執行建立）
  cmyk\         ← Phase 2：python\, gs\, poppler\, app.py, *.icc
```

## 路線

- [ ] **Phase 0 前端離線化**：拔 gtag；pdf.js / pdfjs-dist / jsPDF 本地化到 `vendor/`；
      Google Fonts 改系統字型；build script 複製各工具到 `webroot/<tool>/`
- [ ] **Phase 1 啟動器**：首頁磁磚連到各工具子路徑；固定 port；可攜 Edge profile
- [ ] **Phase 2 CMYK**：本機 sidecar（Python embeddable + gswin64c + poppler），
      由啟動器按需啟動 `app.py`；DOCX（LibreOffice）暫不帶
- [ ] **Phase 3（選配）**：工具間傳檔（同 origin，IndexedDB / BroadcastChannel）

## 注意

- pdfviewer 的 Service Worker 快取會讓改版後跑舊版：打包版不註冊 SW，或每次 build 跑 `bump-cache`。
- Ghostscript 為 AGPL：內部使用無妨，對外散布需一併提供原始碼。
- 未簽章 exe 會被 SmartScreen 警告；公部門電腦需先確認執行檔政策。

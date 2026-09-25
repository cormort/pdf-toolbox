# pdf-toolbox

把幾個 PDF／表格小工具包成 **Windows x64 可攜版**：解壓縮、雙擊 `PdfToolbox.exe` 就能用，免安裝、可離線，檔案不離開本機。

| 分頁 | 來源 | 說明 |
|---|---|---|
| 閱讀與註解 | [pdfviewer_v2](https://github.com/cormort/pdfviewer_v2) | 靜態網站 |
| 資料列分頁調整 | [pdf-row-shifter](https://github.com/cormort/pdf-row-shifter) | 單一 HTML |
| 對開表排版 | [xls2spread](https://github.com/cormort/xls2spread) | 單一 HTML |
| PDF 比對 | [diffpdf-web](https://github.com/cormort/diffpdf-web) | 單一 HTML |
| RGB → CMYK | 本 repo（Go 重寫 [PDF_RGB2CMYK](https://huggingface.co/spaces/cormort/PDF_RGB2CMYK) 的核心） | Go + Ghostscript |

## 架構

```
PdfToolbox\
  PdfToolbox.exe   Go：內嵌 web/ 與 ICC，服務 http://127.0.0.1:17831，用 Edge --app 開視窗
  gs\bin\          Ghostscript 10.08.0（gswin64c + gsdll64 + VC++ 執行階段）
  profile\         Edge 設定（首次執行產生）
  pdf-toolbox.log  執行紀錄
```

- **為什麼要本機 HTTP**：ES module、Service Worker、pdf.js worker 在 `file://` 都會被擋。
- **port 固定 17831**：localStorage 以 origin（含 port）區分，換 port 各工具設定會消失。
- **生命週期**：首頁每 30 秒 ping，5 分鐘沒請求就自動結束；已在執行時再雙擊只會開視窗。
- **安全**：只聽 127.0.0.1，拒絕 Host 不符（DNS rebinding）與跨站 POST。
- **CMYK 不帶 Python**：單色黑前處理（R=G=B 的 RGB 改 DeviceGray → 轉出來只有 K）用 Go + pdfcpu 重寫，
  轉換本身是一行 Ghostscript，預檢的總墨量／四色黑改用 Go 讀 gs 的點陣輸出。

## 開發

```bash
./sync.sh          # 工具有更新時：從 GitHub 拉四個工具到 web/，拿掉 gtag、CDN 換成 web/vendor/
go run . -dev      # 開 http://127.0.0.1:17831（-dev：不開視窗、不自動結束）
go test ./...      # 內容串流改寫＋端到端（需要 gs 在 PATH）
./build.sh         # 產出 dist/PdfToolbox/ 與 dist/PdfToolbox.zip（需要 go、7z、curl）
```

Windows 上開發：裝 Go 後同樣 `go run . -dev`；測試前把 `dist\PdfToolbox\gs\bin` 加進 PATH。
`sync.sh`／`build.sh` 用 Git Bash 跑（build 另需 7-Zip）。

## Windows 驗收清單（Mac 上測不到的部分）

- [ ] 雙擊 exe 會開 Edge app 視窗；SmartScreen 警告時「其他資訊 → 仍要執行」
- [ ] 五個分頁都能開 PDF／xlsx
- [ ] CMYK：中文檔名、中文使用者名稱（暫存在 `%TEMP%`）都能轉；轉換時不會閃黑色主控台視窗
- [ ] 沒裝 VC++ 可轉散發套件的乾淨電腦也能轉 CMYK
- [ ] 關掉視窗後，工作管理員裡的 PdfToolbox.exe 5 分鐘內消失
- [ ] 執行中再雙擊 exe，只會多開一個視窗
- [ ] 下載的檔案進「下載」資料夾，檔名正確

## 待辦

CMYK 預檢還沒移植的（原 Python 版有）：
- [ ] 圖片有效解析度（< 300 / 200 ppi）
- [ ] 成品尺寸、TrimBox／BleedBox、貼邊未出血
- [ ] 透明、疊印、特別色
- [ ] 小字（< 6 pt）與細線（< 0.25 pt）
- [ ] DOCX 輸入（要帶 LibreOffice，先請使用者從 Word 存 PDF）

其他：
- [ ] 工具間傳檔（例如比對結果送 CMYK），有需要再做
- [ ] exe 圖示與版本資訊

## 授權

- Ghostscript：AGPL v3（`gs\COPYING`），原始碼：https://github.com/ArtifexSoftware/ghostpdl
- pdfcpu：Apache 2.0；pdf.js：Apache 2.0；jsPDF：MIT；pdfviewer_v2 的第三方聲明見 `web/viewer/THIRD-PARTY-NOTICES.md`
- `JapanColor2011Coated.icc`：© X-Rite／JPMA，依 ICC Profile Registry 條款可自由使用與散布，不得修改或販售
- `sRGB_IEC61966-2-1_no_black_scaling.icc`：© International Color Consortium
- Microsoft VC++ 執行階段 DLL：依 Visual Studio 可轉散發條款隨附

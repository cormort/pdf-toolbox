# pdf-toolbox

把幾個 PDF／表格小工具包成 **Windows x64 可攜版**：解壓縮、雙擊 `PdfToolbox.exe` 就能用，免安裝、可離線，檔案不離開本機。

| 分頁 | 來源 | 說明 |
|---|---|---|
| 閱讀與註解 | [pdfviewer_v2](https://github.com/cormort/pdfviewer_v2) | 靜態網站 |
| 頁面重組 | [pdf_recompose](https://github.com/cormort/pdf_recompose) | 靜態網站：合併、拆分、排序、旋轉、目次頁與書籤、頁碼／頁首頁尾、浮水印、一頁多張／騎馬釘、裁白邊 |
| 資料列分頁調整 | [pdf-row-shifter](https://github.com/cormort/pdf-row-shifter) | 單一 HTML |
| 對開表排版 | [xls2spread](https://github.com/cormort/xls2spread) | 單一 HTML |
| PDF 比對 | [diffpdf-web](https://github.com/cormort/diffpdf-web) | 單一 HTML |
| 壓縮 PDF | 本 repo（`compress.go`、`web/compress/`） | Go + Ghostscript |
| 密碼保護 | 本 repo（`protect.go`、`web/protect/`） | Go + pdfcpu：加密（AES-256／128）、權限三態、移除保護 |
| PDF 轉圖片 | 本 repo（`images.go`、`web/images/`） | Go + Ghostscript |
| 取出內嵌圖片 | 本 repo（`extract.go`、`web/extract/`） | Go + pdfcpu：拿原始嵌入的圖，不是整頁點陣化 |
| 中繼資料 | 本 repo（`metadata.go`、`web/metadata/`） | Go + pdfcpu：讀出／修改／清除 Info 字典與 XMP |
| PDF 轉文字 | 本 repo（`pdftext.go`、`web/text/`） | Go + Ghostscript `txtwrite`：保留版面，欄位會對齊 |
| 頁面尺寸統一 | 本 repo（`resize.go`、`web/resize/`） | Go + Ghostscript `-dPDFFitPage` |
| RGB → CMYK | 本 repo（Go 重寫 [PDF_RGB2CMYK](https://huggingface.co/spaces/cormort/PDF_RGB2CMYK) 的核心） | Go + Ghostscript |

## 架構

```
PdfToolbox\
  PdfToolbox.exe          Go：內嵌 web/ 與 ICC，服務 http://127.0.0.1:17831，用 Edge --app 開視窗
  gs\bin\                 Ghostscript 10.08.0（gswin64c + gsdll64 + VC++ 執行階段）
  profile\                Edge 設定（首次執行產生）
  pdf-toolbox.log         本次執行的紀錄
  pdf-toolbox.prev.log    上一次執行的紀錄（每次啟動時把上一份改名過來，當掉重開還追得到）
  pdf-toolbox-errors-*.txt 使用者按「匯出錯誤紀錄」下載的檔案
```

- **為什麼要本機 HTTP**：ES module、Service Worker、pdf.js worker 在 `file://` 都會被擋。
- **port 固定 17831**：localStorage 以 origin（含 port）區分，換 port 各工具設定會消失。
- **生命週期**：首頁每 30 秒 ping，5 分鐘沒請求就自動結束；已在執行時再雙擊只會開視窗。
- **暫存**：上傳檔與轉檔結果放在 `%TEMP%\pdf-toolbox-*`，程式結束時整個刪掉；同一場工作階段裡超過 60 分鐘
  沒下載的結果也會先清掉（下載連結到那時候失效）。預檢的點陣資料直接從 gs 的標準輸出串流進來，不落地。
- **錯誤匯出**：GUI 沒有主控台，出錯時使用者手上沒有東西可以回報。所以首頁右上角有「匯出錯誤紀錄」，
  下載一份純文字檔，內含環境（版本、Windows、Ghostscript、暫存目錄）、最近 200 筆錯誤與兩份 log 的尾端。
  收集的來源是後端（HTTP 4xx／5xx、`ok=false`、panic）與前端（首頁統一攔 shell 與各 iframe 的 JS 例外、
  未處理的 promise），前端只回報同一個錯誤第一次，免得壞掉的頁面把紀錄洗掉。按鈕上的數字就是目前的筆數。
- **安全**：只聽 127.0.0.1，拒絕 Host 不符（DNS rebinding）與跨站 POST。
- **CMYK 不帶 Python**：單色黑前處理（R=G=B 的 RGB 改 DeviceGray → 轉出來只有 K）用 Go + pdfcpu 重寫，
  轉換本身是一行 Ghostscript，預檢的總墨量／四色黑改用 Go 讀 gs 的點陣輸出，
  字型嵌入與圖片有效解析度直接走 PDF 結構（不需要 poppler）。

## 開發

```bash
./sync.sh          # 工具有更新時：從 GitHub 拉四個工具到 web/，拿掉 gtag、CDN 換成 web/vendor/
go run . -dev      # 開 http://127.0.0.1:17831（-dev：不開視窗、不自動結束）
go test ./...      # 內容串流改寫＋端到端（需要 gs 在 PATH）
./smoke.sh         # 對執行中的服務打一輪 API：守衛、各工具、錯誤訊息（需要 gs、python）
./build.sh         # 產出 dist/PdfToolbox/ 與 dist/PdfToolbox.zip（需要 go、7z、curl）
                   # 打包不會帶執行時產生的 profile/；PdfToolbox 視窗還開著時會直接停下來要你先關掉
node hover-check.mjs  # 用 Edge 實際量 hover／focus 的樣式（需要 Node 18+ 與 Edge；改 CSS 時跑）
```

Windows 上開發：裝 Go 後同樣 `go run . -dev`；測試前把 `dist\PdfToolbox\gs\bin` 加進 PATH。
`sync.sh`／`build.sh` 用 Git Bash 跑（build 另需 7-Zip）。

### 樣式改動怎麼驗（`hover-check.mjs`）

`web/tool.css` 的通用規則、各頁規則與按鈕變體，是靠權重與先後順序決定誰贏。這種「平常看沒問題、
滑過去才發現被蓋掉」的錯（例如已選取的按鈕被 hover 規則塗掉）看程式碼不保險，所以有一支檢查：

```bash
node hover-check.mjs     # 自己起靜態伺服器與 headless Edge，跑完收乾淨；全過 exit 0，有 ✗ exit 1
EDGE=<msedge 路徑> node hover-check.mjs   # 找不到 Edge 時自己指定
```

它送**真的滑鼠與鍵盤事件**，再讀 computed style，比對的是頁面上的 CSS 變數（所以淺色／深色模式都適用）。
檢查範圍：首頁分頁、密碼頁的分段控制、主要／危險／停用三種按鈕的 hover、以及鍵盤 focus 的外框。
會擋下來的例子：`node hover-check.mjs` 對 `.seg` 的 hover 規則跑一次就會出現
「✗ 已按下 滑過要維持 --accent：rgb(58, 58, 61)」。

### 圖示

- `web/icons/favicon.ico` 同時當瀏覽器 favicon 與 exe 圖示，內含 16／32／48／64／128／256 六種尺寸
  （`index.html` 另外連了 16、32 的 PNG 與 apple-touch-icon）。
- exe 的圖示是編進 `rsrc_windows_amd64.syso`（Go 會自動連結同目錄的 `.syso`；不同 GOOS／GOARCH 不會被帶進去）。
  換圖示時重新產生一次並一起 commit：

  ```bash
  go run github.com/akavel/rsrc@v0.10.2 -ico web/icons/favicon.ico -o rsrc_windows_amd64.syso
  ```

  建置本身不需要這個工具（`build.sh` 只檢查 `.syso` 在不在，不會連網）。

## Windows 驗收清單（Mac 上測不到的部分）

打勾的項目已在 Windows 11 Pro（繁中）上以 v0.1.5–v0.1.8 驗過。

- [x] 雙擊 exe 會開 Edge app 視窗；exe 放在含中文、空格、`&` 的路徑也可以
- [ ] SmartScreen 警告時「其他資訊 → 仍要執行」（要用從 GitHub 下載、帶網路標記的 zip 測）
- [x] `pdf-toolbox.log` 有寫入（v0.1.7 起；之前的版本是空檔）
- [x] 能開 PDF 並正常運作：閱讀與註解、頁面重組、PDF 比對、壓縮 PDF、PDF 轉圖片、RGB → CMYK、密碼保護
- [ ] 資料列分頁調整、對開表排版：用真的表格 PDF／xlsx 走完一次（目前只確認頁面載入）
- [x] CMYK：中文檔名、`%TEMP%` 在中文路徑下都能轉，下載檔名正確
- [ ] 轉換時不會閃黑色主控台視窗（程式有設 `CREATE_NO_WINDOW`，要用眼睛看）
- [ ] 沒裝 VC++ 可轉散發套件的乾淨電腦也能轉 CMYK
- [x] 關掉視窗後，工作管理員裡的 PdfToolbox.exe 5 分鐘內消失
- [x] 執行中再雙擊 exe，不會多一個 PdfToolbox.exe
- [ ] 執行中再雙擊 exe，畫面上多開一個視窗
- [ ] 下載的檔案進「下載」資料夾，檔名正確（在 Edge app 視窗裡按下載）
- [x] 頁面重組的目次、頁碼、浮水印中文用標楷體（`C:\Windows\Fonts\kaiu.ttf`）嵌入並正常顯示；英文版 Windows 需先裝「繁體中文補充字型」

## 待辦

CMYK 預檢（原 Python 版的項目已全部移植）：
- [x] 圖片有效解析度（< 300 / 200 ppi）：追蹤 CTM、Form、軟遮罩群組與行內圖片，跳過預設隱藏的圖層；
      與 poppler `pdfimages -list` 在 164 份 PDF、31,892 筆繪圖上逐筆一致（±1 ppi）
- [x] 成品尺寸、TrimBox／BleedBox 出血、貼邊未出血、頁數是否為 4 的倍數；框超出 MediaBox 時依規範取交集；
      只有左或右一側沒出血時視為書背側，列為需確認而非錯誤。
      讀框與旋轉在 299 份 PDF、9,324 頁上與 poppler `pdfinfo -box` 逐頁一致
- [x] 透明、疊印、特別色：看頁面與 Form 的資源字典（gs 輸出只保留用到的資源）。與原 Python 版在 299 份 PDF 上比對，
      297 份一致；另 2 份是原版的去重 bug（直接物件的資源字典被當成已走訪）漏報，Go 版正確
- [x] 小字（< 6 pt）與細線（< 0.25 pt，含表格細長矩形框線與線寬 0）：與原 Python 版在 297 份 PDF 上逐頁一致；
      原版讀取失敗的 2 份 pdf2zh 輸出也能處理
- [ ] DOCX 輸入（要帶 LibreOffice，先請使用者從 Word 存 PDF）

其他（功能擴充，依建議順序）：
- [x] 頁面整理、浮水印、頁碼：由 pdf_recompose 提供
      已知：pdf_recompose 嵌入中文字型不子集化，每份輸出多一整個字型檔。pdf-lib 的 `subset: true` 會缺字，
      經 gs 重寫則中文無法搜尋，暫維持現況；可評估改用 `@cantoo/pdf-lib`
- [x] 壓縮 PDF（gs，三種程度，色彩不變）：壓縮後在瀏覽器用 pdf.js 逐頁比對文字（NFC、不看順序），有字無法搜尋就警告並列出頁碼
- [x] 加密／移除保護（pdfcpu，AES-256）：開啟密碼、權限密碼與四項權限；移除保護預設比照 Acrobat 要權限密碼，
      沒有權限密碼時可以另外明確勾選「我沒有權限密碼，只用開啟密碼移除」改用開啟密碼
      （只鎖列印、開檔本來就不用密碼的檔案，留空即可解除）
- [x] PDF 轉圖片（gs）：PNG／JPG、72–600 dpi、頁碼範圍；多頁打包 ZIP，檔名帶實際頁碼

第二批（照 iThome Day 27「PDF 安全技術：密碼移除與文字解構」、WCT「移除 PDF 文件的已知密碼」、
IBM RPA「加密及解密 PDF 檔案」與 PDFsam 的功能清單補的）：
- [x] 權限選項加細：列印（允許／只允許低解析度草稿／不允許）、複製（允許／只允許無障礙工具／不允許）、
      修改（允許／只允許組合頁面／不允許）、註解表單（允許／只允許填表單與簽章／不允許），另有 AES-128／256
- [x] 移除保護的 gs 後援：pdfcpu 解不開的檔案改用 gs 重寫（「列印成 PDF」那招的自動版）；
      只在「我沒有權限密碼」那條路啟用，預設那條路不碰 gs，不然填開啟密碼也能移除保護
- [x] 取出內嵌圖片（pdfcpu）：保留原始解析度（跟「PDF 轉圖片」的整頁點陣化互補），可選頁碼、多張打包 ZIP
- [x] 中繼資料（pdfcpu）：讀出／修改／清除 Info 字典與 catalog、各頁的 XMP。清除後只會留下寫入工具自己的
      Producer 與時間戳（任何 PDF 寫入器都會蓋）
- [x] PDF 轉文字（gs `txtwrite`）：保留版面、欄位會對齊，可貼進 Excel；中文靠字型的 ToUnicode，
      沒有文字層的掃描檔會老實說抽不到（要 OCR）
- [x] 頁面尺寸統一（gs `-dPDFFitPage`）：A4／A3／A5／B5／Letter／Legal，直式或橫式，
      內容等比縮放並置中（小頁面也會被放大）

已知：Ghostscript 重寫 PDF 時，部分字型的文字對應會被丟掉或寫錯（畫面、列印正常，但無法搜尋）。61 份抽樣裡 12 份有此現象；
pdf-lib 嵌入的 Identity-H 中文字型可把原檔的 ToUnicode 補回（`tounicode.go`，壓縮與 CMYK 都會做），其他情況只能靠壓縮頁的文字比對提醒。
- [ ] Word／Excel／PPT 轉 PDF（呼叫本機 Office）、網頁轉 PDF（Edge headless）
- [ ] OCR（可攜 Tesseract + 繁中語言檔）、密文遮蔽（整頁點陣化）
- [ ] 工具間傳檔（例如比對結果送 CMYK），有需要再做
- [x] exe 圖示（`web/icons/` ＋ `rsrc_windows_amd64.syso`）；版本資訊還沒做

## 授權

- Ghostscript：AGPL v3（`gs\COPYING`），原始碼：https://github.com/ArtifexSoftware/ghostpdl
- pdfcpu：Apache 2.0；pdf.js：Apache 2.0；jsPDF：MIT；pdfviewer_v2 的第三方聲明見 `web/viewer/THIRD-PARTY-NOTICES.md`
- `JapanColor2011Coated.icc`：© X-Rite／JPMA，依 ICC Profile Registry 條款可自由使用與散布，不得修改或販售
- `sRGB_IEC61966-2-1_no_black_scaling.icc`：© International Color Consortium
- Microsoft VC++ 執行階段 DLL：依 Visual Studio 可轉散發條款隨附

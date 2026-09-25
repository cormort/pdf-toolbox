# 給 AI 代理的測試說明

這份是給協助測試的代理（Codex、Cline、DeepSeek 等）看的。專案說明見 README.md。

## 你的角色

測試並回報，不是改功能。除非使用者明確要求：

- 不要 commit、push、打 tag 或發布 release
- 不要改 `git config`（包括 `core.autocrlf`）
- 不要改 `web/` 底下由 `sync.sh` 帶進來的前端（viewer、recompose、row-shifter、xls2spread、diffpdf）：
  下次同步就被蓋掉。那些問題要回報，修正在上游 repo（`github.com/cormort/<repo>`）做
- 找到 bug 先回報重現步驟；使用者要你修，再修並補測試

## 環境

- Windows 11，用 Git Bash 跑 `.sh`
- 需要 Go（版本以 `go.mod` 為準）、Ghostscript 10.08.0（`gswin64c`）、7-Zip、python
- 有動到 `web/` 底下的樣式時，另外需要 Node 22+ 與 Edge（跑 `node hover-check.mjs`）
- 工具裝不起來時，放在 `%TEMP%` 底下的暫存資料夾，**不要放進 repo**，也不要改系統 PATH
- Go 的快取也指到暫存資料夾：`GOPATH`、`GOCACHE`、`GOTOOLCHAIN=local`
- 在 repo 裡建的暫存測試檔（例如 `zz_tmp_*_test.go`）結束前一定要刪掉

## 測試步驟

依序跑，每一步都記下結果：

```bash
gofmt -l .                 # 應該沒有輸出
go vet ./...               # 應該沒有輸出
go test -count=1 ./...     # 要有 gs 在 PATH，否則端到端測試會被 Skip（Skip 也要回報）
node hover-check.mjs       # 只有改到 web/ 的樣式才需要：用 Edge 實際量 hover／focus（會自己起服務）
go run . -dev              # 另開一個終端機跑；服務在 http://127.0.0.1:17831
./smoke.sh                 # API、守衛、各工具的煙霧測試，印出 ✓／✗
./build.sh                 # 產出 dist/PdfToolbox.zip
```

`smoke.sh` 之前先確認沒有別的 `PdfToolbox.exe` 在跑（`tasklist | grep -i pdftoolbox`）。
port 17831 被占用時，`go run . -dev` 會直接結束，`smoke.sh` 就會打到舊的程式，結果不算數。

`hover-check.mjs` 不用等服務起來（它自己起靜態伺服器與 headless Edge）。沒動到樣式時，
回報裡寫「未測：沒有改到樣式」即可；它擋的是 CSS 權重／順序造成的樣式覆蓋問題，
那種問題 `gofmt`、`go test`、`smoke.sh` 都看不出來。

## 已知的陷阱（不是 bug）

- 用 `localhost` 連會 403：守衛只認 `127.0.0.1:17831`
- 用 curl 打 POST 要帶 `Origin: http://127.0.0.1:17831`，否則 403
- Windows 的 curl 會用 Big5 送中文檔名與表單值，下載檔名就會亂碼。瀏覽器送的是 UTF-8，
  要測中文檔名請用 python 組 multipart，或用瀏覽器測
- 閱讀器、頁面重組等前端要在瀏覽器裡測，curl 只能確認頁面載得到

## 手動驗收

README 的「Windows 驗收清單」裡沒打勾的項目需要人或能操作桌面的代理。能做的就做，
做不到的標明「未測」，不要猜。

## 回報格式

```
版本／commit：<git log --oneline -1>
環境：Windows 版本、Go 版本、gs 版本

| 步驟 | 結果 |
| gofmt / vet | 乾淨 / 列出問題 |
| go test | N/N 通過，Skip 幾個 |
| hover-check.mjs | 全部通過 / 列出 ✗ 的項目（沒動樣式寫「未測：沒有改到樣式」）|
| smoke.sh | 全部通過 / 列出 ✗ 的項目與輸出 |
| build.sh | 成功，zip 大小與 SHA-256 |

發現的問題：每個問題寫重現步驟、預期結果、實際結果
未測項目：列出來並說明原因
repo 狀態：git status 應該是乾淨的（除非使用者要你改東西）
```

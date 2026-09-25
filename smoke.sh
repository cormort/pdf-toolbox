#!/usr/bin/env bash
# 對執行中的服務（go run . -dev 或 PdfToolbox.exe）打一輪 API，檢查守衛、各工具與錯誤訊息。
# 需要 gs（產生測試 PDF）、curl、python。每項印 ✓／✗，有 ✗ 時結束碼為 1。
#   ./smoke.sh                      # 服務在 127.0.0.1:17831
set -uo pipefail
B=http://127.0.0.1:17831
O="Origin: $B"
GS=$(command -v gswin64c || command -v gs) || { echo "找不到 gs" >&2; exit 2; }
curl -fs "$B/api/ping" >/dev/null || { echo "服務沒在跑：先執行 go run . -dev" >&2; exit 2; }

T=$(mktemp -d); trap 'rm -rf "$T"' EXIT
fail=0
ok()  { echo "✓ $1"; }
bad() { echo "✗ $1：$2"; fail=1; }
check() { # 說明 實際 期望（子字串）
  case "$2" in *"$3"*) ok "$1" ;; *) bad "$1" "$2" ;; esac
}
# 檔名一律 ASCII：Windows 的 curl 會用系統字碼頁（Big5）送中文檔名，不代表瀏覽器的行為
post() { local api=$1; shift; curl -s -H "$O" "$@" "$B/api/$api"; }
field() { python -c "import json,sys;print(json.load(sys.stdin).get('$1',''))"; }

# 測試 PDF：RGB 黑字、紅色色塊、0.1 pt 細線、4 pt 小字、貼邊色塊；另一份 5 頁
cat > "$T/a.ps" <<'EOF'
%!PS
<< /PageSize [595 842] >> setpagedevice
0 0 0 setrgbcolor /Helvetica findfont 24 scalefont setfont 72 700 moveto (RGB black text) show
1 0 0 setrgbcolor 72 600 200 50 rectfill
0.5 0.5 0.5 setrgbcolor 0.1 setlinewidth 72 500 moveto 400 500 lineto stroke
/Helvetica findfont 4 scalefont setfont 0 0 0 setrgbcolor 72 450 moveto (tiny text) show
0 0 1 setrgbcolor 0 0 595 30 rectfill
showpage
EOF
printf '%%!PS\n<< /PageSize [595 842] >> setpagedevice /Helvetica findfont 60 scalefont setfont\n1 1 5 { /n exch def 100 400 moveto (Page ) show n 3 string cvs show showpage } for\n' > "$T/five.ps"
"$GS" -q -dNOPAUSE -dBATCH -sDEVICE=pdfwrite -sOutputFile="$T/a.pdf" "$T/a.ps"
"$GS" -q -dNOPAUSE -dBATCH -sDEVICE=pdfwrite -sOutputFile="$T/five.pdf" "$T/five.ps"

echo "── 守衛"
check "Host 不符 → 403" "$(curl -s -o /dev/null -w '%{http_code}' -H 'Host: evil.com' $B/api/ping)" 403
check "localhost → 403（設計如此）" "$(curl -s -o /dev/null -w '%{http_code}' http://localhost:17831/api/ping)" 403
check "POST 沒有 Origin → 403" "$(curl -s -o /dev/null -w '%{http_code}' -F file=@$T/a.pdf $B/api/cmyk)" 403
check "POST 跨站 Origin → 403" "$(curl -s -o /dev/null -w '%{http_code}' -H 'Origin: https://evil.com' -F file=@$T/a.pdf $B/api/cmyk)" 403
check "下載路徑穿越 → 404" "$(curl -s -o /dev/null -w '%{http_code}' --path-as-is "$B/api/file/..%2f..%2fx/y")" 404
check "下載不存在的工作 → 404" "$(curl -s -o /dev/null -w '%{http_code}' $B/api/file/0123456789abcdef/x.pdf)" 404

echo "── 靜態頁面"
for p in / /viewer/ /recompose/ /row-shifter/ /xls2spread/ /diffpdf/ /compress/ /images/ /cmyk/ /protect/; do
  check "GET $p" "$(curl -s -o /dev/null -w '%{http_code}' $B$p)" 200
done
check ".mjs 的 MIME" "$(curl -s -o /dev/null -w '%{content_type}' $B/vendor/pdfjs-4.10.38/pdf.min.mjs)" text/javascript
check "閱讀器沒有註冊 service worker" "$(curl -s $B/viewer/script.js | grep -c 'serviceWorker.register')" 0

echo "── RGB → CMYK"
r=$(post cmyk -F "file=@$T/a.pdf" -F forceK=1)
check "轉換成功" "$(echo "$r" | field ok)" True
rep=$(echo "$r" | python -c "import json,sys;print('\n'.join(json.load(sys.stdin)['report']))")
for w in "單色黑前處理：已將" "未偵測到 RGB" "總墨量：全部未超過" "貼邊物件：第 1 頁" "小字：第 1 頁" "細線：第 1 頁" "均已嵌入"; do
  check "預檢：$w" "$rep" "$w"
done
dl=$(echo "$r" | field download)
curl -s -o "$T/out.pdf" "$B$dl"
check "下載結果是 PDF" "$(head -c 5 "$T/out.pdf")" "%PDF-"
check "非 PDF 被拒" "$(post cmyk -F "file=@$T/a.ps")" "只支援 PDF"

echo "── 壓縮"
r=$(post compress -F "file=@$T/a.pdf" -F level=ebook)
check "壓縮回應 ok" "$(echo "$r" | field ok)" True

echo "── 密碼保護"
r=$(post protect -F "file=@$T/a.pdf" -F mode=encrypt -F userPW=open1 -F ownerPW=own2 -F print=1)
check "加密成功" "$(echo "$r" | field ok)" True
curl -s -o "$T/enc.pdf" "$B$(echo "$r" | field download)"
check "加密檔沒密碼打不開" "$("$GS" -q -dNODISPLAY -dBATCH -dNOPAUSE "$T/enc.pdf" 2>&1)" "password"
check "再加密一次被拒" "$(post protect -F "file=@$T/enc.pdf" -F mode=encrypt -F userPW=x)" "已經有密碼保護"
check "用開啟密碼移除被拒" "$(post protect -F "file=@$T/enc.pdf" -F mode=decrypt -F password=open1)" "密碼不正確"
check "用權限密碼移除成功" "$(post protect -F "file=@$T/enc.pdf" -F mode=decrypt -F password=own2 | field ok)" True
check "沒設密碼被拒" "$(post protect -F "file=@$T/a.pdf" -F mode=encrypt)" "至少設定一種密碼"

echo "── PDF 轉圖片"
check "單頁 PNG" "$(post images -F "file=@$T/five.pdf" -F pages=3 | field download)" "_p003.png"
check "多頁 ZIP" "$(post images -F "file=@$T/five.pdf" -F 'pages=1-2,4-' -F format=jpg | field download)" "4%E5%BC%B5%E5%9C%96.zip"
check "超出範圍" "$(post images -F "file=@$T/five.pdf" -F pages=9)" "不在 1 到 5 頁之間"
check "前後相反" "$(post images -F "file=@$T/five.pdf" -F pages=5-2)" "前後相反"

echo
[ $fail = 0 ] && echo "全部通過。" || echo "有項目未通過。"
exit $fail

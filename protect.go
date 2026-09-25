package main

// 密碼保護：AES-256 加密（開啟密碼、權限密碼與限制），
// 以及移除保護（預設比照 Acrobat 要權限密碼，也可以明確改用開啟密碼）。

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

type fileResult struct {
	OK       bool   `json:"ok"`
	Message  string `json:"message,omitempty"`
	Download string `json:"download,omitempty"`
}

// isEncrypted：有開啟密碼的檔案讀不進來（密碼錯），只有權限密碼的讀得進來但帶加密字典
func isEncrypted(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	ctx, err := api.ReadContext(f, model.NewDefaultConfiguration())
	if err != nil {
		return errors.Is(err, pdfcpu.ErrWrongPassword) || errors.Is(err, pdfcpu.ErrOwnerPasswordRequired)
	}
	return ctx.E != nil
}

// permissions 把表單的四個權限選項轉成 PDF 的權限位元。
// 每一項都有三態（例如列印：允許／只允許低解析度／不允許），對應 Table 22 的位元：
// 舊版位元（Rev2）與新版位元（Rev3）給不同版本的閱讀軟體看，只給其中一個就是「只允許某種」。
// 沒送出或 "0"/"none" 一律當成不允許，跟以前的介面相容。
func permissions(r *http.Request) model.PermissionFlags {
	var p model.PermissionFlags
	add := func(key string, full, partial model.PermissionFlags) {
		switch r.FormValue(key) {
		case "1", "allow":
			p |= full
		case "draft", "a11y", "assemble", "fill":
			p |= partial
		}
	}
	add("print", model.PermissionPrintRev2|model.PermissionPrintRev3, model.PermissionPrintRev2)  // Rev2 = 草稿列印
	add("copy", model.PermissionExtract|model.PermissionExtractRev3, model.PermissionExtractRev3) // Rev3 = 無障礙工具讀取
	add("edit", model.PermissionModify|model.PermissionAssembleRev3, model.PermissionAssembleRev3)
	add("annot", model.PermissionModAnnFillForm|model.PermissionFillRev3, model.PermissionFillRev3)
	return p
}

// aesBits 加密金鑰長度：預設 AES-256，AES-128 給較舊的閱讀軟體
func aesBits(r *http.Request) int {
	if r.FormValue("aes") == "128" {
		return 128
	}
	return 256
}

func handleProtect(w http.ResponseWriter, r *http.Request) {
	res := fileResult{}
	defer func() { w.Header().Set("Content-Type", "application/json"); json.NewEncoder(w).Encode(res) }()

	id, dir, in, name, msg := receivePDF(w, r)
	if msg != "" {
		res.Message = msg
		return
	}
	out := filepath.Join(dir, "out.pdf")
	encrypted := isEncrypted(in)

	switch r.FormValue("mode") {
	case "encrypt":
		if encrypted {
			res.Message = "這個檔案已經有密碼保護，請先移除保護再重新設定。"
			return
		}
		user, owner := r.FormValue("userPW"), r.FormValue("ownerPW")
		if user == "" && owner == "" {
			res.Message = "請至少設定一種密碼。"
			return
		}
		if owner == "" {
			owner = user // 只設開啟密碼：權限密碼與它相同，開得了檔就有全部權限
		}
		conf := model.NewAESConfiguration(user, owner, aesBits(r))
		conf.Permissions = permissions(r)
		if err := api.EncryptFile(in, out, conf); err != nil {
			res.Message = "加密失敗：" + err.Error()
			return
		}
		name += "_加密.pdf"

	case "decrypt":
		if !encrypted {
			res.Message = "這個檔案沒有密碼保護。"
			return
		}
		pw := r.FormValue("password")
		conf := model.NewDefaultConfiguration()
		if r.FormValue("openOnly") == "1" {
			// 使用者明說沒有權限密碼：改用開啟密碼。pdfcpu 的 DECRYPT 只要兩個密碼其中之一，
			// 所以「不需要密碼就能開、只鎖列印」的檔案留空也會過——這正是這個選項要的效果，
			// 也代表權限密碼在這裡沒有被驗證，文件要看清楚再勾。
			conf.UserPW, conf.OwnerPW = pw, pw
			if err := api.DecryptFile(in, out, conf); err != nil {
				// pdfcpu 吃不下的檔案（奇怪的加密、壞掉的 xref）用 gs 重寫一次，
				// 就是「列印成 PDF」那招的自動版。這條路本來就只驗開啟密碼，所以不算放寬。
				if gsPath == "" || !gsDecrypt(in, out, pw) {
					res.Message = "開啟密碼不正確。這個檔案要輸入開啟密碼才打得開；本來就不需要密碼的檔案請直接留空按移除。"
					return
				}
			}
		} else {
			// 預設比照 Acrobat，移除保護要權限密碼。開啟密碼欄填一個不可能猜中的值，
			// 讀得進來就表示權限密碼正確；否則只有權限密碼的檔案不用密碼就能解，限制形同虛設。
			// 這裡刻意不走 gs 後援：gs 兩個密碼都收，等於讓開啟密碼也能移除保護。
			conf.OwnerPW, conf.UserPW = pw, randomID()+randomID()
			if err := api.DecryptFile(in, out, conf); err != nil {
				res.Message = "密碼不正確。移除保護預設要「權限密碼」（設定限制時用的密碼）；沒有權限密碼的話，勾選下面的選項改用開啟密碼。"
				return
			}
		}
		name += "_已移除密碼.pdf"

	default:
		res.Message = "未知的操作。"
		return
	}
	res.OK = true
	res.Download = "/api/file/" + id + "/" + url.PathEscape(name)
}

// gsDecrypt 用 gs 把檔案重寫成沒有加密與權限的 PDF（「列印成 PDF」那招的自動版）。
// gs 只認「開啟密碼或權限密碼其中之一」，所以只用在「使用者明說沒有權限密碼」那條路；
// 預設那條路不能用，不然填開啟密碼也能移除保護，Acrobat 的規矩就白訂了。
func gsDecrypt(in, out, pw string) bool {
	args := []string{"-dSAFER", "-dBATCH", "-dNOPAUSE", "-dQUIET", "-sDEVICE=pdfwrite", "-sOutputFile=" + slash(out)}
	if pw != "" {
		args = append(args, "-sPDFPassword="+pw)
	}
	args = append(args, slash(in))
	msg, err := runGS(convertTimeout, args...)
	if err != nil || !fileExists(out) {
		return false
	}
	// 讀不到檔案時 gs 的 exit code 還是 0，只把錯誤寫到 stderr、順手產出一個沒有頁面的 PDF，
	// 所以一定要自己看訊息（不然會默默交出空白檔），再確認頁數。
	for _, s := range []string{"Cannot decrypt", "Password did not work", "Couldn't initialise file"} {
		if bytes.Contains(msg, []byte(s)) {
			return false
		}
	}
	ctx, err := readPDF(out)
	return err == nil && ctx.PageCount > 0
}

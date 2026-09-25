package main

// 密碼保護：AES-256 加密（開啟密碼、權限密碼與限制），以及用權限密碼移除保護。

import (
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

type protectResult struct {
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

func permissions(r *http.Request) model.PermissionFlags {
	p := model.PermissionsNone
	if r.FormValue("print") == "1" {
		p |= model.PermissionPrintRev2 | model.PermissionPrintRev3
	}
	if r.FormValue("copy") == "1" {
		p |= model.PermissionExtract | model.PermissionExtractRev3
	}
	if r.FormValue("edit") == "1" {
		p |= model.PermissionModify | model.PermissionAssembleRev3
	}
	if r.FormValue("annot") == "1" {
		p |= model.PermissionModAnnFillForm | model.PermissionFillRev3
	}
	return p
}

func handleProtect(w http.ResponseWriter, r *http.Request) {
	res := protectResult{}
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
		conf := model.NewAESConfiguration(user, owner, 256)
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
		// 比照 Acrobat，移除保護要權限密碼。開啟密碼欄填一個不可能猜中的值，
		// 讀得進來就表示權限密碼正確；否則只有權限密碼的檔案不用密碼就能解，限制形同虛設。
		conf := model.NewDefaultConfiguration()
		conf.OwnerPW, conf.UserPW = r.FormValue("password"), randomID()+randomID()
		if err := api.DecryptFile(in, out, conf); err != nil {
			res.Message = "密碼不正確。移除保護需要「權限密碼」（設定限制時用的密碼）；只知道開啟密碼的話，可以開啟閱讀，但不能移除保護。"
			return
		}
		name += "_已移除密碼.pdf"

	default:
		res.Message = "未知的操作。"
		return
	}
	res.OK = true
	res.Download = "/api/file/" + id + "/" + url.PathEscape(name)
}

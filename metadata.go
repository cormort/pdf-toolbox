package main

// 中繼資料：讀出、修改與清除 PDF 的文件資訊。
// 寄件前把作者、公司、產生軟體路徑清掉是這頁的主要用途。要注意中繼資料有兩個地方：
// Info 字典（作者、標題…）與 XMP（/Metadata 串流），清除時兩個都要處理，
// 只清 Info 字典的話 gs 之類產生的檔案還是留著整包 XMP。

import (
	"encoding/json"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// metaStd 是表單上會出現的標準欄位；其他鍵（自訂欄位）只顯示、不讓使用者改，
// 免得表單送來的任意鍵被寫進 PDF。
var metaStd = []string{"Title", "Author", "Subject", "Keywords", "Creator", "Producer", "CreationDate", "ModDate"}

type metadataResult struct {
	fileResult
	Fields    map[string]string `json:"fields,omitempty"`
	Extra     []string          `json:"extra,omitempty"`
	HasXMP    bool              `json:"hasXMP"`
	PageCount int               `json:"pageCount,omitempty"`
}

func handleMetadata(w http.ResponseWriter, r *http.Request) {
	res := metadataResult{}
	defer func() { w.Header().Set("Content-Type", "application/json"); json.NewEncoder(w).Encode(res) }()

	id, dir, in, name, msg := receivePDF(w, r)
	if msg != "" {
		res.Message = msg
		return
	}
	ctx, err := api.ReadContextFile(in)
	if err != nil {
		res.Message = "無法讀取 PDF（有開啟密碼的檔案請先移除保護）：" + err.Error()
		return
	}
	props := readProps(ctx)
	res.PageCount = ctx.PageCount
	res.HasXMP = hasXMP(ctx)
	res.Fields = map[string]string{}
	for _, k := range metaStd {
		res.Fields[k] = props[k]
	}
	for k := range props {
		if !slices.Contains(metaStd, k) {
			res.Extra = append(res.Extra, k)
		}
	}
	sort.Strings(res.Extra)

	switch r.FormValue("action") {
	case "", "read":
		res.OK = true
		return
	case "save":
		// 送空字串 = 清掉該欄位；有值才寫入
		add, drop := map[string]string{}, []string{}
		for _, k := range metaStd {
			if v, ok := r.Form[k]; ok {
				if strings.TrimSpace(v[0]) == "" {
					drop = append(drop, k)
				} else {
					add[k] = v[0]
				}
			}
		}
		if len(add) > 0 {
			if err := pdfcpu.PropertiesAdd(ctx, add); err != nil {
				res.Message = "寫入中繼資料失敗：" + err.Error()
				return
			}
		}
		if len(drop) > 0 {
			pdfcpu.PropertiesRemove(ctx, drop)
		}
		if err := api.WriteContextFile(ctx, filepath.Join(dir, "out.pdf")); err != nil {
			res.Message = "寫出檔案失敗：" + err.Error()
			return
		}
		res.Fields = add
		name += "_已修改中繼資料.pdf"
	case "clear":
		keys := make([]string, 0, len(props))
		for k := range props {
			keys = append(keys, k)
		}
		if len(keys) > 0 {
			pdfcpu.PropertiesRemove(ctx, keys) // 沒有可移除的會回 false，不算失敗
		}
		stripXMP(ctx)
		if err := api.WriteContextFile(ctx, filepath.Join(dir, "out.pdf")); err != nil {
			res.Message = "寫出檔案失敗：" + err.Error()
			return
		}
		name += "_已清除中繼資料.pdf"
	default:
		res.Message = "未知的操作。"
		return
	}
	res.OK = true
	res.Download = "/api/file/" + id + "/" + url.PathEscape(name)
}

// readProps 讀出 Info 字典的鍵值。
// 不能用 pdfcpu 的 api.Properties：它回的是 ctx.Properties，而那個 map 只在 PropertiesAdd
// 寫入時被填，讀檔時一直是空的（v0.15.0 的行為），所以這裡自己解 Info 字典。
func readProps(ctx *model.Context) map[string]string {
	props := map[string]string{}
	if ctx.Info == nil {
		return props
	}
	d, err := ctx.DereferenceDict(*ctx.Info)
	if err != nil || d == nil {
		return props
	}
	for k, v := range d {
		o, err := ctx.Dereference(v)
		if err != nil {
			continue
		}
		var s string
		switch t := o.(type) {
		case types.StringLiteral:
			s, _ = types.StringLiteralToString(t)
		case types.HexLiteral:
			s, _ = types.HexLiteralToString(t)
		default:
			continue
		}
		if s != "" {
			props[k] = s
		}
	}
	return props
}

// hasXMP 看文件與各頁有沒有掛 XMP（/Metadata）
func hasXMP(ctx *model.Context) bool {
	if cat, err := ctx.XRefTable.Catalog(); err == nil {
		if _, ok := cat["Metadata"]; ok {
			return true
		}
	}
	for i := 1; i <= ctx.PageCount; i++ {
		if d, _, _, err := ctx.XRefTable.PageDict(i, false); err == nil {
			if _, ok := d["Metadata"]; ok {
				return true
			}
		}
	}
	return false
}

// stripXMP 把 catalog 與各頁的 /Metadata 拿掉，回傳拆了幾個
func stripXMP(ctx *model.Context) int {
	n := 0
	if cat, err := ctx.XRefTable.Catalog(); err == nil {
		if _, ok := cat["Metadata"]; ok {
			delete(cat, "Metadata")
			n++
		}
	}
	for i := 1; i <= ctx.PageCount; i++ {
		if d, _, _, err := ctx.XRefTable.PageDict(i, false); err == nil {
			if _, ok := d["Metadata"]; ok {
				delete(d, "Metadata")
				n++
			}
		}
	}
	return n
}

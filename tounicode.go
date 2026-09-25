package main

// Ghostscript 重寫 PDF 時，會丟掉 pdf-lib 等工具嵌入的中文字型的 ToUnicode（畫面正常，但文字無法搜尋、複製）。
// Identity-H 的 CID 字型經 gs 後字元編號不變，所以把原檔同名字型的 ToUnicode 接回去即可。

import (
	"os"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

func readPDF(path string) (*model.Context, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return api.ReadAndValidate(f, model.NewDefaultConfiguration())
}

// cidFonts 回傳 Identity-H 的 Type0 字型：去掉子集前綴（ABCDEF+）的名稱 → 字型字典
func cidFonts(xt *model.XRefTable) map[string]types.Dict {
	fonts := map[string]types.Dict{}
	for _, e := range xt.Table {
		if e == nil {
			continue
		}
		d, ok := e.Object.(types.Dict)
		if !ok || d["Subtype"] != types.Name("Type0") || d["Encoding"] != types.Name("Identity-H") {
			continue
		}
		name, _ := d["BaseFont"].(types.Name)
		n := string(name)
		if len(n) > 7 && n[6] == '+' {
			n = n[7:]
		}
		fonts[n] = d
	}
	return fonts
}

// restoreToUnicode 把 src 的 ToUnicode 補到 out 裡缺少的同名字型，回傳補了幾個；有補才改寫 out。
func restoreToUnicode(src, out string) (int, error) {
	sc, err := readPDF(src)
	if err != nil {
		return 0, err
	}
	oc, err := readPDF(out)
	if err != nil {
		return 0, err
	}
	have := cidFonts(sc.XRefTable)
	n := 0
	for name, d := range cidFonts(oc.XRefTable) {
		if _, ok := d["ToUnicode"]; ok || have[name] == nil {
			continue
		}
		sd, _, err := sc.XRefTable.DereferenceStreamDict(have[name]["ToUnicode"])
		if err != nil || sd == nil || sd.Decode() != nil {
			continue
		}
		nsd, err := oc.XRefTable.NewStreamDictForBuf(sd.Content)
		if err != nil || nsd.Encode() != nil {
			continue
		}
		ref, err := oc.XRefTable.IndRefForNewObject(*nsd)
		if err != nil {
			continue
		}
		d["ToUnicode"] = *ref
		n++
	}
	if n == 0 {
		return 0, nil
	}
	return n, api.WriteContextFile(oc, out)
}

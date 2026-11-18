// Package loa creates local, immutable LOA artifacts without executing HTML,
// fetching remote assets, or accepting caller-controlled templates/fonts.
package loa

import (
	"bytes"
	"embed"
	"encoding/base64"
	"fmt"
	"html/template"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"codeberg.org/go-pdf/fpdf"
)

//go:embed assets/NotoSansSC.ttf assets/moedove-logo.png assets/OFL.txt
var assets embed.FS

func validate(data Data) error {
	if data.ID == "" || data.MemberName == "" || data.ExchangeName == "" || data.AllocationID == "" || len(data.Ports) < 1 || len(data.Ports) > 2 {
		return fmt.Errorf("LOA snapshot identity or port list is invalid")
	}
	for _, text := range []string{data.ID, data.ExchangeName, data.MemberName, data.PanelName, data.Location, data.RackName, data.SwitchPort, data.CircuitID, data.AllocationID, data.Reason, strings.Join(data.Ports, " ")} {
		if !utf8.ValidString(text) || len([]rune(text)) > 2000 {
			return fmt.Errorf("LOA labels must be valid UTF-8 and at most2000 characters")
		}
		for _, r := range text {
			if unicode.IsControl(r) && r != '\n' && r != '\t' {
				return fmt.Errorf("LOA text contains a control character")
			}
			if r > 0xFFFF {
				return fmt.Errorf("LOA PDF font encoding supports BMP characters; supplementary characters require a compatible font renderer")
			}
		}
	}
	issued, e := time.Parse(time.RFC3339, data.IssuedAt)
	if e != nil {
		return e
	}
	expires, e := time.Parse(time.RFC3339, data.ExpiresAt)
	if e != nil || !expires.After(issued) {
		return fmt.Errorf("invalid LOA validity period")
	}
	return nil
}
func Generate(data Data) (pdf []byte, html string, err error) {
	if err = validate(data); err != nil {
		return nil, "", err
	}
	font, _ := assets.ReadFile("assets/NotoSansSC.ttf")
	logo, _ := assets.ReadFile("assets/moedove-logo.png")
	doc := fpdf.New("P", "mm", "A4", "")
	doc.SetCompression(false)
	doc.SetCatalogSort(true)
	issued, _ := time.Parse(time.RFC3339, data.IssuedAt)
	doc.SetCreationDate(issued)
	doc.SetModificationDate(issued)
	doc.SetTitle("互联接入授权书 / Letter of Authorization", true)
	doc.SetAuthor(data.ExchangeName, true)
	doc.SetCreator("MoeDove IXP Manager", true)
	doc.SetMargins(20, 20, 20)
	doc.SetAutoPageBreak(true, 20)
	doc.AddUTF8FontFromBytes("NotoSC", "", font)
	doc.AddPage()
	doc.RegisterImageOptionsReader("moedove", fpdf.ImageOptions{ImageType: "PNG"}, bytes.NewReader(logo))
	doc.ImageOptions("moedove", 20, 18, 25, 0, false, fpdf.ImageOptions{ImageType: "PNG"}, 0, "")
	doc.SetXY(52, 22)
	doc.SetFont("NotoSC", "", 18)
	doc.MultiCell(138, 10, "互联接入授权书", "", "L", false)
	doc.SetXY(52, 35)
	doc.SetFont("NotoSC", "", 10)
	doc.MultiCell(138, 6, "Letter of Authorization · "+data.ExchangeName, "", "L", false)
	doc.SetY(56)
	doc.SetDrawColor(217, 223, 231)
	doc.Line(20, 53, 190, 53)
	doc.SetFont("NotoSC", "", 11)
	memberLabel := data.MemberName
	if data.MemberASN != 0 {
		memberLabel = fmt.Sprintf("%s · AS%d", data.MemberName, data.MemberASN)
	}
	fields := [][2]string{{"授权编号 / Reference", data.ID}, {"申请单位 / Member", memberLabel}, {"机房 / Location", data.Location}, {"机柜 / Rack", data.RackName}, {"配线架 / Panel", data.PanelName}, {"端口 / Ports", strings.Join(data.Ports, " / ")}, {"交换机接口 / Switch port", data.SwitchPort}, {"线路编号 / Circuit", data.CircuitID}, {"签发时间 / Issued", data.IssuedAt}, {"有效期至 / Expires", data.ExpiresAt}}
	for _, field := range fields {
		value := field[1]
		if value == "" {
			value = "—"
		}
		doc.SetTextColor(85, 97, 115)
		doc.MultiCell(170, 6, field[0], "", "L", false)
		doc.SetTextColor(20, 28, 42)
		doc.MultiCell(170, 7, value, "", "L", false)
		doc.Ln(2)
	}
	doc.Ln(3)
	doc.MultiCell(170, 7, "本授权允许上述申请单位在所列配线架端口办理本次互联接入。仅适用于该次预留，转让、释放、撤销或有效期届满后失效。", "", "L", false)
	doc.Ln(3)
	doc.MultiCell(170, 7, "This authorization applies only to the specified allocation and expires upon revocation, release, reassignment, or the stated expiry time.", "", "L", false)
	if data.Reason != "" {
		doc.Ln(3)
		doc.MultiCell(170, 7, "说明 / Notes: "+data.Reason, "", "L", false)
	}
	doc.Ln(5)
	doc.SetFont("NotoSC", "", 9)
	doc.MultiCell(170, 6, "预留标识 / Allocation: "+data.AllocationID+"\n请通过管理平台核对当前授权状态。本文件不含数字签名。", "", "L", false)
	var buffer bytes.Buffer
	if err = doc.Output(&buffer); err != nil {
		return nil, "", err
	}
	if buffer.Len() > 4<<20 {
		return nil, "", fmt.Errorf("LOA artifact exceeds4 MiB")
	}
	pdf = buffer.Bytes()
	view := struct {
		Data
		Logo template.URL
	}{data, template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(logo))}
	var page bytes.Buffer
	if err = loaTemplate.Execute(&page, view); err != nil {
		return nil, "", err
	}
	return pdf, page.String(), nil
}

var loaTemplate = template.Must(template.New("loa").Parse(`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>互联接入授权书 {{.ID}}</title><style>@page{size:A4;margin:20mm}body{font:15px/1.6 system-ui,"Noto Sans SC",sans-serif;color:#172033;max-width:780px;margin:40px auto;padding:0 20px}header{display:flex;gap:24px;align-items:center;border-bottom:1px solid #ddd;padding-bottom:20px}img{width:95px}h1{font-size:25px;margin:0}header p{color:#667;margin:4px 0}dl{display:grid;grid-template-columns:180px 1fr;gap:10px;margin:28px 0}dt{color:#667}dd{margin:0;overflow-wrap:anywhere}footer{border-top:1px solid #ddd;margin-top:28px;padding-top:14px;font-size:12px;color:#667}.note{white-space:pre-wrap}</style></head><body><header><img src="{{.Logo}}" alt="MoeDove"><div><h1>互联接入授权书</h1><p>Letter of Authorization · {{.ExchangeName}}</p></div></header><dl><dt>授权编号</dt><dd>{{.ID}}</dd><dt>申请单位</dt><dd>{{.MemberName}}{{if .MemberASN}} · AS{{.MemberASN}}{{end}}</dd><dt>机房 / 机柜</dt><dd>{{.Location}} / {{.RackName}}</dd><dt>配线架</dt><dd>{{.PanelName}}</dd><dt>端口</dt><dd>{{range .Ports}}{{.}}&nbsp; {{end}}</dd><dt>交换机接口</dt><dd>{{.SwitchPort}}</dd><dt>线路编号</dt><dd>{{.CircuitID}}</dd><dt>签发时间</dt><dd>{{.IssuedAt}}</dd><dt>有效期至</dt><dd>{{.ExpiresAt}}</dd></dl><p>本授权允许上述申请单位在所列配线架端口办理本次互联接入。仅适用于该次预留，转让、释放、撤销或有效期届满后失效。</p><p>This authorization applies only to the specified allocation and expires upon revocation, release, reassignment, or the stated expiry time.</p><p class="note">{{.Reason}}</p><footer>预留标识：{{.AllocationID}}<br>请通过管理平台核对当前授权状态。本文件不含数字签名。</footer></body></html>`))

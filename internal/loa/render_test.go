package loa

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
	"unicode/utf16"
)

func example() Data {
	return Data{ID: "loa-test", ExchangeName: "萌鸽互联网交换中心", MemberName: "测试网络有限公司", MemberASN: 64501, PanelName: "上海配线架", Location: "上海 数据中心", RackName: "机柜A", Ports: []string{"端口1", "端口2"}, SwitchPort: "sw1 / xe-1", CircuitID: "线路123", AllocationID: "allocation-test", IssuedAt: "2026-11-06T00:00:00Z", ExpiresAt: "2026-11-07T00:00:00Z", Reason: "双工光纤交叉连接"}
}
func TestChinesePDFAndSafeHTML(t *testing.T) {
	data := example()
	pdf, html, err := Generate(data)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) || !bytes.Contains(pdf, []byte("/FontFile2")) || !bytes.Contains(pdf, []byte("/ToUnicode")) {
		t.Fatal("not a real embedded-font accessible PDF")
	}
	var text bytes.Buffer
	for _, unit := range utf16.Encode([]rune("测试网络有限公司")) {
		_ = binary.Write(&text, binary.BigEndian, unit)
	}
	if !bytes.Contains(pdf, text.Bytes()) {
		t.Fatal("Chinese text is absent from PDF content stream")
	}
	if !strings.Contains(html, "测试网络有限公司") || !strings.Contains(html, "data:image/png;base64,") {
		t.Fatal("print artifact lost snapshot/embedded branding")
	}
	data.MemberName = "<script>alert(1)</script>"
	_, html, err = Generate(data)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(html, "<script>") {
		t.Fatal("HTML template accepted script injection")
	}
}

func TestNonPeeringOrganizationDoesNotFabricateASZero(t *testing.T) {
	d := example()
	d.MemberASN = 0
	pdf, html, err := Generate(d)
	if err != nil {
		t.Fatal(err)
	}
	if len(pdf) == 0 || strings.Contains(html, " · AS0") {
		t.Fatal("missing ASN rendered as a real AS0")
	}
}

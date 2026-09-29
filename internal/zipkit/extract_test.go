package zipkit

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestExtractPlainAndMarkdown(t *testing.T) {
	text, err := ExtractRequirement("notes.txt", []byte("Add a login screen.\n"))
	if err != nil {
		t.Fatal(err)
	}
	if text != "Add a login screen." {
		t.Fatalf("text = %q", text)
	}
	md, err := ExtractRequirement("spec.md", []byte("# Portal\n\nShip invoices."))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(md, "Ship invoices.") {
		t.Fatalf("markdown = %q", md)
	}
}

func TestExtractDocx(t *testing.T) {
	text, err := ExtractRequirement("billing.docx", docxWithParagraph("Ship a billing portal for invoices."))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "Ship a billing portal for invoices.") {
		t.Fatalf("docx text = %q", text)
	}
}

func TestExtractPDF(t *testing.T) {
	text, err := ExtractRequirement("portal.pdf", minimalPDF("Build a customer portal"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "Build a customer portal") {
		t.Fatalf("pdf text = %q", text)
	}
}

func TestExtractEmptyPDF(t *testing.T) {
	_, err := ExtractRequirement("scan.pdf", minimalPDF(""))
	if err == nil || !strings.Contains(err.Error(), "no selectable text") {
		t.Fatalf("err = %v", err)
	}
}

func TestExtractSimpleDoc(t *testing.T) {
	word := make([]byte, 0x200)
	binary.LittleEndian.PutUint16(word[0:2], 0xA5EC)
	binary.LittleEndian.PutUint16(word[0x0A:0x0C], 0x1000)
	binary.LittleEndian.PutUint32(word[0x18:0x1C], 0x200)
	payload := utf16Bytes("Ship a billing portal")
	binary.LittleEndian.PutUint32(word[0x4C:0x50], uint32(len(payload)/2))
	word = append(word, payload...)
	got, err := textFromWord(word, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "Ship a billing portal") {
		t.Fatalf("doc text = %q", got)
	}
}

func TestExtractComplexDoc(t *testing.T) {
	payload := utf16Bytes("Ship a billing portal")
	word := make([]byte, 100)
	binary.LittleEndian.PutUint16(word[0:2], 0xA5EC)
	binary.LittleEndian.PutUint16(word[0x0A:0x0C], 0x0004)
	word = append(word, payload...)
	fc := 100
	chars := len(payload) / 2
	table := pieceTable(fc, chars)
	got, err := textFromWord(word, table, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "Ship a billing portal") {
		t.Fatalf("complex doc text = %q", got)
	}
}

func TestToMarkdownPrefersPastedText(t *testing.T) {
	file := []byte("Add login.\n")
	got, err := ToMarkdown("Portal", "notes.txt", file, "Pasted wins")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "Pasted wins") || strings.Contains(got, "Add login") {
		t.Fatalf("markdown = %q", got)
	}
	fromFile, err := ToMarkdown("Portal", "notes.txt", file, "  ")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fromFile, "Add login") || strings.Contains(fromFile, "could not extract") {
		t.Fatalf("file markdown = %q", fromFile)
	}
}

func docxWithParagraph(text string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("word/document.xml")
	if err != nil {
		panic(err)
	}
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:body><w:p><w:r><w:t>%s</w:t></w:r></w:p></w:body>
</w:document>`, text)
	if err := zw.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func minimalPDF(text string) []byte {
	stream := "BT /F1 12 Tf 50 700 Td (" + text + ") Tj ET"
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	var b strings.Builder
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs)+1)
	for i, obj := range objs {
		offsets[i+1] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n", len(objs)+1)
	b.WriteString("0000000000 65535 f \n")
	for i := 1; i <= len(objs); i++ {
		fmt.Fprintf(&b, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	return []byte(b.String())
}

func utf16Bytes(text string) []byte {
	units := utf16.Encode([]rune(text))
	out := make([]byte, len(units)*2)
	for i, unit := range units {
		binary.LittleEndian.PutUint16(out[i*2:], unit)
	}
	return out
}

func pieceTable(fc, chars int) []byte {
	n := 1
	lcb := 12*n + 4
	buf := make([]byte, 5+lcb)
	buf[0] = 0x02
	binary.LittleEndian.PutUint32(buf[1:5], uint32(lcb))
	plc := buf[5:]
	binary.LittleEndian.PutUint32(plc[0:4], 0)
	binary.LittleEndian.PutUint32(plc[4:8], uint32(chars))
	binary.LittleEndian.PutUint32(plc[10:14], uint32(fc))
	return buf
}

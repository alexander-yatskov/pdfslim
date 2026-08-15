package analyze

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

func TestBytesUsesPDFObjectGraph(t *testing.T) {
	content := "BT (/Encrypt /Type /Font %%EOF) Tj ET"
	pdf := testPDF([]string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] /Resources << /Font << /F1 5 0 R >> /XObject << /Im1 6 0 R >> >> /Contents 4 0 R >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
		"<< /Type /XObject /Subtype /Image /Width 1 /Height 1 /ColorSpace /DeviceGray /BitsPerComponent 8 /Length 1 >>\nstream\nx\nendstream",
	})

	r, err := read(bytes.NewReader(pdf))
	if err != nil {
		t.Fatal(err)
	}
	if r.Version != "1.7" || r.Pages != 1 || r.Images != 1 || r.Fonts != 1 || r.Streams != 2 {
		t.Fatalf("unexpected report: %+v", r)
	}
	if r.StreamBytes != int64(len(content)+1) || r.Revisions != 1 || r.Encrypted || r.Signed {
		t.Fatalf("unexpected stream or security report: %+v", r)
	}
	if r.ImageBytes != 1 || r.OtherBytes != int64(len(content)) {
		t.Fatalf("unexpected size breakdown: %+v", r)
	}
}

func TestRejectsNonPDF(t *testing.T) {
	if _, err := read(bytes.NewReader([]byte("hello"))); err == nil {
		t.Fatal("expected an error")
	}
}

func testPDF(objects []string) []byte {
	var b strings.Builder
	b.WriteString("%PDF-1.7\n")
	offsets := make([]int, len(objects)+1)
	for i, object := range objects {
		offsets[i+1] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, object)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n", len(objects)+1)
	b.WriteString("0000000000 65535 f \n")
	for i := 1; i <= len(objects); i++ {
		fmt.Fprintf(&b, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return []byte(b.String())
}

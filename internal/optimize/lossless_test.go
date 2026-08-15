package optimize

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

func TestLosslessParsesAndWritesPDF(t *testing.T) {
	dir := t.TempDir()
	in, out := filepath.Join(dir, "in.pdf"), filepath.Join(dir, "out.pdf")
	if err := os.WriteFile(in, minimalPDF(), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := OptimizeWithOptions(in, out, ProfileLossless, Options{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(got), "%PDF-") || !strings.Contains(string(got), "%%EOF") {
		t.Fatalf("invalid output PDF: %q", got)
	}
	if r.After > r.Before || r.Saved < 0 {
		t.Fatalf("optimizer increased size: %+v", r)
	}
}

func TestLosslessRejectsSamePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "in.pdf")
	if _, err := OptimizeWithOptions(path, path, ProfileLossless, Options{}); err == nil {
		t.Fatal("expected an error")
	}
}

func TestLosslessRejectsHardLinkToInput(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "in.pdf")
	output := filepath.Join(dir, "out.pdf")
	if err := os.WriteFile(input, minimalPDF(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(input, output); err != nil {
		t.Fatal(err)
	}

	if _, err := OptimizeWithOptions(input, output, ProfileLossless, Options{}); err == nil {
		t.Fatal("expected an error")
	}
}

func TestLosslessRejectsSymbolicLinkToInput(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "in.pdf")
	output := filepath.Join(dir, "out.pdf")
	if err := os.WriteFile(input, minimalPDF(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(input, output); err != nil {
		t.Fatal(err)
	}

	if _, err := OptimizeWithOptions(input, output, ProfileLossless, Options{}); err == nil {
		t.Fatal("expected an error")
	}
}

func TestOptimizeRemovesTemporaryOutputAfterCancellation(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "in.pdf")
	output := filepath.Join(dir, "out.pdf")
	if err := os.WriteFile(input, minimalPDF(), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx := &stagedCancelContext{cancelAt: 2}
	if _, err := OptimizeWithOptions(input, output, ProfileLossless, Options{Context: ctx}); err == nil {
		t.Fatal("expected cancellation error")
	}
	matches, err := filepath.Glob(filepath.Join(dir, ".pdfslim-*.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary output remains after cancellation: %v", matches)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("final output was installed after cancellation: %v", err)
	}
}

type stagedCancelContext struct {
	calls    int
	cancelAt int
}

func (*stagedCancelContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (*stagedCancelContext) Done() <-chan struct{}       { return nil }
func (*stagedCancelContext) Value(any) any               { return nil }
func (c *stagedCancelContext) Err() error {
	c.calls++
	if c.calls >= c.cancelAt {
		return context.Canceled
	}
	return nil
}

func TestDeduplicateFontFiles(t *testing.T) {
	length := int64(4)
	fontData := func() types.StreamDict {
		return types.StreamDict{Dict: types.Dict{"Length": types.Integer(4)}, StreamLength: &length, Raw: []byte("font")}
	}
	ref1 := *types.NewIndirectRef(3, 0)
	ref2 := *types.NewIndirectRef(4, 0)
	xref := &model.XRefTable{Table: map[int]*model.XRefTableEntry{
		1: model.NewXRefTableEntryGen0(types.Dict{"Type": types.Name("FontDescriptor"), "FontFile2": ref1}),
		2: model.NewXRefTableEntryGen0(types.Dict{"Type": types.Name("FontDescriptor"), "FontFile2": ref2}),
		3: model.NewXRefTableEntryGen0(fontData()),
		4: model.NewXRefTableEntryGen0(fontData()),
	}}
	ctx := &model.Context{
		XRefTable: xref,
		Optimize:  &model.OptimizationContext{DuplicateFontObjs: types.IntSet{}},
	}

	if err := deduplicateFontFiles(ctx); err != nil {
		t.Fatal(err)
	}
	d := xref.Table[2].Object.(types.Dict)
	got := d.IndirectRefEntry("FontFile2")
	if got == nil || got.ObjectNumber.Value() != 3 {
		t.Fatalf("font stream was not deduplicated: %v", got)
	}
	if !ctx.Optimize.DuplicateFontObjs[4] {
		t.Fatal("duplicate font stream was not marked for removal")
	}
}

func TestReferencedMaskObjects(t *testing.T) {
	maskRef := *types.NewIndirectRef(7, 0)
	softMaskRef := *types.NewIndirectRef(8, 0)
	groupRef := *types.NewIndirectRef(9, 0)
	groupImageRef := *types.NewIndirectRef(10, 0)
	ctx := &model.Context{XRefTable: &model.XRefTable{Table: map[int]*model.XRefTableEntry{
		1: model.NewXRefTableEntryGen0(types.StreamDict{Dict: types.Dict{"Mask": maskRef}}),
		2: model.NewXRefTableEntryGen0(types.Dict{"SMask": softMaskRef}),
		3: model.NewXRefTableEntryGen0(types.Dict{"SMask": types.Dict{"G": groupRef}}),
		9: model.NewXRefTableEntryGen0(types.StreamDict{Dict: types.Dict{
			"Subtype":   types.Name("Form"),
			"Resources": types.Dict{"XObject": types.Dict{"Im": groupImageRef}},
		}}),
		10: model.NewXRefTableEntryGen0(types.StreamDict{Dict: types.Dict{"Subtype": types.Name("Image")}}),
	}}}
	got := referencedMaskObjects(ctx)
	if !got[7] || !got[8] || !got[10] || len(got) != 3 {
		t.Fatalf("mask objects = %v, want 7, 8, and 10", got)
	}
}

func TestParseProfile(t *testing.T) {
	for _, name := range []string{"lossless", "balanced", "screen", "print", "ebook", "aggressive"} {
		if _, err := ParseProfile(name); err != nil {
			t.Fatalf("ParseProfile(%q): %v", name, err)
		}
	}
	if _, err := ParseProfile("tiny"); err == nil {
		t.Fatal("expected unsupported profile error")
	}
}

func TestDownsample(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 1000, 500))
	got := downsample(src, 400, 400)
	if got.Bounds().Dx() != 400 || got.Bounds().Dy() != 200 {
		t.Fatalf("unexpected dimensions: %v", got.Bounds())
	}
}

func TestGrayImageBytes(t *testing.T) {
	img := image.NewGray(image.Rect(0, 0, 2, 2))
	img.Pix = []byte{0, 64, 128, 255}
	if got, want := grayImageBytes(img, img.Bounds()), img.Pix; !bytes.Equal(got, want) {
		t.Fatalf("gray bytes = %v, want %v", got, want)
	}
}

func TestOptimizeTransparentImageSkipsBrokenSMaskReference(t *testing.T) {
	length := int64(10)
	missingMask := *types.NewIndirectRef(99, 0)
	sd := &types.StreamDict{Dict: types.Dict{
		"Width": types.Integer(100), "Height": types.Integer(100), "SMask": missingMask,
	}, StreamLength: &length}
	ctx := &model.Context{XRefTable: &model.XRefTable{Table: map[int]*model.XRefTableEntry{}}}
	imageObject := &model.ImageObject{ImageDict: sd, ResourceNames: map[int]string{}}
	settings := imageSettings{memoryBudget: NewMemoryBudget(0)}
	if err := optimizeTransparentImage(ctx, 1, imageObject, nil, nil, settings); err != nil {
		t.Fatalf("broken SMask aborted optimization: %v", err)
	}
}

func TestTransparentImageCandidateDoesNotChangeXRefTable(t *testing.T) {
	size := 1
	ctx := &model.Context{XRefTable: &model.XRefTable{
		Table: map[int]*model.XRefTableEntry{0: model.NewFreeHeadXRefTableEntry()},
		Size:  &size,
	}}
	colorImage := image.NewRGBA(image.Rect(0, 0, 64, 64))
	maskImage := image.NewGray(image.Rect(0, 0, 64, 64))
	before := len(ctx.Table)

	imageStream, maskStream, _, err := transparentImageStreams(ctx.XRefTable, colorImage, maskImage, 65)
	if err != nil {
		t.Fatal(err)
	}
	if len(ctx.Table) != before {
		t.Fatalf("candidate generation changed XRef table size from %d to %d", before, len(ctx.Table))
	}
	maskRef, err := ctx.IndRefForNewObject(*maskStream)
	if err != nil {
		t.Fatal(err)
	}
	imageStream.Insert("SMask", *maskRef)
	if got := imageStream.IndirectRefEntry("SMask"); got == nil || got.ObjectNumber.Value() != maskRef.ObjectNumber.Value() {
		t.Fatal("accepted candidate has no valid soft-mask reference")
	}
	if len(ctx.Table) != before+1 {
		t.Fatalf("accepted candidate added %d objects, want 1", len(ctx.Table)-before)
	}
}

func TestDecodeStreamImageReturnsErrorForTruncatedCMYK(t *testing.T) {
	sd := &types.StreamDict{Dict: types.Dict{
		"Width":            types.Integer(1),
		"Height":           types.Integer(1),
		"BitsPerComponent": types.Integer(8),
		"ColorSpace":       types.Name(model.DeviceCMYKCS),
	}, Content: []byte{}}
	ctx := &model.Context{XRefTable: &model.XRefTable{Table: map[int]*model.XRefTableEntry{}}}

	if _, err := decodeStreamImage(ctx, sd, 7); err == nil {
		t.Fatal("expected a truncated CMYK decode error")
	}
}

func minimalPDF() []byte {
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] /Resources << >> /Contents 4 0 R >>",
		"<< /Length 0 >>\nstream\n\nendstream",
	}
	var b strings.Builder
	b.WriteString("%PDF-1.4\n")
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

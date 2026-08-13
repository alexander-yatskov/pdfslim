package optimize

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/filter"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"

	"pdfslim/internal/pdfconfig"
)

func TestAdaptiveImageChoosesIndexedFlateForFlatGraphics(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 256, 256))
	for y := 0; y < 256; y++ {
		for x := 0; x < 256; x++ {
			c := color.RGBA{R: 240, G: 240, B: 240, A: 255}
			if x > 48 && x < 208 && y > 96 && y < 160 {
				c = color.RGBA{R: 20, G: 60, B: 180, A: 255}
			}
			img.SetRGBA(x, y, c)
		}
	}

	tempDir := t.TempDir()
	sd, err := adaptiveImageStream(img, 68, tempDir)
	if err != nil {
		t.Fatal(err)
	}
	if !sd.HasSoleFilterNamed(filter.Flate) {
		t.Fatalf("filter = %v, want FlateDecode", sd.FilterPipeline)
	}
	colorSpace := sd.ArrayEntry("ColorSpace")
	if len(colorSpace) != 4 || colorSpace[0] != types.Name("Indexed") {
		t.Fatalf("ColorSpace = %v, want Indexed", colorSpace)
	}
	if err := sd.Decode(); err != nil {
		t.Fatal(err)
	}
	r, format, err := pdfcpu.RenderImage(&model.XRefTable{}, sd, false, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	decoded, _, err := image.Decode(r)
	if err != nil {
		t.Fatalf("decode rendered %s candidate: %v", format, err)
	}
	if got := color.NRGBAModel.Convert(decoded.At(100, 120)).(color.NRGBA); got.B < 150 || got.R > 50 {
		t.Fatalf("indexed color changed: %+v", got)
	}
	assertEmptyDirectory(t, tempDir)
}

func TestIndexedPixelsRejectsMoreThan256Colors(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 257, 1))
	for x := 0; x < 257; x++ {
		img.SetRGBA(x, 0, color.RGBA{R: uint8(x), G: uint8(x >> 8), B: uint8(x * 31), A: 255})
	}
	if _, _, ok := indexedPalette(img); ok {
		t.Fatal("accepted image with more than 256 colors")
	}
}

func TestIndexedCandidateUsesPackedBits(t *testing.T) {
	for _, tc := range []struct{ colors, bpc int }{{2, 1}, {16, 4}, {17, 8}} {
		img := image.NewNRGBA(image.Rect(0, 0, 19, 7))
		for y := 0; y < 7; y++ {
			for x := 0; x < 19; x++ {
				v := byte((x + y*19) % tc.colors)
				img.SetNRGBA(x, y, color.NRGBA{R: v, G: 255 - v, B: v * 11, A: 255})
			}
		}
		candidate, ok, err := writeIndexedCandidate(img, t.TempDir())
		if err != nil || !ok {
			t.Fatalf("colors=%d ok=%v err=%v", tc.colors, ok, err)
		}
		if candidate.bpc != tc.bpc {
			t.Fatalf("colors=%d bpc=%d, want %d", tc.colors, candidate.bpc, tc.bpc)
		}
		data, err := os.ReadFile(candidate.path)
		if err != nil {
			t.Fatal(err)
		}
		sd := encodedImageStream(data, 19, 7, candidate.colorSpace, candidate.bpc, candidate.filter, nil)
		if err := sd.Decode(); err != nil {
			t.Fatal(err)
		}
		r, _, err := pdfcpu.RenderImage(&model.XRefTable{}, sd, false, "", 1)
		if err != nil {
			t.Fatal(err)
		}
		decoded, _, err := image.Decode(r)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range []image.Point{{0, 0}, {18, 0}, {3, 6}, {18, 6}} {
			want := color.NRGBAModel.Convert(img.At(p.X, p.Y)).(color.NRGBA)
			got := color.NRGBAModel.Convert(decoded.At(p.X, p.Y)).(color.NRGBA)
			if got.R != want.R || got.G != want.G || got.B != want.B {
				t.Fatalf("colors=%d pixel=%v got=%v want=%v", tc.colors, p, got, want)
			}
		}
	}
}

func TestInterpolateOnlyForSmallImages(t *testing.T) {
	small := encodedImageStream([]byte{0}, 800, 800, types.Name(model.DeviceGrayCS), 8, filter.Flate, nil)
	if small.BooleanEntry("Interpolate") == nil {
		t.Fatal("small image has no Interpolate hint")
	}
	wide := encodedImageStream([]byte{0}, 5000, 800, types.Name(model.DeviceGrayCS), 8, filter.Flate, nil)
	if wide.BooleanEntry("Interpolate") != nil {
		t.Fatal("wide image has Interpolate hint")
	}
}

func TestAnalyzeImageConcreteTypes(t *testing.T) {
	images := []image.Image{
		image.NewGray(image.Rect(0, 0, 3, 2)),
		image.NewRGBA(image.Rect(0, 0, 3, 2)),
		image.NewNRGBA(image.Rect(0, 0, 3, 2)),
		&image.YCbCr{Y: []byte{0, 255, 0, 255, 0, 255}, Cb: []byte{128, 128, 128, 128, 128, 128}, Cr: []byte{128, 128, 128, 128, 128, 128}, YStride: 3, CStride: 3, SubsampleRatio: image.YCbCrSubsampleRatio444, Rect: image.Rect(0, 0, 3, 2)},
	}
	for _, img := range images {
		a := analyzeImage(img)
		if !a.gray || !a.bilevel || !a.indexed {
			t.Fatalf("%T analysis=%+v", img, a)
		}
	}
}

func TestVisitImageRowHandlesSubImageStrideAndOrigin(t *testing.T) {
	parent := image.NewNRGBA(image.Rect(0, 0, 10, 10))
	parent.SetNRGBA(4, 5, color.NRGBA{R: 11, G: 22, B: 33, A: 255})
	sub := parent.SubImage(image.Rect(4, 5, 7, 8)).(*image.NRGBA)
	var got [3]byte
	visitImageRow(sub, 5, func(x int, r, g, b byte) {
		if x == 4 {
			got = [3]byte{r, g, b}
		}
	})
	if got != [3]byte{11, 22, 33} {
		t.Fatalf("pixel=%v", got)
	}
}

func TestCCITTGroup4CandidateForBinaryGraphics(t *testing.T) {
	img := image.NewGray(image.Rect(0, 0, 1728, 2200))
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	for y := 0; y < 2200; y++ {
		for run := 0; run < 24; run++ {
			left := 20 + run*70 + (y/3+run*13)%31
			for x := left; x < left+18+(run%9); x++ {
				img.SetGray(x, y, color.Gray{Y: 0})
			}
		}
	}
	tempDir := t.TempDir()
	candidate, ok, err := writeCCITTG4Candidate(img, tempDir)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("binary image was rejected")
	}
	data, err := os.ReadFile(candidate.path)
	if err != nil {
		t.Fatal(err)
	}
	sd := encodedImageStream(data, 1728, 2200, candidate.colorSpace, candidate.bpc, candidate.filter, candidate.decodeParms)
	if !sd.HasSoleFilterNamed(filter.CCITTFax) {
		t.Fatalf("filter = %v, want CCITTFaxDecode", sd.FilterPipeline)
	}
	if got := sd.IntEntry("BitsPerComponent"); got == nil || *got != 1 {
		t.Fatalf("BitsPerComponent = %v, want 1", got)
	}
	dp := sd.DictEntry("DecodeParms")
	if dp == nil || dp.IntEntry("K") == nil || *dp.IntEntry("K") != -1 {
		t.Fatalf("DecodeParms = %v, want Group 4 K=-1", dp)
	}
}

func TestAdaptiveImageDoesNotReencodeExistingOneBitObjectAsCCITT(t *testing.T) {
	img := binaryTestImage(257, 129)
	sd, err := adaptiveImageStreamWithCCITT(img, 68, t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	if sd.HasSoleFilterNamed(filter.CCITTFax) {
		t.Fatal("existing 1-bit source was re-encoded as CCITT")
	}
}

func TestSafeCCITTSource(t *testing.T) {
	sd := &types.StreamDict{Dict: types.Dict{
		"BitsPerComponent": types.Integer(8),
		"ColorSpace":       types.Name(model.DeviceGrayCS),
		"Filter":           types.Name(filter.Flate),
	}, FilterPipeline: []types.PDFFilter{{Name: filter.Flate}}}
	if !safeCCITTSource(sd) {
		t.Fatal("safe 8-bit Gray Flate source was rejected")
	}
	sd.Insert("Decode", types.NewNumberArray(1, 0))
	if safeCCITTSource(sd) {
		t.Fatal("source with Decode was accepted")
	}
}

func TestCCITTGroup4RejectsNonBinaryImage(t *testing.T) {
	img := image.NewGray(image.Rect(0, 0, 8, 8))
	for i := range img.Pix {
		img.Pix[i] = 127
	}
	if _, ok, err := writeCCITTG4Candidate(img, t.TempDir()); err != nil || ok {
		t.Fatalf("ok=%v err=%v, want clean rejection", ok, err)
	}
}

func TestCCITTGroup4RendersLosslessly(t *testing.T) {
	img := binaryTestImage(257, 129)
	candidate, ok, err := writeCCITTG4Candidate(img, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("binary image was rejected")
	}
	data, err := os.ReadFile(candidate.path)
	if err != nil {
		t.Fatal(err)
	}
	sd := encodedImageStream(data, 257, 129, candidate.colorSpace, candidate.bpc, candidate.filter, candidate.decodeParms)
	if err := sd.Decode(); err != nil {
		t.Fatal(err)
	}
	r, _, err := pdfcpu.RenderImage(&model.XRefTable{}, sd, false, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	decoded, _, err := image.Decode(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []image.Point{{0, 0}, {31, 16}, {128, 64}, {256, 128}} {
		want := color.GrayModel.Convert(img.At(p.X, p.Y)).(color.Gray).Y
		got := color.GrayModel.Convert(decoded.At(p.X, p.Y)).(color.Gray).Y
		if got != want {
			t.Fatalf("pixel %v = %d, want %d", p, got, want)
		}
	}
}

func binaryTestImage(width, height int) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, width, height))
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	var state uint32 = 1
	for top := 12; top+10 < height; top += 22 {
		for left := 16; left+8 < width-16; left += 11 {
			state = state*1664525 + 1013904223
			glyph := state
			for y := 0; y < 10; y++ {
				for x := 0; x < 7; x++ {
					if (glyph>>uint((x+y*3)%24))&1 != 0 && (x == 1 || x == 5 || y == 1 || y == 8) {
						img.SetGray(left+x, top+y, color.Gray{Y: 0})
					}
				}
			}
		}
	}
	return img
}

func TestIndexedFlateSurvivesPDFWriteAndRead(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.pdf")
	output := filepath.Join(dir, "output.pdf")
	img := image.NewRGBA(image.Rect(0, 0, 256, 256))
	for y := 0; y < 256; y++ {
		for x := 0; x < 256; x++ {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x / 64 * 80), G: uint8(y / 64 * 80), B: 120, A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, img); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := api.ImportImages(nil, f, []io.Reader{bytes.NewReader(encoded.Bytes())}, nil, pdfconfig.New()); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	sourceFile, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := api.ReadValidateAndOptimize(sourceFile, pdfconfig.New())
	_ = sourceFile.Close()
	if err != nil {
		t.Fatal(err)
	}
	for objNr, object := range ctx.Optimize.ImageObjects {
		replacement, err := adaptiveImageStream(img, 68, dir)
		if err != nil {
			t.Fatal(err)
		}
		ctx.Table[objNr].Object = *replacement
		object.ImageDict = replacement
	}
	written, err := os.Create(output)
	if err != nil {
		t.Fatal(err)
	}
	if err := api.WriteContext(ctx, written); err != nil {
		_ = written.Close()
		t.Fatal(err)
	}
	if err := written.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := os.Open(output)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	ctx, err = api.ReadValidateAndOptimize(out, pdfconfig.New())
	if err != nil {
		t.Fatal(err)
	}
	var rendered image.Image
	for objNr, object := range ctx.Optimize.ImageObjects {
		sd := object.ImageDict
		if a := sd.ArrayEntry("ColorSpace"); len(a) == 4 && a[0] == types.Name("Indexed") {
			if len(sd.Content) == 0 {
				if err := sd.Decode(); err != nil {
					t.Fatal(err)
				}
			}
			r, _, err := pdfcpu.RenderImage(ctx.XRefTable, sd, false, "", objNr)
			if err != nil {
				t.Fatal(err)
			}
			rendered, _, err = image.Decode(r)
			if err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	if rendered == nil {
		t.Fatal("optimized PDF has no Indexed image")
	}
	if got := color.NRGBAModel.Convert(rendered.At(100, 100)).(color.NRGBA); got.B < 100 {
		t.Fatalf("indexed color changed after PDF write: %+v", got)
	}
}

func TestAdaptiveImageUsesGrayColorSpace(t *testing.T) {
	img := image.NewGray(image.Rect(0, 0, 32, 32))
	for i := range img.Pix {
		img.Pix[i] = uint8(i)
	}

	tempDir := t.TempDir()
	sd, err := adaptiveImageStream(img, 68, tempDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := sd.NameEntry("ColorSpace"); got == nil || *got != model.DeviceGrayCS {
		t.Fatalf("ColorSpace = %v, want %s", got, model.DeviceGrayCS)
	}
	assertEmptyDirectory(t, tempDir)
}

func TestAdaptiveImageChoosesJPEGForNoisyColorImage(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 256, 256))
	var state uint32 = 1
	for y := 0; y < 256; y++ {
		for x := 0; x < 256; x++ {
			state = state*1664525 + 1013904223
			img.SetRGBA(x, y, color.RGBA{R: uint8(state >> 24), G: uint8(state >> 16), B: uint8(state >> 8), A: 255})
		}
	}

	tempDir := t.TempDir()
	sd, err := adaptiveImageStream(img, 68, tempDir)
	if err != nil {
		t.Fatal(err)
	}
	if !sd.HasSoleFilterNamed(filter.DCT) {
		t.Fatalf("filter = %v, want DCTDecode", sd.FilterPipeline)
	}
	assertEmptyDirectory(t, tempDir)
}

func assertEmptyDirectory(t *testing.T, path string) {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("temporary candidates were not removed: %v", entries)
	}
}

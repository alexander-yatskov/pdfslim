package optimize

import (
	"bytes"
	"compress/zlib"
	"image"
	"image/color"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/filter"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

func TestRenderStreamImageDecodesFlateRGBWithPNGPredictor(t *testing.T) {
	// Two PNG scanlines. Each starts with predictor 0 (None).
	rawPixels := []byte{0, 255, 0, 0, 0, 0, 255, 0}
	sd := flateImageStream(t, rawPixels, 1, 2, 3, model.DeviceRGBCS, types.Dict{
		"Predictor": types.Integer(10),
		"Colors":    types.Integer(3),
		"Columns":   types.Integer(1),
	})

	img, err := renderStreamImage(emptyImageContext(), sd, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := color.NRGBAModel.Convert(img.At(0, 0)).(color.NRGBA); got != (color.NRGBA{R: 255, A: 255}) {
		t.Fatalf("first pixel = %#v", got)
	}
	if got := color.NRGBAModel.Convert(img.At(0, 1)).(color.NRGBA); got != (color.NRGBA{G: 255, A: 255}) {
		t.Fatalf("second pixel = %#v", got)
	}
}

func TestRenderStreamImageAppliesDecodeArray(t *testing.T) {
	sd := flateImageStream(t, []byte{0, 255}, 2, 1, 1, model.DeviceGrayCS, nil)
	sd.Dict["Decode"] = types.Array{types.Integer(1), types.Integer(0)}

	img, err := renderStreamImage(emptyImageContext(), sd, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := color.GrayModel.Convert(img.At(0, 0)).(color.Gray).Y; got != 255 {
		t.Fatalf("first pixel = %d, want 255", got)
	}
	if got := color.GrayModel.Convert(img.At(1, 0)).(color.Gray).Y; got != 0 {
		t.Fatalf("second pixel = %d, want 0", got)
	}
}

func TestRenderStreamImageDecodesFlateCMYK(t *testing.T) {
	sd := flateImageStream(t, []byte{1, 2, 3, 4}, 1, 1, 4, model.DeviceCMYKCS, nil)

	img, err := renderStreamImage(emptyImageContext(), sd, 3)
	if err != nil {
		t.Fatal(err)
	}
	got := img.At(0, 0).(color.CMYK)
	if got != (color.CMYK{C: 1, M: 2, Y: 3, K: 4}) {
		t.Fatalf("pixel = %#v", got)
	}
}

func TestRenderStreamImageUsesICCBasedComponentCount(t *testing.T) {
	profileRef := *types.NewIndirectRef(9, 0)
	sd := flateImageStream(t, []byte{10, 20, 30}, 1, 1, 3, model.DeviceRGBCS, nil)
	sd.Dict["ColorSpace"] = types.Array{types.Name(model.ICCBasedCS), profileRef}
	ctx := emptyImageContext()
	ctx.Table[9] = model.NewXRefTableEntryGen0(types.StreamDict{Dict: types.Dict{"N": types.Integer(3)}})

	img, err := renderStreamImage(ctx, sd, 4)
	if err != nil {
		t.Fatal(err)
	}
	if got := color.NRGBAModel.Convert(img.At(0, 0)).(color.NRGBA); got != (color.NRGBA{R: 10, G: 20, B: 30, A: 255}) {
		t.Fatalf("pixel = %#v", got)
	}
}

func TestNormalizeDCTImageInvertsAdobeCMYK(t *testing.T) {
	src := image.NewCMYK(image.Rect(0, 0, 1, 1))
	src.Pix = []byte{255, 254, 253, 252}

	got := normalizeDCTImage(src, nil).(*image.CMYK).Pix
	want := []byte{0, 1, 2, 3}
	if !bytes.Equal(got, want) {
		t.Fatalf("CMYK pixels = %v, want %v", got, want)
	}
}

func TestNormalizeDCTImageAppliesDecodeInPlace(t *testing.T) {
	src := image.NewCMYK(image.Rect(0, 0, 1, 1))
	src.Pix = []byte{0, 64, 128, 255}
	decode := types.NewNumberArray(1, 0, 0, 1, 1, 0, 0, 1)

	got := normalizeDCTImage(src, decode)
	if got != src {
		t.Fatal("normalization returned a copied image")
	}
	want := []byte{0, 191, 128, 0}
	if !bytes.Equal(src.Pix, want) {
		t.Fatalf("CMYK pixels = %v, want %v", src.Pix, want)
	}
}

func flateImageStream(t *testing.T, pixels []byte, width, height, components int, colorSpace string, decodeParms types.Dict) *types.StreamDict {
	t.Helper()
	var encoded bytes.Buffer
	w := zlib.NewWriter(&encoded)
	if _, err := w.Write(pixels); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	filterEntry := types.PDFFilter{Name: filter.Flate, DecodeParms: decodeParms}
	return &types.StreamDict{
		Dict: types.Dict{
			"Width":            types.Integer(width),
			"Height":           types.Integer(height),
			"BitsPerComponent": types.Integer(8),
			"ColorSpace":       types.Name(colorSpace),
			"Filter":           types.Name(filter.Flate),
		},
		Raw:            encoded.Bytes(),
		FilterPipeline: []types.PDFFilter{filterEntry},
	}
}

func emptyImageContext() *model.Context {
	return &model.Context{XRefTable: &model.XRefTable{Table: map[int]*model.XRefTableEntry{}}}
}

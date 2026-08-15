package optimize

import (
	"bytes"
	"fmt"
	"image"

	"github.com/pdfcpu/pdfcpu/pkg/filter"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"

	"pdfslim/internal/imagecodec"
)

// renderStreamImage decodes a PDF image stream directly into pixels. pdfcpu's
// RenderImage assumes that StreamDict.Content was decoded by its extraction
// pipeline. The optimizer receives streams before that pipeline has run, so it
// must decode the stream itself.
func renderStreamImage(ctx *model.Context, sd *types.StreamDict, objNr int) (image.Image, error) {
	width, height := sd.IntEntry("Width"), sd.IntEntry("Height")
	if width == nil || height == nil || *width <= 0 || *height <= 0 {
		return nil, fmt.Errorf("render image object %d: invalid dimensions", objNr)
	}

	encodedFilter := imageFilter(sd)
	if encodedFilter == filter.DCT || encodedFilter == filter.JPX {
		data := sd.Content
		if len(data) == 0 {
			data = sd.Raw
		}
		if len(data) == 0 {
			return nil, fmt.Errorf("render image object %d: empty encoded stream", objNr)
		}
		img, _, err := imagecodec.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("render image object %d: %w", objNr, err)
		}
		if encodedFilter == filter.DCT {
			img = normalizeDCTImage(img, sd.ArrayEntry("Decode"))
		}
		return img, nil
	}

	if err := sd.Decode(); err != nil {
		return nil, fmt.Errorf("render image object %d: decode stream: %w", objNr, err)
	}
	bpc := sd.IntEntry("BitsPerComponent")
	if bpc == nil || *bpc != 8 {
		return nil, fmt.Errorf("render image object %d: unsupported bits per component %v", objNr, bpc)
	}

	components, err := imageColorComponents(ctx, sd)
	if err != nil {
		return nil, fmt.Errorf("render image object %d: %w", objNr, err)
	}
	w, h := *width, *height
	expected := w * h * components
	if len(sd.Content) < expected {
		return nil, fmt.Errorf("render image object %d: short pixel stream: got %d, want %d", objNr, len(sd.Content), expected)
	}
	decode := sd.ArrayEntry("Decode")

	switch components {
	case 1:
		img := image.NewGray(image.Rect(0, 0, w, h))
		for i := range img.Pix {
			img.Pix[i] = decodedComponent(sd.Content[i], decode, 0)
		}
		return img, nil
	case 3:
		img := image.NewNRGBA(image.Rect(0, 0, w, h))
		for source, target := 0, 0; source < expected; source, target = source+3, target+4 {
			img.Pix[target] = decodedComponent(sd.Content[source], decode, 0)
			img.Pix[target+1] = decodedComponent(sd.Content[source+1], decode, 1)
			img.Pix[target+2] = decodedComponent(sd.Content[source+2], decode, 2)
			img.Pix[target+3] = 0xff
		}
		return img, nil
	case 4:
		img := image.NewCMYK(image.Rect(0, 0, w, h))
		for source := 0; source < expected; source += 4 {
			img.Pix[source] = decodedComponent(sd.Content[source], decode, 0)
			img.Pix[source+1] = decodedComponent(sd.Content[source+1], decode, 1)
			img.Pix[source+2] = decodedComponent(sd.Content[source+2], decode, 2)
			img.Pix[source+3] = decodedComponent(sd.Content[source+3], decode, 3)
		}
		return img, nil
	default:
		return nil, fmt.Errorf("render image object %d: unsupported component count %d", objNr, components)
	}
}

// Adobe CMYK JPEG samples use the opposite polarity from image.CMYK: 255
// means no ink. Go's JPEG decoder preserves that polarity, so invert the four
// channels before normal image processing. The decoder result is exclusively
// owned by this package, so normalization changes its pixel buffer in place.
func normalizeDCTImage(img image.Image, decode types.Array) image.Image {
	src, ok := img.(*image.CMYK)
	if !ok {
		return img
	}
	for y := src.Bounds().Min.Y; y < src.Bounds().Max.Y; y++ {
		source := src.PixOffset(src.Bounds().Min.X, y)
		for x := src.Bounds().Min.X; x < src.Bounds().Max.X; x++ {
			for component := 0; component < 4; component++ {
				src.Pix[source+component] = decodedComponent(255-src.Pix[source+component], decode, component)
			}
			source += 4
		}
	}
	return src
}

func imageFilter(sd *types.StreamDict) string {
	if len(sd.FilterPipeline) == 0 {
		return ""
	}
	return sd.FilterPipeline[len(sd.FilterPipeline)-1].Name
}

func imageColorComponents(ctx *model.Context, sd *types.StreamDict) (int, error) {
	o, err := ctx.DereferenceDictEntry(sd.Dict, "ColorSpace")
	if err != nil {
		return 0, err
	}
	return colorSpaceComponents(ctx, o)
}

func colorSpaceComponents(ctx *model.Context, o types.Object) (int, error) {
	switch cs := o.(type) {
	case types.Name:
		switch cs {
		case model.DeviceGrayCS, model.CalGrayCS:
			return 1, nil
		case model.DeviceRGBCS, model.CalRGBCS, model.LabCS:
			return 3, nil
		case model.DeviceCMYKCS:
			return 4, nil
		}
	case types.Array:
		if len(cs) == 0 {
			break
		}
		name, ok := cs[0].(types.Name)
		if !ok {
			break
		}
		switch name {
		case model.ICCBasedCS:
			if len(cs) < 2 {
				break
			}
			profile, _, err := ctx.DereferenceStreamDict(cs[1])
			if err != nil || profile == nil {
				return 0, fmt.Errorf("invalid ICCBased profile")
			}
			n := profile.IntEntry("N")
			if n != nil && (*n == 1 || *n == 3 || *n == 4) {
				return *n, nil
			}
		case model.CalGrayCS:
			return 1, nil
		case model.CalRGBCS, model.LabCS:
			return 3, nil
		}
	}
	return 0, fmt.Errorf("unsupported color space %v", o)
}

func decodedComponent(value byte, decode types.Array, component int) byte {
	index := component * 2
	if len(decode) <= index+1 {
		return value
	}
	minValue, minOK := numberValue(decode[index])
	maxValue, maxOK := numberValue(decode[index+1])
	if !minOK || !maxOK {
		return value
	}
	result := minValue + float64(value)*(maxValue-minValue)/255
	if result <= 0 {
		return 0
	}
	if result >= 1 {
		return 255
	}
	return byte(result*255 + 0.5)
}

func numberValue(o types.Object) (float64, bool) {
	switch value := o.(type) {
	case types.Integer:
		return float64(value), true
	case types.Float:
		return float64(value), true
	default:
		return 0, false
	}
}

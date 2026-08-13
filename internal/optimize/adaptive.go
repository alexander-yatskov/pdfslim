package optimize

import (
	"compress/zlib"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"os"

	"github.com/pdfcpu/pdfcpu/pkg/filter"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
	"pdfslim/internal/ccittg4"
)

type imageCandidate struct {
	path        string
	size        int64
	filter      string
	colorSpace  types.Object
	bpc         int
	decodeParms types.Dict
}

// adaptiveImageStream writes PDF-native candidates to temporary files and
// loads only the smallest candidate into the PDF context. The caller still
// compares the result with the original stream.
func adaptiveImageStream(img image.Image, quality int, tempDir string) (*types.StreamDict, error) {
	return adaptiveImageStreamWithCCITT(img, quality, tempDir, true)
}

func adaptiveImageStreamWithCCITT(img image.Image, quality int, tempDir string, allowCCITT bool) (*types.StreamDict, error) {
	gray := isGrayImage(img)
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	cs := model.DeviceRGBCS
	if gray {
		cs = model.DeviceGrayCS
	}

	jpegCandidate, err := writeJPEGCandidate(img, gray, quality, tempDir)
	if err != nil {
		return nil, err
	}
	defer os.Remove(jpegCandidate.path)
	jpegCandidate.colorSpace = types.Name(cs)
	jpegCandidate.bpc = 8

	flateCandidate, err := writeFlateCandidate(img, gray, tempDir)
	if err != nil {
		return nil, err
	}
	defer os.Remove(flateCandidate.path)
	flateCandidate.colorSpace = types.Name(cs)
	flateCandidate.bpc = 8

	best := jpegCandidate
	if flateCandidate.size < best.size {
		best = flateCandidate
	}
	indexedCandidate, ok, err := writeIndexedCandidate(img, tempDir)
	if err != nil {
		return nil, err
	}
	if ok {
		defer os.Remove(indexedCandidate.path)
		if indexedCandidate.size < best.size {
			best = indexedCandidate
		}
	}
	if allowCCITT {
		ccittCandidate, ok, err := writeCCITTG4Candidate(img, tempDir)
		if err != nil {
			return nil, err
		}
		if ok {
			defer os.Remove(ccittCandidate.path)
			if ccittCandidate.size < best.size {
				best = ccittCandidate
			}
		}
	}
	data, err := os.ReadFile(best.path)
	if err != nil {
		return nil, fmt.Errorf("read selected image candidate: %w", err)
	}
	return encodedImageStream(data, w, h, best.colorSpace, best.bpc, best.filter, best.decodeParms), nil
}

func writeCCITTG4Candidate(img image.Image, tempDir string) (imageCandidate, bool, error) {
	b := img.Bounds()
	rows := make([][]byte, b.Dy())
	for y := b.Min.Y; y < b.Max.Y; y++ {
		row := make([]byte, b.Dx())
		for x := b.Min.X; x < b.Max.X; x++ {
			c := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
			switch {
			case c.R == 0 && c.G == 0 && c.B == 0:
				row[x-b.Min.X] = 0
			case c.R == 255 && c.G == 255 && c.B == 255:
				row[x-b.Min.X] = 1
			default:
				return imageCandidate{}, false, nil
			}
		}
		rows[y-b.Min.Y] = row
	}
	encoded := (&ccittg4.Encoder{K: -1, Columns: b.Dx(), Rows: b.Dy(), EndOfBlock: true}).Encode(rows)
	f, err := os.CreateTemp(tempDir, ".pdfslim-image-*.ccitt-g4")
	if err != nil {
		return imageCandidate{}, false, fmt.Errorf("create CCITT Group 4 candidate: %w", err)
	}
	path := f.Name()
	complete := false
	defer func() {
		if !complete {
			_ = os.Remove(path)
		}
	}()
	if _, err := f.Write(encoded); err != nil {
		_ = f.Close()
		return imageCandidate{}, false, fmt.Errorf("write CCITT Group 4 candidate: %w", err)
	}
	if err := f.Close(); err != nil {
		return imageCandidate{}, false, fmt.Errorf("close CCITT Group 4 candidate: %w", err)
	}
	complete = true
	decodeParms := types.Dict{
		"K":          types.Integer(-1),
		"Columns":    types.Integer(b.Dx()),
		"Rows":       types.Integer(b.Dy()),
		"EndOfBlock": types.Boolean(true),
	}
	return imageCandidate{
		path: path, size: int64(len(encoded)) + 64, filter: filter.CCITTFax,
		colorSpace: types.Name(model.DeviceGrayCS), bpc: 1, decodeParms: decodeParms,
	}, true, nil
}

func writeJPEGCandidate(img image.Image, gray bool, quality int, tempDir string) (imageCandidate, error) {
	f, err := os.CreateTemp(tempDir, ".pdfslim-image-*.jpg")
	if err != nil {
		return imageCandidate{}, fmt.Errorf("create JPEG candidate: %w", err)
	}
	path := f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(path)
		}
	}()
	jpegImage := img
	if gray {
		jpegImage = grayscaleView{Image: img}
	}
	if err := jpeg.Encode(f, jpegImage, &jpeg.Options{Quality: quality}); err != nil {
		_ = f.Close()
		return imageCandidate{}, fmt.Errorf("encode JPEG candidate: %w", err)
	}
	if err := f.Close(); err != nil {
		return imageCandidate{}, fmt.Errorf("close JPEG candidate: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return imageCandidate{}, fmt.Errorf("inspect JPEG candidate: %w", err)
	}
	ok = true
	return imageCandidate{path: path, size: info.Size(), filter: filter.DCT}, nil
}

func writeFlateCandidate(img image.Image, gray bool, tempDir string) (imageCandidate, error) {
	f, err := os.CreateTemp(tempDir, ".pdfslim-image-*.flate")
	if err != nil {
		return imageCandidate{}, fmt.Errorf("create Flate candidate: %w", err)
	}
	path := f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(path)
		}
	}()
	zw := zlib.NewWriter(f)
	if err := writeImageRows(zw, img, gray); err != nil {
		_ = zw.Close()
		_ = f.Close()
		return imageCandidate{}, fmt.Errorf("encode Flate candidate: %w", err)
	}
	if err := zw.Close(); err != nil {
		_ = f.Close()
		return imageCandidate{}, fmt.Errorf("close Flate encoder: %w", err)
	}
	if err := f.Close(); err != nil {
		return imageCandidate{}, fmt.Errorf("close Flate candidate: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return imageCandidate{}, fmt.Errorf("inspect Flate candidate: %w", err)
	}
	ok = true
	return imageCandidate{path: path, size: info.Size(), filter: filter.Flate}, nil
}

func writeIndexedCandidate(img image.Image, tempDir string) (imageCandidate, bool, error) {
	palette, colors, ok := indexedPalette(img)
	if !ok {
		return imageCandidate{}, false, nil
	}
	f, err := os.CreateTemp(tempDir, ".pdfslim-image-*.indexed-flate")
	if err != nil {
		return imageCandidate{}, false, fmt.Errorf("create Indexed Flate candidate: %w", err)
	}
	path := f.Name()
	complete := false
	defer func() {
		if !complete {
			_ = os.Remove(path)
		}
	}()
	zw := zlib.NewWriter(f)
	if err := writeIndexedRows(zw, img, colors); err != nil {
		_ = zw.Close()
		_ = f.Close()
		return imageCandidate{}, false, fmt.Errorf("encode Indexed Flate candidate: %w", err)
	}
	if err := zw.Close(); err != nil {
		_ = f.Close()
		return imageCandidate{}, false, fmt.Errorf("close Indexed Flate encoder: %w", err)
	}
	if err := f.Close(); err != nil {
		return imageCandidate{}, false, fmt.Errorf("close Indexed Flate candidate: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return imageCandidate{}, false, fmt.Errorf("inspect Indexed Flate candidate: %w", err)
	}
	complete = true
	colorSpace := types.Array{
		types.Name("Indexed"),
		types.Name(model.DeviceRGBCS),
		types.Integer(len(palette)/3 - 1),
		types.NewHexLiteral(palette),
	}
	// Hex lookup data uses two PDF bytes for each palette byte.
	size := info.Size() + int64(len(palette)*2)
	return imageCandidate{path: path, size: size, filter: filter.Flate, colorSpace: colorSpace, bpc: 8}, true, nil
}

func indexedPalette(img image.Image) (palette []byte, colors map[uint32]byte, ok bool) {
	b := img.Bounds()
	colors = map[uint32]byte{}
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
			key := uint32(c.R)<<16 | uint32(c.G)<<8 | uint32(c.B)
			index, found := colors[key]
			if !found {
				if len(colors) == 256 {
					return nil, nil, false
				}
				index = byte(len(colors))
				colors[key] = index
				palette = append(palette, c.R, c.G, c.B)
			}
		}
	}
	return palette, colors, len(palette) > 0
}

func writeIndexedRows(w io.Writer, img image.Image, colors map[uint32]byte) error {
	b := img.Bounds()
	row := make([]byte, b.Dx())
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
			key := uint32(c.R)<<16 | uint32(c.G)<<8 | uint32(c.B)
			row[x-b.Min.X] = colors[key]
		}
		if _, err := w.Write(row); err != nil {
			return err
		}
	}
	return nil
}

func encodedImageStream(data []byte, width, height int, colorSpace types.Object, bpc int, filterName string, decodeParms types.Dict) *types.StreamDict {
	length := int64(len(data))
	sd := &types.StreamDict{
		Dict: types.Dict{
			"Type":             types.Name("XObject"),
			"Subtype":          types.Name("Image"),
			"Width":            types.Integer(width),
			"Height":           types.Integer(height),
			"BitsPerComponent": types.Integer(bpc),
			"ColorSpace":       colorSpace,
			"Filter":           types.Name(filterName),
			"Length":           types.Integer(length),
		},
		StreamLength:   &length,
		FilterPipeline: []types.PDFFilter{{Name: filterName}},
		Raw:            data,
	}
	if decodeParms != nil {
		sd.Insert("DecodeParms", decodeParms)
		sd.FilterPipeline[0].DecodeParms = decodeParms
	}
	if width < 1000 || height < 1000 {
		sd.Insert("Interpolate", types.Boolean(true))
	}
	return sd
}

func encodedStreamSize(sd *types.StreamDict) int64 {
	if sd == nil {
		return 0
	}
	if sd.StreamLength != nil {
		return *sd.StreamLength
	}
	return int64(len(sd.Raw))
}

func isGrayImage(img image.Image) bool {
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, blue, _ := img.At(x, y).RGBA()
			if r != g || g != blue {
				return false
			}
		}
	}
	return true
}

type grayscaleView struct {
	image.Image
}

func (grayscaleView) ColorModel() color.Model {
	return color.GrayModel
}

func (g grayscaleView) At(x, y int) color.Color {
	return color.GrayModel.Convert(g.Image.At(x, y))
}

func writeImageRows(w io.Writer, img image.Image, gray bool) error {
	b := img.Bounds()
	components := 3
	if gray {
		components = 1
	}
	row := make([]byte, b.Dx()*components)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		i := 0
		for x := b.Min.X; x < b.Max.X; x++ {
			if gray {
				row[i] = color.GrayModel.Convert(img.At(x, y)).(color.Gray).Y
				i++
				continue
			}
			c := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
			row[i], row[i+1], row[i+2] = c.R, c.G, c.B
			i += 3
		}
		if _, err := w.Write(row); err != nil {
			return err
		}
	}
	return nil
}

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

type imageAnalysis struct {
	gray    bool
	bilevel bool
	palette []byte
	colors  map[uint32]byte
	indexed bool
}

// adaptiveImageStream writes PDF-native candidates to temporary files and
// loads only the smallest candidate into the PDF context. The caller still
// compares the result with the original stream.
func adaptiveImageStream(img image.Image, quality int, tempDir string) (*types.StreamDict, error) {
	return adaptiveImageStreamWithCCITT(img, quality, tempDir, true)
}

func adaptiveImageStreamWithCCITT(img image.Image, quality int, tempDir string, allowCCITT bool) (*types.StreamDict, error) {
	analysis := analyzeImage(img)
	gray := analysis.gray
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
	indexedCandidate, ok, err := writeIndexedCandidateFromAnalysis(img, analysis, tempDir)
	if err != nil {
		return nil, err
	}
	if ok {
		defer os.Remove(indexedCandidate.path)
		if indexedCandidate.size < best.size {
			best = indexedCandidate
		}
	}
	if allowCCITT && analysis.bilevel {
		ccittCandidate, ok, err := writeCCITTG4CandidateKnownBinary(img, tempDir)
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
	if !analyzeImage(img).bilevel {
		return imageCandidate{}, false, nil
	}
	return writeCCITTG4CandidateKnownBinary(img, tempDir)
}

func writeCCITTG4CandidateKnownBinary(img image.Image, tempDir string) (imageCandidate, bool, error) {
	b := img.Bounds()
	rows := make([][]byte, b.Dy())
	for y := b.Min.Y; y < b.Max.Y; y++ {
		row := make([]byte, b.Dx())
		visitImageRow(img, y, func(x int, r, _, _ byte) { row[x-b.Min.X] = r / 255 })
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
	return writeIndexedCandidateFromAnalysis(img, analyzeImage(img), tempDir)
}

func writeIndexedCandidateFromAnalysis(img image.Image, analysis imageAnalysis, tempDir string) (imageCandidate, bool, error) {
	if !analysis.indexed {
		return imageCandidate{}, false, nil
	}
	palette, colors := analysis.palette, analysis.colors
	bpc := indexedBitsPerComponent(len(palette) / 3)
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
	if err := writeIndexedRowsPacked(zw, img, colors, bpc); err != nil {
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
	return imageCandidate{path: path, size: size, filter: filter.Flate, colorSpace: colorSpace, bpc: bpc}, true, nil
}

func indexedPalette(img image.Image) (palette []byte, colors map[uint32]byte, ok bool) {
	a := analyzeImage(img)
	return a.palette, a.colors, a.indexed
}

func writeIndexedRows(w io.Writer, img image.Image, colors map[uint32]byte) error {
	return writeIndexedRowsPacked(w, img, colors, 8)
}

func writeIndexedRowsPacked(w io.Writer, img image.Image, colors map[uint32]byte, bpc int) error {
	b := img.Bounds()
	row := make([]byte, (b.Dx()*bpc+7)/8)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		clear(row)
		visitImageRow(img, y, func(x int, r, g, blue byte) {
			index := colors[uint32(r)<<16|uint32(g)<<8|uint32(blue)]
			pixel := x - b.Min.X
			switch bpc {
			case 1:
				row[pixel/8] |= index << uint(7-pixel%8)
			case 4:
				row[pixel/2] |= index << uint(4*(1-pixel%2))
			default:
				row[pixel] = index
			}
		})
		if _, err := w.Write(row); err != nil {
			return err
		}
	}
	return nil
}

func indexedBitsPerComponent(colors int) int {
	if colors <= 2 {
		return 1
	}
	if colors <= 16 {
		return 4
	}
	return 8
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
	if width < 1000 && height < 1000 {
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
	return analyzeImage(img).gray
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
		visitImageRow(img, y, func(_ int, r, g, blue byte) {
			if gray {
				row[i] = r
				i++
				return
			}
			row[i], row[i+1], row[i+2] = r, g, blue
			i += 3
		})
		if _, err := w.Write(row); err != nil {
			return err
		}
	}
	return nil
}

func analyzeImage(img image.Image) imageAnalysis {
	a := imageAnalysis{gray: true, bilevel: true, indexed: true, colors: map[uint32]byte{}}
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		visitImageRow(img, y, func(_ int, r, g, blue byte) {
			a.gray = a.gray && r == g && g == blue
			a.bilevel = a.bilevel && r == g && g == blue && (r == 0 || r == 255)
			if !a.indexed {
				return
			}
			key := uint32(r)<<16 | uint32(g)<<8 | uint32(blue)
			if _, found := a.colors[key]; found {
				return
			}
			if len(a.colors) == 256 {
				a.indexed, a.palette, a.colors = false, nil, nil
				return
			}
			a.colors[key] = byte(len(a.colors))
			a.palette = append(a.palette, r, g, blue)
		})
	}
	a.indexed = a.indexed && len(a.palette) > 0
	return a
}

func visitImageRow(img image.Image, y int, visit func(x int, r, g, b byte)) {
	b := img.Bounds()
	visitImageRowRange(img, y, b.Min.X, b.Max.X, visit)
}

func visitImageRowRange(img image.Image, y, minX, maxX int, visit func(x int, r, g, b byte)) {
	b := img.Bounds()
	minX, maxX = max(minX, b.Min.X), min(maxX, b.Max.X)
	switch src := img.(type) {
	case *image.Gray:
		off := src.PixOffset(minX, y)
		for x := minX; x < maxX; x++ {
			v := src.Pix[off]
			visit(x, v, v, v)
			off++
		}
	case *image.NRGBA:
		off := src.PixOffset(minX, y)
		for x := minX; x < maxX; x++ {
			visit(x, src.Pix[off], src.Pix[off+1], src.Pix[off+2])
			off += 4
		}
	case *image.RGBA:
		off := src.PixOffset(minX, y)
		for x := minX; x < maxX; x++ {
			r, g, blue, a := src.Pix[off], src.Pix[off+1], src.Pix[off+2], src.Pix[off+3]
			if a != 0 && a != 255 {
				r, g, blue = byte(uint16(r)*255/uint16(a)), byte(uint16(g)*255/uint16(a)), byte(uint16(blue)*255/uint16(a))
			}
			visit(x, r, g, blue)
			off += 4
		}
	case *image.YCbCr:
		for x := minX; x < maxX; x++ {
			yi := src.YOffset(x, y)
			ci := src.COffset(x, y)
			r, g, blue := color.YCbCrToRGB(src.Y[yi], src.Cb[ci], src.Cr[ci])
			visit(x, r, g, blue)
		}
	default:
		for x := minX; x < maxX; x++ {
			c := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
			visit(x, c.R, c.G, c.B)
		}
	}
}

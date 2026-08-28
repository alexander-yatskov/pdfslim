package optimize

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
	"golang.org/x/image/draw"

	"pdfslim/internal/pdfconfig"
)

type Profile string

const (
	ProfileLossless   Profile = "lossless"
	ProfileBalanced   Profile = "balanced"
	ProfileScreen     Profile = "screen"
	ProfilePrint      Profile = "print"
	ProfileEbook      Profile = "ebook"
	ProfileAggressive Profile = "aggressive"
)

type imageSettings struct {
	dpi                 int
	quality             int
	optimizeTransparent bool
	memoryBudget        *MemoryBudget
}

type Options struct {
	Context                        context.Context
	ImageMemoryBudget              *MemoryBudget
	DisableFontDeduplication       bool
	DisablePDFCPUFontDeduplication bool
}

func ParseProfile(s string) (Profile, error) {
	p := Profile(s)
	switch p {
	case ProfileLossless, ProfileBalanced, ProfileScreen, ProfilePrint, ProfileEbook, ProfileAggressive:
		return p, nil
	default:
		return "", fmt.Errorf("unsupported preset %q; use lossless, balanced, screen, print, ebook, or aggressive", s)
	}
}

type Result struct {
	Before  int64
	After   int64
	Saved   int64
	Percent float64
}

func OptimizeWithOptions(input, output string, profile Profile, options Options) (Result, error) {
	ctx := options.Context
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	settings, err := settingsFor(profile)
	if err != nil {
		return Result{}, err
	}
	if settings != nil {
		settings.memoryBudget = options.ImageMemoryBudget
	}
	same, err := sameFile(input, output)
	if err != nil {
		return Result{}, err
	}
	if same {
		return Result{}, fmt.Errorf("input and output paths must differ")
	}
	inputInfo, err := os.Stat(input)
	if err != nil {
		return Result{}, fmt.Errorf("read input information: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return Result{}, fmt.Errorf("create output directory: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(output), ".pdfslim-*.pdf")
	if err != nil {
		return Result{}, fmt.Errorf("create temporary output: %w", err)
	}
	tmpName := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return Result{}, fmt.Errorf("close temporary output: %w", err)
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmpName)
		}
	}()

	conf := pdfconfig.New()
	conf.WriteObjectStream = true
	conf.WriteXRefStream = true
	// pdfcpu's optimizer deduplicates whole font dictionaries while building its
	// resource index. That happens before our font-file deduplication and can
	// alter glyph mappings in PDFs with non-standard font resources.
	conf.Optimize = !options.DisableFontDeduplication && !options.DisablePDFCPUFontDeduplication
	conf.OptimizeResourceDicts = true
	conf.OptimizeDuplicateContentStreams = true
	conf.PostProcessValidate = true
	if err := optimizeFile(ctx, input, tmpName, conf, settings, !options.DisableFontDeduplication); err != nil {
		return Result{}, fmt.Errorf("parse and optimize PDF: %w", err)
	}

	optimizedInfo, err := os.Stat(tmpName)
	if err != nil {
		return Result{}, fmt.Errorf("read optimized file information: %w", err)
	}
	if optimizedInfo.Size() >= inputInfo.Size() {
		if err := copyFile(input, tmpName, inputInfo.Mode()); err != nil {
			return Result{}, err
		}
		optimizedInfo, err = os.Stat(tmpName)
		if err != nil {
			return Result{}, fmt.Errorf("read output information: %w", err)
		}
	}
	if err := os.Rename(tmpName, output); err != nil {
		return Result{}, fmt.Errorf("install output: %w", err)
	}
	ok = true

	result := Result{Before: inputInfo.Size(), After: optimizedInfo.Size()}
	result.Saved = result.Before - result.After
	if result.Before > 0 {
		result.Percent = float64(result.Saved) * 100 / float64(result.Before)
	}
	return result, nil
}

func settingsFor(profile Profile) (*imageSettings, error) {
	switch profile {
	case ProfileLossless:
		return nil, nil
	case ProfileBalanced:
		return &imageSettings{dpi: 180, quality: 82}, nil
	case ProfileScreen:
		return &imageSettings{dpi: 144, quality: 72}, nil
	case ProfilePrint:
		return &imageSettings{dpi: 300, quality: 90}, nil
	case ProfileEbook:
		return &imageSettings{dpi: 104, quality: 68}, nil
	case ProfileAggressive:
		return &imageSettings{dpi: 104, quality: 65, optimizeTransparent: true}, nil
	default:
		return nil, fmt.Errorf("unsupported profile %q", profile)
	}
}

func optimizeFile(processCtx context.Context, input, output string, conf *model.Configuration, settings *imageSettings, deduplicateFonts bool) error {
	in, err := os.Open(input)
	if err != nil {
		return err
	}
	defer in.Close()

	pdfCtx, err := api.ReadValidateAndOptimize(in, conf)
	if err != nil {
		return err
	}
	if err := processCtx.Err(); err != nil {
		return err
	}
	if deduplicateFonts {
		if err := deduplicateFontFiles(pdfCtx); err != nil {
			return fmt.Errorf("deduplicate embedded fonts: %w", err)
		}
	}
	if settings != nil {
		if err := optimizeImages(processCtx, pdfCtx, *settings); err != nil {
			return fmt.Errorf("optimize images: %w", err)
		}
	}
	if err := processCtx.Err(); err != nil {
		return err
	}

	out, err := os.OpenFile(output, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return err
	}
	if err := api.WriteContext(pdfCtx, out); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func optimizeImages(processCtx context.Context, ctx *model.Context, settings imageSettings) error {
	pageDims, err := ctx.PageDims()
	if err != nil {
		return err
	}
	renderedBounds := renderedImageBounds(ctx, settings.dpi)
	maskObjects := referencedMaskObjects(ctx)
	imageObjects := ctx.Optimize.ImageObjects
	if len(imageObjects) == 0 {
		imageObjects = imageObjectsFromRenderedBounds(ctx, renderedBounds)
	}
	for objNr, imageObject := range imageObjects {
		if err := processCtx.Err(); err != nil {
			return err
		}
		if maskObjects[objNr] {
			continue
		}
		sd := imageObject.ImageDict
		if sd == nil {
			continue
		}
		_, hasMask := sd.Find("Mask")
		_, hasSoftMask := sd.Find("SMask")
		if sd.BooleanEntry("ImageMask") != nil || hasMask {
			continue
		}
		if hasSoftMask {
			if settings.optimizeTransparent {
				width, height := sd.IntEntry("Width"), sd.IntEntry("Height")
				if width == nil || height == nil {
					continue
				}
				memory := estimatedImageMemory(*width, *height, true)
				acquired, err := settings.memoryBudget.acquire(processCtx, memory)
				if err != nil {
					return err
				}
				if !acquired {
					continue
				}
				if err := optimizeTransparentImage(ctx, objNr, imageObject, pageDims, renderedBounds, settings); err != nil {
					settings.memoryBudget.release(memory)
					return err
				}
				settings.memoryBudget.release(memory)
			}
			continue
		}
		width, height := sd.IntEntry("Width"), sd.IntEntry("Height")
		if width == nil || height == nil {
			continue
		}
		memory := estimatedImageMemory(*width, *height, false)
		acquired, err := settings.memoryBudget.acquire(processCtx, memory)
		if err != nil {
			return err
		}
		if !acquired {
			continue
		}
		img, err := decodeStreamImage(ctx, sd, objNr)
		if err != nil {
			// A valid PDF may contain an image that pdfcpu cannot render, for
			// example a malformed or unsupported Indexed color space. Keep that
			// original object unchanged and continue with the remaining images.
			settings.memoryBudget.release(memory)
			continue
		}
		maxW, maxH := imageBoundsForPages(pageDims, imageObject.ResourceNames, settings.dpi)
		if bounds, ok := renderedBounds[objNr]; ok && bounds.width > 0 && bounds.height > 0 {
			maxW, maxH = bounds.width, bounds.height
		}
		resized := downsample(img, maxW, maxH)
		allowCCITT := safeCCITTSource(sd)
		replacement, err := adaptiveImageStreamWithCCITT(resized, settings.quality, "", allowCCITT)
		if err != nil {
			settings.memoryBudget.release(memory)
			return err
		}
		if sd.StreamLength != nil && encodedStreamSize(replacement) >= *sd.StreamLength {
			settings.memoryBudget.release(memory)
			continue
		}
		entry := ctx.Table[objNr]
		if entry != nil {
			entry.Object = *replacement
			imageObject.ImageDict = replacement
		}
		settings.memoryBudget.release(memory)
	}
	return nil
}

// imageObjectsFromRenderedBounds builds the image index needed for image
// optimization without calling pdfcpu's OptimizeContext. The latter can merge
// font dictionaries in PDFs whose equal-looking fonts are not interchangeable.
// Restricting the index to images seen in page content also ensures every image
// has an actual rendered size before it is considered for downsampling.
func imageObjectsFromRenderedBounds(ctx *model.Context, bounds map[int]imageBounds) map[int]*model.ImageObject {
	images := make(map[int]*model.ImageObject, len(bounds))
	for objNr := range bounds {
		entry := ctx.Table[objNr]
		if entry == nil || entry.Free || entry.Object == nil {
			continue
		}
		stream, ok := entry.Object.(types.StreamDict)
		if !ok || !stream.Image() {
			continue
		}
		image := stream
		images[objNr] = &model.ImageObject{ImageDict: &image}
	}
	return images
}

func safeCCITTSource(sd *types.StreamDict) bool {
	bpc := sd.IntEntry("BitsPerComponent")
	cs := sd.NameEntry("ColorSpace")
	_, hasDecode := sd.Find("Decode")
	return bpc != nil && *bpc == 8 &&
		cs != nil && *cs == model.DeviceGrayCS &&
		sd.HasSoleFilterNamed("FlateDecode") && !hasDecode
}

func referencedMaskObjects(ctx *model.Context) types.IntSet {
	objects := types.IntSet{}
	for _, entry := range ctx.Table {
		if entry == nil || entry.Free || entry.Object == nil {
			continue
		}
		var d types.Dict
		switch o := entry.Object.(type) {
		case types.Dict:
			d = o
		case types.StreamDict:
			d = o.Dict
		case types.ObjectStreamDict:
			d = o.Dict
		}
		if d == nil {
			continue
		}
		for _, key := range []string{"Mask", "SMask"} {
			if ref := d.IndirectRefEntry(key); ref != nil {
				objects[ref.ObjectNumber.Value()] = true
			}
		}
		if smask, found := d.Find("SMask"); found {
			smaskDict, err := ctx.DereferenceDict(smask)
			if err == nil && smaskDict != nil {
				if group := smaskDict.IndirectRefEntry("G"); group != nil {
					collectFormImageObjects(ctx, *group, objects, types.IntSet{})
				}
			}
		}
	}
	return objects
}

func collectFormImageObjects(ctx *model.Context, ref types.IndirectRef, images, visitedForms types.IntSet) {
	objNr := ref.ObjectNumber.Value()
	if visitedForms[objNr] {
		return
	}
	visitedForms[objNr] = true
	sd, _, err := ctx.DereferenceStreamDict(ref)
	if err != nil || sd == nil {
		return
	}
	o, found := sd.Find("Resources")
	if !found {
		return
	}
	resources, err := ctx.DereferenceDict(o)
	if err != nil || resources == nil {
		return
	}
	o, found = resources.Find("XObject")
	if !found {
		return
	}
	xObjects, err := ctx.DereferenceDict(o)
	if err != nil || xObjects == nil {
		return
	}
	for _, object := range xObjects {
		childRef, ok := object.(types.IndirectRef)
		if !ok {
			continue
		}
		child, _, err := ctx.DereferenceStreamDict(childRef)
		if err != nil || child == nil || child.Subtype() == nil {
			continue
		}
		switch *child.Subtype() {
		case "Image":
			images[childRef.ObjectNumber.Value()] = true
		case "Form":
			collectFormImageObjects(ctx, childRef, images, visitedForms)
		}
	}
}

func imageBoundsForPages(pageDims []types.Dim, pages map[int]string, dpi int) (int, int) {
	maxW, maxH := 1, 1
	for page := range pages {
		if page < 0 || page >= len(pageDims) {
			continue
		}
		d := pageDims[page]
		w := int(d.Width*float64(dpi)/72 + 0.5)
		h := int(d.Height*float64(dpi)/72 + 0.5)
		if w > maxW {
			maxW = w
		}
		if h > maxH {
			maxH = h
		}
	}
	return maxW, maxH
}

func downsample(src image.Image, maxW, maxH int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= maxW && h <= maxH {
		return src
	}
	scale := min(float64(maxW)/float64(w), float64(maxH)/float64(h))
	dst := image.NewRGBA(image.Rect(0, 0, max(1, int(float64(w)*scale+0.5)), max(1, int(float64(h)*scale+0.5))))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)
	return dst
}

func optimizeTransparentImage(ctx *model.Context, objNr int, imageObject *model.ImageObject, pageDims []types.Dim, renderedBounds map[int]imageBounds, settings imageSettings) error {
	sd := imageObject.ImageDict
	width, height := sd.IntEntry("Width"), sd.IntEntry("Height")
	if width == nil || height == nil || *width < 64 || *height < 64 || *width > *height*20 || *height > *width*20 {
		return nil
	}
	maskRef := sd.IndirectRefEntry("SMask")
	if maskRef == nil || sd.StreamLength == nil {
		return nil
	}
	maskStream, _, err := ctx.DereferenceStreamDict(*maskRef)
	if err != nil || maskStream == nil || maskStream.StreamLength == nil {
		return nil
	}

	colorImage, err := decodeStreamImage(ctx, sd, objNr)
	if err != nil {
		return nil
	}
	maskImage, err := decodeStreamImage(ctx, maskStream, maskRef.ObjectNumber.Value())
	if err != nil {
		return nil
	}
	maxW, maxH := imageBoundsForPages(pageDims, imageObject.ResourceNames, settings.dpi)
	if bounds, ok := renderedBounds[objNr]; ok && bounds.width > 0 && bounds.height > 0 {
		maxW, maxH = bounds.width, bounds.height
	}
	colorImage = downsample(colorImage, maxW, maxH)
	maskImage = resizeTo(maskImage, colorImage.Bounds().Dx(), colorImage.Bounds().Dy())

	replacement, replacementMask, encodedBytes, err := transparentImageStreams(ctx.XRefTable, colorImage, maskImage, settings.quality)
	if err != nil {
		return err
	}
	originalBytes := *sd.StreamLength + *maskStream.StreamLength
	if encodedBytes >= originalBytes {
		return nil
	}
	newMaskRef, err := ctx.IndRefForNewObject(*replacementMask)
	if err != nil {
		return err
	}
	replacement.Insert("SMask", *newMaskRef)
	ctx.Table[objNr].Object = *replacement
	imageObject.ImageDict = replacement
	return nil
}

func decodeStreamImage(ctx *model.Context, sd *types.StreamDict, objNr int) (img image.Image, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			img = nil
			err = fmt.Errorf("render image object %d: %v", objNr, recovered)
		}
	}()
	return renderStreamImage(ctx, sd, objNr)
}

func resizeTo(src image.Image, width, height int) image.Image {
	if src.Bounds().Dx() == width && src.Bounds().Dy() == height {
		return src
	}
	dst := image.NewGray(image.Rect(0, 0, width, height))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Over, nil)
	return dst
}

func transparentImageStreams(xref *model.XRefTable, colorImage, maskImage image.Image, quality int) (*types.StreamDict, *types.StreamDict, int64, error) {
	bounds := colorImage.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	var jpegData bytes.Buffer
	if err := jpeg.Encode(&jpegData, colorImage, &jpeg.Options{Quality: quality}); err != nil {
		return nil, nil, 0, err
	}
	imageStream, err := model.CreateDCTImageStreamDict(xref, jpegData.Bytes(), width, height, 8, model.DeviceRGBCS)
	if err != nil {
		return nil, nil, 0, err
	}
	maskBytes := grayImageBytes(maskImage, maskImage.Bounds())
	maskStream, err := model.CreateFlateImageStreamDict(xref, maskBytes, nil, width, height, 8, model.DeviceGrayCS)
	if err != nil {
		return nil, nil, 0, err
	}
	return imageStream, maskStream, int64(len(imageStream.Raw) + len(maskStream.Raw)), nil
}

func grayImageBytes(src image.Image, bounds image.Rectangle) []byte {
	buf := make([]byte, 0, bounds.Dx()*bounds.Dy())
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		visitImageRowRange(src, y, bounds.Min.X, bounds.Max.X, func(_ int, r, g, b byte) {
			gray := byte((19595*uint32(r) + 38470*uint32(g) + 7471*uint32(b) + 1<<15) >> 16)
			buf = append(buf, gray)
		})
	}
	return buf
}

// deduplicateFontFiles makes font descriptors share an identical embedded font
// stream. Font dictionaries, encodings, widths, character maps, and metadata
// stay unchanged.
func deduplicateFontFiles(ctx *model.Context) error {
	canonical := map[[sha256.Size]byte][]types.IndirectRef{}
	objectNumbers := make([]int, 0, len(ctx.Table))
	for objectNumber := range ctx.Table {
		objectNumbers = append(objectNumbers, objectNumber)
	}
	sort.Ints(objectNumbers)
	for _, objectNumber := range objectNumbers {
		entry := ctx.Table[objectNumber]
		if entry == nil || entry.Free || entry.Object == nil {
			continue
		}
		d, ok := entry.Object.(types.Dict)
		if !ok {
			continue
		}
		typ := d.Type()
		if typ == nil || *typ != "FontDescriptor" {
			continue
		}

		for _, key := range []string{"FontFile", "FontFile2", "FontFile3"} {
			ref := d.IndirectRefEntry(key)
			if ref == nil {
				continue
			}
			stream, _, err := ctx.DereferenceStreamDict(*ref)
			if err != nil {
				return err
			}
			if stream == nil {
				continue
			}
			hash := fontStreamHash(stream)
			duplicate, err := identicalFontFile(ctx, stream, canonical[hash])
			if err != nil {
				return err
			}
			if duplicate != nil {
				d[key] = *duplicate
				ctx.IncrementRefCount(duplicate)
				ctx.Optimize.DuplicateFontObjs[ref.ObjectNumber.Value()] = true
				continue
			}
			canonical[hash] = append(canonical[hash], *ref)
		}
	}
	return nil
}

func fontStreamHash(stream *types.StreamDict) [sha256.Size]byte {
	if len(stream.Raw) > 0 {
		return sha256.Sum256(stream.Raw)
	}
	return sha256.Sum256(stream.Content)
}

func identicalFontFile(ctx *model.Context, candidateStream *types.StreamDict, canonical []types.IndirectRef) (*types.IndirectRef, error) {
	for _, ref := range canonical {
		stream, _, err := ctx.DereferenceStreamDict(ref)
		if err != nil {
			return nil, err
		}
		equal, err := model.EqualObjects(*candidateStream, *stream, ctx.XRefTable, nil)
		if err != nil {
			return nil, err
		}
		if equal {
			refCopy := ref
			return &refCopy, nil
		}
	}
	return nil, nil
}

// sameFile reports whether input and output identify the same file. Comparing
// only path strings is not sufficient because output can be a symbolic link or
// a hard link to input.
func sameFile(input, output string) (bool, error) {
	inputPath, err := filepath.Abs(input)
	if err != nil {
		return false, fmt.Errorf("resolve input path: %w", err)
	}
	outputPath, err := filepath.Abs(output)
	if err != nil {
		return false, fmt.Errorf("resolve output path: %w", err)
	}
	if filepath.Clean(inputPath) == filepath.Clean(outputPath) {
		return true, nil
	}

	inputInfo, err := os.Stat(inputPath)
	if err != nil {
		return false, fmt.Errorf("read input information: %w", err)
	}
	outputInfo, err := os.Stat(outputPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read output information: %w", err)
	}
	return os.SameFile(inputInfo, outputInfo), nil
}

func copyFile(source, destination string, mode os.FileMode) error {
	in, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open input: %w", err)
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_TRUNC, mode.Perm())
	if err != nil {
		return fmt.Errorf("open temporary output: %w", err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("copy input: %w", err)
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return fmt.Errorf("sync output: %w", err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close output: %w", err)
	}
	return nil
}

package imagecodec

import (
	"image"
	"io"

	_ "github.com/mububoki/jpeg2000/j2k"
	_ "github.com/mububoki/jpeg2000/jp2"
)

// Decode converts an encoded image stream into pixels. The blank imports above
// register JPEG 2000 codecs with the standard image package.
func Decode(r io.Reader) (image.Image, string, error) {
	return image.Decode(r)
}

package imagecodec

import (
	"image"
	"io"

	_ "github.com/mububoki/jpeg2000/j2k"
	_ "github.com/mububoki/jpeg2000/jp2"
)

// Decoder converts an encoded image stream into pixels. The interface keeps
// codec selection separate from PDF processing and allows bounded decoders to
// be added later.
type Decoder interface {
	Decode(io.Reader) (image.Image, string, error)
}

type StandardDecoder struct{}

func (StandardDecoder) Decode(r io.Reader) (image.Image, string, error) {
	return image.Decode(r)
}

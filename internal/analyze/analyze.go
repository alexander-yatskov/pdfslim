package analyze

import (
	"fmt"
	"io"
	"os"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"

	"pdfslim/internal/pdfconfig"
)

type Report struct {
	Size        int64
	Version     string
	Pages       int
	Images      int
	Fonts       int
	Streams     int
	StreamBytes int64
	ImageBytes  int64
	OtherBytes  int64
	Revisions   int
	Encrypted   bool
	Signed      bool
}

func File(path string) (Report, error) {
	f, err := os.Open(path)
	if err != nil {
		return Report{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	r, err := read(f)
	if err != nil {
		return Report{}, fmt.Errorf("analyze %s: %w", path, err)
	}
	return r, nil
}

func read(rs io.ReadSeeker) (Report, error) {
	conf := pdfconfig.New()
	ctx, err := api.ReadAndValidate(rs, conf)
	if err != nil {
		return Report{}, err
	}

	r := Report{
		Size:        ctx.Read.FileSize,
		Version:     ctx.VersionString(),
		Pages:       ctx.PageCount,
		StreamBytes: ctx.Read.BinaryTotalSize,
		Revisions:   1,
		Encrypted:   ctx.Encrypt != nil,
		Signed:      ctx.SignatureExist || ctx.AppendOnly || len(ctx.Signatures) > 0,
	}

	for _, entry := range ctx.Table {
		if entry == nil || entry.Free || entry.Object == nil {
			continue
		}
		if entry.Incr > r.Revisions {
			r.Revisions = entry.Incr
		}
		countObject(entry.Object, &r)
	}
	r.OtherBytes = r.StreamBytes - r.ImageBytes
	return r, nil
}

func countObject(obj types.Object, r *Report) {
	switch obj := obj.(type) {
	case types.Dict:
		if typ := obj.Type(); typ != nil && *typ == "Font" {
			r.Fonts++
		}
	case types.StreamDict:
		countStream(obj, r)
	case types.ObjectStreamDict:
		countStream(obj.StreamDict, r)
	case types.XRefStreamDict:
		countStream(obj.StreamDict, r)
	}
}

func countStream(stream types.StreamDict, r *Report) {
	r.Streams++
	if stream.Image() {
		r.Images++
		if stream.StreamLength != nil {
			r.ImageBytes += *stream.StreamLength
		}
	}
}

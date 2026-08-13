package imagecodec

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type manifest struct {
	Samples []struct {
		File   string `json:"file"`
		Width  int    `json:"width"`
		Height int    `json:"height"`
	} `json:"samples"`
}

func TestDecodeJPXSamples(t *testing.T) {
	data, err := os.ReadFile("testdata/jpx/manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	decoder := StandardDecoder{}
	for _, sample := range m.Samples {
		t.Run(sample.File, func(t *testing.T) {
			f, err := os.Open(filepath.Join("testdata/jpx", sample.File))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			img, format, err := decoder.Decode(f)
			if err != nil {
				t.Fatal(err)
			}
			if format != "j2k" && format != "jp2" {
				t.Fatalf("unexpected format %q", format)
			}
			if img.Bounds().Dx() != sample.Width || img.Bounds().Dy() != sample.Height {
				t.Fatalf("got %v, want %dx%d", img.Bounds(), sample.Width, sample.Height)
			}
		})
	}
}

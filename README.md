# pdfslim

`pdfslim` is an offline command-line tool that analyzes and optimizes PDF files.
It uses pure Go codecs and does not send files to external services.

Version: `0.1.0`

## Main features

- lossless PDF structure cleanup;
- adaptive image transcoding;
- JPEG 2000 decoding in pure Go;
- lossless Indexed Flate and CCITT Group 4 candidates;
- image downsampling based on the actual size on a page;
- safe handling of image masks and soft masks;
- duplicate embedded font-file removal;
- directory processing with a configurable worker count;
- soft memory limit and controlled shutdown;
- atomic output that does not overwrite the source file.

## Requirements

- Go 1.26.5 or newer.

No C libraries or external PDF tools are required.

## Build

```sh
git clone https://github.com/alexander-yatskov/pdfslim.git
cd pdfslim
go build -o pdfslim ./cmd/pdfslim
```

Check the build:

```sh
./pdfslim --version
```

## Basic use

Analyze a file without changing it:

```sh
./pdfslim --report document.pdf
```

Run lossless structure cleanup:

```sh
./pdfslim document.pdf
```

The default output is `document.slim.pdf`. The input file stays unchanged.

Optimize for an ebook:

```sh
./pdfslim --preset ebook document.pdf
```

Set an explicit output path:

```sh
./pdfslim --preset screen -o small.pdf document.pdf
```

## Presets

| Preset | Image processing |
| --- | --- |
| `lossless` | Structure cleanup only |
| `balanced` | Up to 180 DPI, JPEG quality 82 |
| `screen` | Up to 144 DPI, JPEG quality 72 |
| `print` | Up to 300 DPI, JPEG quality 90 |
| `ebook` | Up to 104 DPI, JPEG quality 68 |
| `aggressive` | Up to 104 DPI, JPEG quality 65, tiled soft-mask processing |

Image presets compare several relevant encodings and keep the smallest stream.
An original image stays unchanged when all new candidates are larger.

The adaptive layer can compare:

- JPEG (`DCTDecode`);
- Gray or RGB Flate (`FlateDecode`);
- exact Indexed Flate for images with up to 256 colors;
- lossless CCITT Group 4 for safe, strictly black-and-white sources.

CCITT Group 4 has a conservative safety rule. Existing 1-bit images, masks,
Indexed images, and images with custom `Decode` rules stay unchanged.

## Directory processing

Process all PDF files directly in a directory:

```sh
./pdfslim --preset ebook --suffix ebook --workers 2 ./books
```

This creates names such as `book.ebook.pdf`. Subdirectories are not scanned.
Files that already have the selected suffix are skipped.

One failed file does not stop other files. The command returns a non-zero exit
code after all started jobs finish.

## Memory control

The default soft memory limit is `1GiB`:

```sh
./pdfslim --memory-limit 2GiB --preset aggressive large.pdf
```

Use `--memory-limit 0` to disable the limit. The limit is soft. A Go process can
temporarily use more memory while a codec completes an active operation.

The default directory worker count is the smaller value of `2` and the number
of logical CPUs. Large files can require much memory, so increase `--workers`
carefully.

## Output control

Use `--quiet` to hide the normal operation report:

```sh
./pdfslim --quiet --preset ebook document.pdf
```

Errors are still written to stderr.

`SIGINT` and `SIGTERM` start a controlled shutdown. The tool stops new directory
jobs, removes its temporary files, and exits with code 130.

## Current limits

- directory processing is not recursive;
- tiled output does not provide tiled decoding;
- JPEG 2000 can be decoded, but new JPEG 2000 streams are not encoded;
- signed PDFs are reported and not modified safely as signed documents;
- image recompression can take much more time than structure cleanup.

## Third-party code

See [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

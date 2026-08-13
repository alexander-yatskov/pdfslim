package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"pdfslim/internal/analyze"
	"pdfslim/internal/optimize"
)

const version = "0.1.0"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(runContext(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	return runContext(context.Background(), args, stdout, stderr)
}

func runContext(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("pdfslim", flag.ContinueOnError)
	fs.SetOutput(stderr)
	output := fs.String("o", "", "output PDF path")
	reportOnly := fs.Bool("report", false, "report PDF details without changing it")
	analyzeOnly := fs.Bool("analyze", false, "alias for --report")
	quiet := fs.Bool("quiet", false, "do not print operation details")
	suffix := fs.String("suffix", "slim", "suffix for generated PDF names")
	workers := fs.Int("workers", defaultWorkers(), "number of parallel directory workers")
	memoryLimitText := fs.String("memory-limit", "1GiB", "soft memory limit: bytes, KiB, MiB, GiB, or 0")
	preset := fs.String("preset", "lossless", "optimization preset: lossless, balanced, screen, print, ebook, aggressive")
	showVersion := fs.Bool("version", false, "show version")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: pdfslim [options] input.pdf|directory")
		fmt.Fprintln(stderr, "\nOptions:")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *showVersion {
		fmt.Fprintln(stdout, version)
		return 0
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}
	profile, err := optimize.ParseProfile(*preset)
	if err != nil {
		fmt.Fprintf(stderr, "pdfslim: %v\n", err)
		return 2
	}
	if *quiet && (*reportOnly || *analyzeOnly) {
		fmt.Fprintln(stderr, "pdfslim: --quiet cannot be used with --report or --analyze")
		return 2
	}
	if err := validateSuffix(*suffix); err != nil {
		fmt.Fprintf(stderr, "pdfslim: %v\n", err)
		return 2
	}
	if *workers < 1 {
		fmt.Fprintln(stderr, "pdfslim: --workers must be at least 1")
		return 2
	}
	memoryLimit, err := parseByteSize(*memoryLimitText)
	if err != nil {
		fmt.Fprintf(stderr, "pdfslim: --memory-limit: %v\n", err)
		return 2
	}
	var memoryBudget *optimize.MemoryBudget
	if memoryLimit > 0 {
		if memoryLimit < 64<<20 {
			fmt.Fprintln(stderr, "pdfslim: --memory-limit must be at least 64MiB or 0")
			return 2
		}
		debug.SetMemoryLimit(memoryLimit)
		memoryBudget = optimize.NewMemoryBudget(memoryLimit / 5 * 3)
	} else {
		debug.SetMemoryLimit(-1)
	}

	input := fs.Arg(0)
	info, err := os.Stat(input)
	if err != nil {
		fmt.Fprintf(stderr, "pdfslim: read input information: %v\n", err)
		return 1
	}
	if info.IsDir() {
		if *output != "" {
			fmt.Fprintln(stderr, "pdfslim: -o cannot be used with a directory")
			return 2
		}
		return runDirectory(ctx, input, profile, *suffix, *workers, memoryBudget, *reportOnly || *analyzeOnly, *quiet, stdout, stderr)
	}
	text, err := processFile(ctx, input, *output, profile, *suffix, memoryBudget, *reportOnly || *analyzeOnly, *quiet)
	if err != nil {
		if ctx.Err() != nil {
			fmt.Fprintln(stderr, "pdfslim: interrupted")
			return 130
		}
		fmt.Fprintf(stderr, "pdfslim: %v\n", err)
		return 1
	}
	if text != "" {
		fmt.Fprint(stdout, text)
	}
	return 0
}

func defaultWorkers() int {
	return min(runtime.NumCPU(), 2)
}

func processFile(ctx context.Context, input, output string, profile optimize.Profile, suffix string, memoryBudget *optimize.MemoryBudget, reportOnly, quiet bool) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	report, err := analyze.File(input)
	if err != nil {
		return "", err
	}
	var text bytes.Buffer
	if reportOnly {
		printReport(&text, input, report)
		return text.String(), nil
	}
	if report.Encrypted {
		return "", fmt.Errorf("%s: encrypted PDFs are not supported", input)
	}
	if report.Signed {
		return "", fmt.Errorf("%s: refusing to rewrite a signed PDF", input)
	}

	destination := output
	if destination == "" {
		destination = outputPath(input, suffix)
	}
	result, err := optimize.OptimizeWithOptions(input, destination, profile, optimize.Options{Context: ctx, ImageMemoryBudget: memoryBudget})
	if err != nil {
		return "", err
	}
	if !quiet {
		printReport(&text, input, report)
		fmt.Fprintf(&text, "Output:     %s\n", destination)
		fmt.Fprintf(&text, "Output size: %s\n", formatBytes(result.After))
		fmt.Fprintf(&text, "Saved:      %s (%.1f%%)\n", formatBytes(result.Saved), result.Percent)
	}
	return text.String(), nil
}

func validateSuffix(suffix string) error {
	if suffix == "" || strings.TrimSpace(suffix) != suffix || suffix == "." || suffix == ".." {
		return fmt.Errorf("--suffix must be a non-empty file name component")
	}
	if strings.ContainsAny(suffix, `/\\`) {
		return fmt.Errorf("--suffix must not contain path separators")
	}
	return nil
}

func outputPath(input, suffix string) string {
	ext := filepath.Ext(input)
	return input[:len(input)-len(ext)] + "." + suffix + ".pdf"
}

type fileResult struct {
	text string
	err  error
}

func runDirectory(ctx context.Context, directory string, profile optimize.Profile, suffix string, workers int, memoryBudget *optimize.MemoryBudget, reportOnly, quiet bool, stdout, stderr io.Writer) int {
	entries, err := os.ReadDir(directory)
	if err != nil {
		fmt.Fprintf(stderr, "pdfslim: read directory: %v\n", err)
		return 1
	}
	var inputs []string
	generatedEnding := "." + strings.ToLower(suffix) + ".pdf"
	for _, entry := range entries {
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".pdf") {
			continue
		}
		if !reportOnly && strings.HasSuffix(strings.ToLower(entry.Name()), generatedEnding) {
			continue
		}
		inputs = append(inputs, filepath.Join(directory, entry.Name()))
	}
	sort.Strings(inputs)
	if len(inputs) == 0 {
		if !quiet {
			fmt.Fprintln(stdout, "No PDF files found.")
		}
		return 0
	}
	workers = min(workers, len(inputs))
	results := make([]fileResult, len(inputs))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				results[i].text, results[i].err = processFile(ctx, inputs[i], "", profile, suffix, memoryBudget, reportOnly, quiet)
			}
		}()
	}
	for i := range inputs {
		select {
		case jobs <- i:
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			fmt.Fprintln(stderr, "pdfslim: interrupted")
			return 130
		}
	}
	close(jobs)
	wg.Wait()

	exitCode := 0
	for i, result := range results {
		if result.err != nil {
			if ctx.Err() != nil {
				continue
			}
			fmt.Fprintf(stderr, "pdfslim: %s: %v\n", inputs[i], result.err)
			exitCode = 1
			continue
		}
		if result.text != "" {
			fmt.Fprint(stdout, result.text)
		}
	}
	if ctx.Err() != nil {
		fmt.Fprintln(stderr, "pdfslim: interrupted")
		return 130
	}
	return exitCode
}

func parseByteSize(value string) (int64, error) {
	if value == "0" {
		return 0, nil
	}
	units := []struct {
		suffix string
		factor int64
	}{
		{"GiB", 1 << 30},
		{"MiB", 1 << 20},
		{"KiB", 1 << 10},
		{"B", 1},
	}
	for _, unit := range units {
		if !strings.HasSuffix(value, unit.suffix) {
			continue
		}
		number := strings.TrimSuffix(value, unit.suffix)
		if number == "" {
			return 0, fmt.Errorf("missing number")
		}
		n, err := strconv.ParseInt(number, 10, 64)
		if err != nil || n <= 0 || n > (1<<63-1)/unit.factor {
			return 0, fmt.Errorf("invalid value %q", value)
		}
		return n * unit.factor, nil
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid value %q", value)
	}
	return n, nil
}

func printReport(w io.Writer, path string, r analyze.Report) {
	fmt.Fprintf(w, "File:       %s\n", path)
	fmt.Fprintf(w, "Size:       %s\n", formatBytes(r.Size))
	fmt.Fprintf(w, "PDF version: %s\n", r.Version)
	fmt.Fprintf(w, "Pages:      %d\n", r.Pages)
	fmt.Fprintf(w, "Images:     %d\n", r.Images)
	fmt.Fprintf(w, "Fonts:      %d\n", r.Fonts)
	fmt.Fprintf(w, "Streams:    %d (%s payload)\n", r.Streams, formatBytes(r.StreamBytes))
	fmt.Fprintf(w, "  Images:   %s\n", formatBytes(r.ImageBytes))
	fmt.Fprintf(w, "  Other:    %s\n", formatBytes(r.OtherBytes))
	fmt.Fprintf(w, "Revisions:  %d\n", r.Revisions)
	if r.Encrypted {
		fmt.Fprintln(w, "Encrypted:  yes")
	}
	if r.Signed {
		fmt.Fprintln(w, "Signed:     yes")
	}
}

func formatBytes(n int64) string {
	const unit = int64(1024)
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := unit, 0
	for value := n / unit; value >= unit && exp < 3; value /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGT"[exp])
}

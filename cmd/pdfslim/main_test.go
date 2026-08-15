package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReportDoesNotWriteOutput(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "in.pdf")
	if err := os.WriteFile(input, minimalPDF(), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--report", input}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Pages:      1") || !strings.Contains(stdout.String(), "Images:") {
		t.Fatalf("missing report data: %s", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "in.slim.pdf")); !os.IsNotExist(err) {
		t.Fatalf("report created an output file: %v", err)
	}
}

func TestQuietWritesOutputWithoutOperationDetails(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "in.pdf")
	output := filepath.Join(dir, "out.pdf")
	if err := os.WriteFile(input, minimalPDF(), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--quiet", "-o", output, input}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("quiet output: %q", stdout.String())
	}
	if _, err := os.Stat(output); err != nil {
		t.Fatalf("missing output: %v", err)
	}
}

func TestQuietRejectsReport(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--quiet", "--report", "in.pdf"}, &stdout, &stderr); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
}

func TestCustomSuffixWritesDefaultOutput(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "book.pdf")
	if err := os.WriteFile(input, minimalPDF(), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--quiet", "--suffix", "ebook", input}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "book.ebook.pdf")); err != nil {
		t.Fatalf("missing suffixed output: %v", err)
	}
}

func TestDirectoryProcessesPDFsInParallel(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"one.pdf", "two.PDF"} {
		if err := os.WriteFile(filepath.Join(dir, name), minimalPDF(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("not a PDF"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--quiet", "--workers", "2", "--suffix", "ebook", dir}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, stderr.String())
	}
	for _, name := range []string{"one.ebook.pdf", "two.ebook.pdf"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.ebook.pdf")); !os.IsNotExist(err) {
		t.Fatalf("non-PDF file was processed: %v", err)
	}
}

func TestDirectoryShowsProgressAndSeparatesReports(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"one.pdf", "two.pdf"} {
		if err := os.WriteFile(filepath.Join(dir, name), minimalPDF(), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--report", "--workers", "2", dir}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, stderr.String())
	}
	progress := stderr.String()
	if !strings.Contains(progress, "Processing 2 PDF files...") ||
		!strings.Contains(progress, "[1/2] Done:") ||
		!strings.Contains(progress, "[2/2] Done:") {
		t.Fatalf("missing directory progress: %s", progress)
	}
	if !strings.Contains(stdout.String(), "Revisions:  1\n\nFile:") {
		t.Fatalf("reports are not separated: %s", stdout.String())
	}
}

func TestDirectorySkipsFilesWithTargetSuffix(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"book.pdf", "old.ebook.pdf"} {
		if err := os.WriteFile(filepath.Join(dir, name), minimalPDF(), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--quiet", "--suffix", "ebook", dir}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "old.ebook.ebook.pdf")); !os.IsNotExist(err) {
		t.Fatalf("generated file was processed again: %v", err)
	}
}

func TestDirectoryContinuesAfterFileError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "good.pdf"), minimalPDF(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bad.pdf"), []byte("not a PDF"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--quiet", "--workers", "2", dir}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if _, err := os.Stat(filepath.Join(dir, "good.slim.pdf")); err != nil {
		t.Fatalf("valid PDF was not processed: %v; stderr: %s", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "bad.pdf") {
		t.Fatalf("missing failed file name: %s", stderr.String())
	}
}

func TestDirectoryRejectsExplicitOutput(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-o", "out.pdf", dir}, &stdout, &stderr); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
}

func TestRejectsInvalidSuffix(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--suffix", "bad/name", "input.pdf"}, &stdout, &stderr); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
}

func TestDefaultWorkersIsMemoryConservative(t *testing.T) {
	if got := defaultWorkers(); got < 1 || got > 2 {
		t.Fatalf("default workers = %d, want between 1 and 2", got)
	}
}

func TestParseByteSize(t *testing.T) {
	for input, want := range map[string]int64{
		"0":      0,
		"512MiB": 512 << 20,
		"2GiB":   2 << 30,
		"4096":   4096,
	} {
		got, err := parseByteSize(input)
		if err != nil || got != want {
			t.Fatalf("parseByteSize(%q) = %d, %v; want %d", input, got, err, want)
		}
	}
	for _, input := range []string{"", "-1", "1GB", "GiB"} {
		if _, err := parseByteSize(input); err == nil {
			t.Fatalf("parseByteSize(%q) accepted invalid value", input)
		}
	}
}

func TestRejectsTooSmallMemoryLimit(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--memory-limit", "32MiB", "input.pdf"}, &stdout, &stderr); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
}

func TestCanceledRunReturnsInterruptedExitCode(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dir := t.TempDir()
	input := filepath.Join(dir, "in.pdf")
	if err := os.WriteFile(input, minimalPDF(), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := runContext(ctx, []string{input}, &stdout, &stderr); code != 130 {
		t.Fatalf("exit %d, want 130; stderr: %s", code, stderr.String())
	}
}

func minimalPDF() []byte {
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] /Resources << >> /Contents 4 0 R >>",
		"<< /Length 0 >>\nstream\n\nendstream",
	}
	var b strings.Builder
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects)+1)
	for i, object := range objects {
		offsets[i+1] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, object)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n", len(objects)+1)
	b.WriteString("0000000000 65535 f \n")
	for i := 1; i <= len(objects); i++ {
		fmt.Fprintf(&b, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return []byte(b.String())
}

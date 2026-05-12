//go:build bench && whisper

package stt

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/partyzanex/a2text/pkg/audio"
)

const (
	benchGoWhisperURLEnv   = "GO_WHISPER_URL"
	benchGoWhisperModelEnv = "GO_WHISPER_MODEL"
	benchStrictEnv         = "BENCH_STRICT"
	benchLang              = "en"
	benchDefaultModel      = "ggml-small"
)

// benchSkipOrFatal turns a skip into a fatal when BENCH_STRICT is set, so CI
// and `make bench-stt` notice missing prerequisites instead of silently
// reporting a partial result set.
func benchSkipOrFatal(b *testing.B, format string, args ...any) {
	b.Helper()
	if os.Getenv(benchStrictEnv) != "" {
		b.Fatalf("BENCH_STRICT: "+format, args...)
	}
	b.Skipf(format, args...)
}

func BenchmarkGoWhisper_Short(b *testing.B) {
	tr := newBenchGoWhisper(b)
	benchmarkTranscribe(b, tr, benchPath(b, "jfk.ogg"))
}

func BenchmarkGoWhisper_Long(b *testing.B) {
	tr := newBenchGoWhisper(b)
	benchmarkTranscribe(b, tr, benchPath(b, "roosevelt_pearl_harbor_180s.ogg"))
}

func BenchmarkCGo_Short(b *testing.B) {
	tr := newBenchCGo(b)
	benchmarkTranscribe(b, tr, benchPath(b, "jfk.wav"))
}

func BenchmarkCGo_Long(b *testing.B) {
	tr := newBenchCGo(b)
	benchmarkTranscribe(b, tr, benchPath(b, "roosevelt_pearl_harbor_180s.wav"))
}

func BenchmarkGoWhisperPipeline_Short(b *testing.B) {
	tr := newBenchGoWhisper(b)
	converter := audio.NewPassthroughConverter()
	benchmarkPipeline(b, converter, tr, benchPath(b, "jfk.ogg"), false)
}

func BenchmarkGoWhisperPipeline_Long(b *testing.B) {
	tr := newBenchGoWhisper(b)
	converter := audio.NewPassthroughConverter()
	benchmarkPipeline(b, converter, tr, benchPath(b, "roosevelt_pearl_harbor_180s.ogg"), false)
}

func BenchmarkCGoPipeline_Short(b *testing.B) {
	tr := newBenchCGo(b)
	converter := audio.NewFFmpegConverter(5*time.Minute, b.TempDir(), benchLogger())
	benchmarkPipeline(b, converter, tr, benchPath(b, "jfk.ogg"), true)
}

func BenchmarkCGoPipeline_Long(b *testing.B) {
	tr := newBenchCGo(b)
	converter := audio.NewFFmpegConverter(10*time.Minute, b.TempDir(), benchLogger())
	benchmarkPipeline(b, converter, tr, benchPath(b, "roosevelt_pearl_harbor_180s.ogg"), true)
}

type benchTranscriber interface {
	Transcribe(ctx context.Context, wavPath string, lang string) (string, error)
}

type benchConverter interface {
	ToWAV(ctx context.Context, inputPath string) (wavPath string, err error)
}

func benchmarkTranscribe(b *testing.B, tr benchTranscriber, audioPath string) {
	b.Helper()
	setBenchBytes(b, audioPath)
	b.ResetTimer()

	for range b.N {
		text, err := tr.Transcribe(context.Background(), audioPath, benchLang)
		if err != nil {
			b.Fatalf("transcribe %s: %v", audioPath, err)
		}
		if text == "" {
			b.Fatalf("transcribe %s: empty result", audioPath)
		}
	}
}

func benchmarkPipeline(b *testing.B, converter benchConverter, tr benchTranscriber, inputPath string, cleanupConverted bool) {
	b.Helper()
	setBenchBytes(b, inputPath)
	b.ResetTimer()

	for range b.N {
		wavPath, err := converter.ToWAVFromFile(context.Background(), inputPath)
		if err != nil {
			b.Fatalf("convert %s: %v", inputPath, err)
		}
		text, err := tr.Transcribe(context.Background(), wavPath, benchLang)
		if cleanupConverted {
			_ = os.Remove(wavPath)
		}
		if err != nil {
			b.Fatalf("transcribe %s: %v", wavPath, err)
		}
		if text == "" {
			b.Fatalf("transcribe %s: empty result", wavPath)
		}
	}
}

func newBenchGoWhisper(b *testing.B) *GoWhisperTranscriber {
	b.Helper()
	baseURL := os.Getenv(benchGoWhisperURLEnv)
	if baseURL == "" {
		benchSkipOrFatal(b, "set %s to run go-whisper benchmarks", benchGoWhisperURLEnv)
	}
	model := os.Getenv(benchGoWhisperModelEnv)
	if model == "" {
		model = benchDefaultModel
	}

	tr := NewGoWhisperTranscriber(GoWhisperConfig{
		BaseURL: baseURL,
		Model:   model,
		Timeout: 30 * time.Minute,
	}, benchLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := tr.LoadModelCtx(ctx, model); err != nil {
		benchSkipOrFatal(b, "go-whisper model %q is not ready at %s: %v", model, baseURL, err)
	}
	return tr
}

func newBenchCGo(b *testing.B) *WhisperTranscriber {
	b.Helper()
	modelPath := os.Getenv(modelPathEnv)
	if modelPath == "" {
		benchSkipOrFatal(b, "set %s to run CGo whisper benchmarks", modelPathEnv)
	}

	tr := NewWhisperTranscriber(benchLogger())
	if err := tr.LoadModel(modelPath); err != nil {
		b.Fatalf("load whisper model %s: %v", modelPath, err)
	}
	b.Cleanup(func() { _ = tr.Close() })
	return tr
}

func benchPath(b *testing.B, name string) string {
	b.Helper()
	path := filepath.Join(repoRoot(b), "tests", "testdata", name)
	if _, err := os.Stat(path); err != nil {
		benchSkipOrFatal(b, "missing %s: run make testdata-prepare-bench", path)
	}
	return path
}

func repoRoot(b *testing.B) string {
	b.Helper()
	wd, err := os.Getwd()
	if err != nil {
		b.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(wd, "go.mod")); err == nil {
			return wd
		}
		next := filepath.Dir(wd)
		if next == wd {
			b.Fatal("go.mod not found while locating repo root")
		}
		wd = next
	}
}

func setBenchBytes(b *testing.B, path string) {
	b.Helper()
	info, err := os.Stat(path)
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(info.Size())
}

func benchLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

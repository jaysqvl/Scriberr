package adapters

import (
	"os"
	"path/filepath"
	"testing"

	"scriberr/internal/transcription/interfaces"
)

func TestCanaryArgsPassBatchSizeAndTimestampDisable(t *testing.T) {
	adapter := NewCanaryAdapter("/tmp/canary")
	input := interfaces.AudioInput{FilePath: "/tmp/audio.wav"}
	tempDir := t.TempDir()

	args, err := adapter.buildCanaryArgs(input, map[string]interface{}{
		"source_lang":         "en",
		"target_lang":         "en",
		"task":                "transcribe",
		"timestamps":          false,
		"batch_size":          2,
		"chunking":            true,
		"chunk_duration":      30,
		"device":              "cuda",
		"precision":           "float16",
		"include_confidence":  true,
		"preserve_formatting": true,
	}, tempDir)
	if err != nil {
		t.Fatalf("buildCanaryArgs returned error: %v", err)
	}

	assertArgValue(t, args, "--batch-size", "2")
	assertArgValue(t, args, "--chunk-len", "30")
	assertArgValue(t, args, "--device", "cuda")
	assertArgValue(t, args, "--precision", "float16")
	assertContainsArg(t, args, "--chunking")
	assertContainsArg(t, args, "--no-timestamps")
	assertNotContainsArg(t, args, "--timestamps")
	assertNotContainsArg(t, args, "--no-chunking")
}

func TestParakeetArgsPassBatchSizeAndTimestampDisable(t *testing.T) {
	adapter := NewParakeetAdapter("/tmp/parakeet")
	input := interfaces.AudioInput{FilePath: "/tmp/audio.wav"}
	tempDir := t.TempDir()

	args, err := adapter.buildParakeetArgs(input, map[string]interface{}{
		"timestamps":    false,
		"context_left":  128,
		"context_right": 64,
		"batch_size":    3,
	}, tempDir)
	if err != nil {
		t.Fatalf("buildParakeetArgs returned error: %v", err)
	}

	assertArgValue(t, args, "--batch-size", "3")
	assertArgValue(t, args, "--context-left", "128")
	assertArgValue(t, args, "--context-right", "64")
	assertContainsArg(t, args, "--no-timestamps")
	assertNotContainsArg(t, args, "--timestamps")
}

func TestParakeetBufferedArgsPreferParameterChunkDuration(t *testing.T) {
	t.Setenv("PARAKEET_CHUNK_THRESHOLD_SECS", "999")

	adapter := NewParakeetAdapter("/tmp/parakeet")
	input := interfaces.AudioInput{FilePath: "/tmp/audio.wav"}
	tempDir := t.TempDir()

	args, err := adapter.buildBufferedArgs(input, map[string]interface{}{
		"chunk_duration": 45,
	}, tempDir)
	if err != nil {
		t.Fatalf("buildBufferedArgs returned error: %v", err)
	}

	assertArgValue(t, args, "--chunk-len", "45")
}

func TestCanaryQwenArgsPassGenerationControls(t *testing.T) {
	adapter := NewCanaryQwenAdapter("/tmp/canary-qwen")
	input := interfaces.AudioInput{FilePath: "/tmp/audio.wav"}
	tempDir := t.TempDir()

	args, err := adapter.buildCanaryQwenArgs(input, map[string]interface{}{
		"timestamps":     true,
		"batch_size":     2,
		"chunk_duration": 40,
		"max_new_tokens": 512,
		"device":         "cuda",
		"precision":      "bfloat16",
		"prompt":         "Transcribe the following:",
	}, tempDir)
	if err != nil {
		t.Fatalf("buildCanaryQwenArgs returned error: %v", err)
	}

	assertContainsArg(t, args, "--timestamps")
	assertArgValue(t, args, "--batch-size", "2")
	assertArgValue(t, args, "--chunk-len", "40")
	assertArgValue(t, args, "--max-new-tokens", "512")
	assertArgValue(t, args, "--device", "cuda")
	assertArgValue(t, args, "--precision", "bfloat16")
	assertArgValue(t, args, "--prompt", "Transcribe the following:")
}

func TestCanaryQwenParseResultPreservesChunkSegments(t *testing.T) {
	adapter := NewCanaryQwenAdapter("/tmp/canary-qwen")
	tempDir := t.TempDir()
	resultJSON := `{
		"text": "hello world",
		"language": "en",
		"segments": [
			{"start": 0, "end": 10, "text": "hello"},
			{"start": 10, "end": 20, "text": "world"}
		]
	}`
	if err := os.WriteFile(filepath.Join(tempDir, "result.json"), []byte(resultJSON), 0644); err != nil {
		t.Fatalf("failed to write result fixture: %v", err)
	}

	result, err := adapter.parseCanaryQwenResult(tempDir)
	if err != nil {
		t.Fatalf("parseCanaryQwenResult returned error: %v", err)
	}
	if result.Text != "hello world" {
		t.Fatalf("expected text hello world, got %q", result.Text)
	}
	if len(result.Segments) != 2 {
		t.Fatalf("expected 2 segments, got %d", len(result.Segments))
	}
	if len(result.WordSegments) != 0 {
		t.Fatalf("expected no word segments, got %d", len(result.WordSegments))
	}
}

func assertArgValue(t *testing.T, args []string, flag, expected string) {
	t.Helper()
	for i, arg := range args {
		if arg == flag {
			if i+1 >= len(args) {
				t.Fatalf("flag %s has no value", flag)
			}
			if args[i+1] != expected {
				t.Fatalf("expected %s %s, got %s", flag, expected, args[i+1])
			}
			return
		}
	}
	t.Fatalf("missing flag %s in args %v", flag, args)
}

func assertContainsArg(t *testing.T, args []string, expected string) {
	t.Helper()
	for _, arg := range args {
		if arg == expected {
			return
		}
	}
	t.Fatalf("missing arg %s in args %v", expected, args)
}

func assertNotContainsArg(t *testing.T, args []string, expected string) {
	t.Helper()
	for _, arg := range args {
		if arg == expected {
			t.Fatalf("unexpected arg %s in args %v", expected, args)
		}
	}
}

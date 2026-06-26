package transcription

import (
	"testing"

	"scriberr/internal/models"
)

func TestConvertToParakeetParamsIncludesNvidiaControls(t *testing.T) {
	service := NewUnifiedTranscriptionService(nil, "data/temp", "data/transcripts")
	timestamps := false

	paramMap := service.convertToParakeetParams(models.WhisperXParams{
		BatchSize:             4,
		AttentionContextLeft:  128,
		AttentionContextRight: 64,
		NvidiaChunkDuration:   120,
		NvidiaTimestamps:      &timestamps,
	})

	if paramMap["batch_size"] != 4 {
		t.Fatalf("expected batch_size 4, got %v", paramMap["batch_size"])
	}
	if paramMap["timestamps"] != false {
		t.Fatalf("expected timestamps false, got %v", paramMap["timestamps"])
	}
	if paramMap["chunk_duration"] != 120 {
		t.Fatalf("expected chunk_duration 120, got %v", paramMap["chunk_duration"])
	}
	if paramMap["context_left"] != 128 || paramMap["context_right"] != 64 {
		t.Fatalf("expected attention contexts 128/64, got %v/%v", paramMap["context_left"], paramMap["context_right"])
	}
}

func TestConvertToCanaryParamsIncludesNvidiaControls(t *testing.T) {
	service := NewUnifiedTranscriptionService(nil, "data/temp", "data/transcripts")
	timestamps := false
	targetLang := "de"
	sourceLang := "fr"
	chunking := true

	paramMap := service.convertToCanaryParams(models.WhisperXParams{
		BatchSize:            99,
		Device:               "cuda",
		Task:                 "translate",
		Language:             &sourceLang,
		NvidiaChunkDuration:  30,
		NvidiaUseChunking:    &chunking,
		NvidiaPrecision:      "bfloat16",
		NvidiaTargetLanguage: &targetLang,
		NvidiaTimestamps:     &timestamps,
	})

	if paramMap["batch_size"] != 8 {
		t.Fatalf("expected clamped batch_size 8, got %v", paramMap["batch_size"])
	}
	if paramMap["timestamps"] != false {
		t.Fatalf("expected timestamps false, got %v", paramMap["timestamps"])
	}
	if paramMap["chunk_duration"] != 30 {
		t.Fatalf("expected chunk_duration 30, got %v", paramMap["chunk_duration"])
	}
	if paramMap["chunking"] != true {
		t.Fatalf("expected chunking true, got %v", paramMap["chunking"])
	}
	if paramMap["device"] != "cuda" {
		t.Fatalf("expected device cuda, got %v", paramMap["device"])
	}
	if paramMap["precision"] != "bfloat16" {
		t.Fatalf("expected precision bfloat16, got %v", paramMap["precision"])
	}
	if paramMap["source_lang"] != "fr" {
		t.Fatalf("expected source_lang fr, got %v", paramMap["source_lang"])
	}
	if paramMap["target_lang"] != "de" {
		t.Fatalf("expected target_lang de, got %v", paramMap["target_lang"])
	}
}

func TestConvertToCanaryParamsUsesChunkDefault(t *testing.T) {
	service := NewUnifiedTranscriptionService(nil, "data/temp", "data/transcripts")

	paramMap := service.convertToCanaryParams(models.WhisperXParams{})

	if paramMap["chunk_duration"] != 40 {
		t.Fatalf("expected chunk_duration 40, got %v", paramMap["chunk_duration"])
	}
	if paramMap["chunking"] != false {
		t.Fatalf("expected chunking false, got %v", paramMap["chunking"])
	}
	if paramMap["precision"] != "float16" {
		t.Fatalf("expected precision float16, got %v", paramMap["precision"])
	}
}

func TestConvertToCanaryQwenParamsUsesSafeDefaults(t *testing.T) {
	service := NewUnifiedTranscriptionService(nil, "data/temp", "data/transcripts")

	paramMap := service.convertToCanaryQwenParams(models.WhisperXParams{})

	if paramMap["batch_size"] != 1 {
		t.Fatalf("expected batch_size 1, got %v", paramMap["batch_size"])
	}
	if paramMap["chunk_duration"] != 40 {
		t.Fatalf("expected chunk_duration 40, got %v", paramMap["chunk_duration"])
	}
	if paramMap["precision"] != "float16" {
		t.Fatalf("expected precision float16, got %v", paramMap["precision"])
	}
	if paramMap["device"] != "auto" {
		t.Fatalf("expected device auto, got %v", paramMap["device"])
	}
	if paramMap["max_new_tokens"] != 256 {
		t.Fatalf("expected max_new_tokens 256, got %v", paramMap["max_new_tokens"])
	}
}

func TestSelectModelsIncludesCanaryQwen(t *testing.T) {
	service := NewUnifiedTranscriptionService(nil, "data/temp", "data/transcripts")

	transcriptionModelID, _, err := service.selectModels(models.WhisperXParams{
		ModelFamily: FamilyNvidiaCanaryQwen,
	})
	if err != nil {
		t.Fatalf("selectModels returned error: %v", err)
	}
	if transcriptionModelID != ModelCanaryQwen {
		t.Fatalf("expected %s, got %s", ModelCanaryQwen, transcriptionModelID)
	}
}

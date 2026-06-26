package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"scriberr/internal/transcription/interfaces"
	"scriberr/pkg/logger"
)

// CanaryQwenAdapter implements transcription for NVIDIA Canary-Qwen-2.5B.
type CanaryQwenAdapter struct {
	*BaseAdapter
	envPath string
}

// NewCanaryQwenAdapter creates a new Canary-Qwen adapter.
func NewCanaryQwenAdapter(envPath string) *CanaryQwenAdapter {
	capabilities := interfaces.ModelCapabilities{
		ModelID:            "canary_qwen",
		ModelFamily:        "nvidia_canary_qwen",
		DisplayName:        "NVIDIA Canary-Qwen 2.5B",
		Description:        "NVIDIA's English Canary-Qwen speech-augmented language model",
		Version:            "2.5.0",
		SupportedLanguages: []string{"en"},
		SupportedFormats:   []string{"wav", "flac"},
		RequiresGPU:        false,
		MemoryRequirement:  12288,
		Features: map[string]bool{
			"timestamps":      false,
			"word_level":      false,
			"english_only":    true,
			"high_quality":    true,
			"chunked":         true,
			"speech_lm":       true,
			"llm_postprocess": true,
		},
		Metadata: map[string]string{
			"engine":             "nvidia_nemo",
			"framework":          "speechlm2_salm",
			"license":            "CC-BY-4.0",
			"model_id":           "nvidia/canary-qwen-2.5b",
			"language":           "english_only",
			"sample_rate":        "16000",
			"format":             "16khz_mono_wav",
			"no_word_timestamps": "true",
			"chunk_recommended":  "40",
		},
	}

	schema := []interfaces.ParameterSchema{
		{
			Name:        "language",
			Type:        "string",
			Required:    false,
			Default:     "en",
			Options:     []string{"en"},
			Description: "Language of the audio",
			Group:       "basic",
		},
		{
			Name:        "timestamps",
			Type:        "bool",
			Required:    false,
			Default:     true,
			Description: "Include chunk-level timing segments",
			Group:       "basic",
		},
		{
			Name:        "output_format",
			Type:        "string",
			Required:    false,
			Default:     "json",
			Options:     []string{"json"},
			Description: "Output format for results",
			Group:       "basic",
		},
		{
			Name:        "auto_convert_audio",
			Type:        "bool",
			Required:    false,
			Default:     true,
			Description: "Automatically convert audio to 16kHz mono WAV",
			Group:       "advanced",
		},
		{
			Name:        "batch_size",
			Type:        "int",
			Required:    false,
			Default:     1,
			Min:         &[]float64{1}[0],
			Max:         &[]float64{8}[0],
			Description: "Number of chunks to process at once",
			Group:       "advanced",
		},
		{
			Name:        "chunk_duration",
			Type:        "int",
			Required:    false,
			Default:     40,
			Min:         &[]float64{10}[0],
			Max:         &[]float64{120}[0],
			Description: "Chunk duration in seconds",
			Group:       "advanced",
		},
		{
			Name:        "max_new_tokens",
			Type:        "int",
			Required:    false,
			Default:     256,
			Min:         &[]float64{64}[0],
			Max:         &[]float64{2048}[0],
			Description: "Maximum generated tokens per chunk",
			Group:       "advanced",
		},
		{
			Name:        "device",
			Type:        "string",
			Required:    false,
			Default:     "auto",
			Options:     []string{"auto", "cuda", "cpu"},
			Description: "Device for inference",
			Group:       "advanced",
		},
		{
			Name:        "precision",
			Type:        "string",
			Required:    false,
			Default:     "float16",
			Options:     []string{"float16", "bfloat16", "float32"},
			Description: "Model precision",
			Group:       "advanced",
		},
		{
			Name:        "prompt",
			Type:        "string",
			Required:    false,
			Default:     "Transcribe the following:",
			Description: "Prompt used for ASR generation",
			Group:       "advanced",
		},
	}

	baseAdapter := NewBaseAdapter("canary_qwen", envPath, capabilities, schema)

	return &CanaryQwenAdapter{
		BaseAdapter: baseAdapter,
		envPath:     envPath,
	}
}

// GetSupportedModels returns the Canary-Qwen model ID.
func (c *CanaryQwenAdapter) GetSupportedModels() []string {
	return []string{"nvidia/canary-qwen-2.5b"}
}

// PrepareEnvironment sets up the Canary-Qwen environment.
func (c *CanaryQwenAdapter) PrepareEnvironment(ctx context.Context) error {
	logger.Info("Preparing NVIDIA Canary-Qwen environment", "env_path", c.envPath)

	if err := c.copyTranscriptionScript(); err != nil {
		return fmt.Errorf("failed to copy transcription script: %w", err)
	}

	if CheckEnvironmentReady(c.envPath, "from nemo.collections.speechlm2.models import SALM") {
		logger.Info("Canary-Qwen environment already ready")
		c.initialized = true
		return nil
	}

	if err := c.setupCanaryQwenEnvironment(); err != nil {
		return fmt.Errorf("failed to setup Canary-Qwen environment: %w", err)
	}

	c.initialized = true
	logger.Info("Canary-Qwen environment prepared successfully")
	return nil
}

func (c *CanaryQwenAdapter) setupCanaryQwenEnvironment() error {
	if err := os.MkdirAll(c.envPath, 0755); err != nil {
		return fmt.Errorf("failed to create canary-qwen directory: %w", err)
	}

	pyprojectContent, err := nvidiaScripts.ReadFile("py/nvidia/canary_qwen_pyproject.toml")
	if err != nil {
		return fmt.Errorf("failed to read embedded canary_qwen_pyproject.toml: %w", err)
	}

	contentStr := strings.Replace(
		string(pyprojectContent),
		"https://download.pytorch.org/whl/cu126",
		GetPyTorchWheelURL(),
		1,
	)

	pyprojectPath := filepath.Join(c.envPath, "pyproject.toml")
	if err := os.WriteFile(pyprojectPath, []byte(contentStr), 0644); err != nil {
		return fmt.Errorf("failed to write pyproject.toml: %w", err)
	}

	logger.Info("Installing Canary-Qwen dependencies")
	cmd := exec.Command("uv", "sync", "--native-tls")
	cmd.Dir = c.envPath
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("uv sync failed: %w: %s", err, strings.TrimSpace(string(out)))
	}

	return nil
}

func (c *CanaryQwenAdapter) copyTranscriptionScript() error {
	if err := os.MkdirAll(c.envPath, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	scriptContent, err := nvidiaScripts.ReadFile("py/nvidia/canary_qwen_transcribe.py")
	if err != nil {
		return fmt.Errorf("failed to read embedded canary_qwen_transcribe.py: %w", err)
	}

	scriptPath := filepath.Join(c.envPath, "canary_qwen_transcribe.py")
	if err := os.WriteFile(scriptPath, scriptContent, 0755); err != nil {
		return fmt.Errorf("failed to write transcription script: %w", err)
	}

	return nil
}

// Transcribe processes audio using Canary-Qwen.
func (c *CanaryQwenAdapter) Transcribe(ctx context.Context, input interfaces.AudioInput, params map[string]interface{}, procCtx interfaces.ProcessingContext) (*interfaces.TranscriptResult, error) {
	startTime := time.Now()
	c.LogProcessingStart(input, procCtx)
	defer func() {
		c.LogProcessingEnd(procCtx, time.Since(startTime), nil)
	}()

	if err := c.ValidateAudioInput(input); err != nil {
		return nil, fmt.Errorf("invalid audio input: %w", err)
	}

	if err := c.ValidateParameters(params); err != nil {
		return nil, fmt.Errorf("invalid parameters: %w", err)
	}

	tempDir, err := c.CreateTempDirectory(procCtx)
	if err != nil {
		return nil, fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer c.CleanupTempDirectory(tempDir)

	audioInput := input
	if c.GetBoolParameter(params, "auto_convert_audio") {
		convertedInput, err := c.ConvertAudioFormat(ctx, input, "wav", 16000)
		if err != nil {
			logger.Warn("Audio conversion failed, using original", "error", err)
		} else {
			audioInput = convertedInput
		}
	}

	args, err := c.buildCanaryQwenArgs(audioInput, params, tempDir)
	if err != nil {
		return nil, fmt.Errorf("failed to build command: %w", err)
	}

	cmd := exec.CommandContext(ctx, "uv", args...)
	cmd.Env = append(os.Environ(),
		"PYTHONUNBUFFERED=1",
		"PYTORCH_CUDA_ALLOC_CONF=expandable_segments:True")

	logFile, err := os.OpenFile(filepath.Join(procCtx.OutputDirectory, "transcription.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		logger.Warn("Failed to create log file", "error", err)
	} else {
		defer logFile.Close()
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	}

	logger.Info("Executing Canary-Qwen command", "args", strings.Join(args, " "))

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.Canceled {
			return nil, fmt.Errorf("transcription was cancelled")
		}

		logPath := filepath.Join(procCtx.OutputDirectory, "transcription.log")
		logTail, readErr := c.ReadLogTail(logPath, 2048)
		if readErr != nil {
			logger.Warn("Failed to read log tail", "error", readErr)
		}

		logger.Error("Canary-Qwen execution failed", "error", err)
		return nil, fmt.Errorf("Canary-Qwen execution failed: %w\nLogs:\n%s", err, logTail)
	}

	result, err := c.parseCanaryQwenResult(tempDir)
	if err != nil {
		return nil, fmt.Errorf("failed to parse result: %w", err)
	}

	result.ProcessingTime = time.Since(startTime)
	result.ModelUsed = "nvidia/canary-qwen-2.5b"
	result.Metadata = c.CreateDefaultMetadata(params)

	logger.Info("Canary-Qwen transcription completed",
		"segments", len(result.Segments),
		"processing_time", result.ProcessingTime)

	return result, nil
}

func (c *CanaryQwenAdapter) buildCanaryQwenArgs(input interfaces.AudioInput, params map[string]interface{}, tempDir string) ([]string, error) {
	outputFile := filepath.Join(tempDir, "result.json")
	scriptPath := filepath.Join(c.envPath, "canary_qwen_transcribe.py")

	args := []string{
		"run", "--native-tls", "--project", c.envPath, "python", scriptPath,
		input.FilePath,
		"--output", outputFile,
	}

	if c.GetBoolParameter(params, "timestamps") {
		args = append(args, "--timestamps")
	} else {
		args = append(args, "--no-timestamps")
	}

	args = append(args,
		"--batch-size", strconv.Itoa(c.GetIntParameter(params, "batch_size")),
		"--chunk-len", strconv.Itoa(c.GetIntParameter(params, "chunk_duration")),
		"--max-new-tokens", strconv.Itoa(c.GetIntParameter(params, "max_new_tokens")),
		"--device", c.GetStringParameter(params, "device"),
		"--precision", c.GetStringParameter(params, "precision"),
	)

	if prompt := c.GetStringParameter(params, "prompt"); prompt != "" {
		args = append(args, "--prompt", prompt)
	}

	return args, nil
}

func (c *CanaryQwenAdapter) parseCanaryQwenResult(tempDir string) (*interfaces.TranscriptResult, error) {
	resultFile := filepath.Join(tempDir, "result.json")

	data, err := os.ReadFile(resultFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read result file: %w", err)
	}

	var canaryQwenResult struct {
		Text     string `json:"text"`
		Language string `json:"language"`
		Segments []struct {
			Start float64 `json:"start"`
			End   float64 `json:"end"`
			Text  string  `json:"text"`
		} `json:"segments"`
	}

	if err := json.Unmarshal(data, &canaryQwenResult); err != nil {
		return nil, fmt.Errorf("failed to parse JSON result: %w", err)
	}

	result := &interfaces.TranscriptResult{
		Text:         canaryQwenResult.Text,
		Language:     canaryQwenResult.Language,
		Segments:     make([]interfaces.TranscriptSegment, len(canaryQwenResult.Segments)),
		WordSegments: nil,
		Confidence:   0.0,
	}

	for i, seg := range canaryQwenResult.Segments {
		result.Segments[i] = interfaces.TranscriptSegment{
			Start: seg.Start,
			End:   seg.End,
			Text:  seg.Text,
		}
	}

	return result, nil
}

// GetEstimatedProcessingTime provides Canary-Qwen-specific time estimation.
func (c *CanaryQwenAdapter) GetEstimatedProcessingTime(input interfaces.AudioInput) time.Duration {
	baseTime := c.BaseAdapter.GetEstimatedProcessingTime(input)
	return time.Duration(float64(baseTime) * 2.5)
}

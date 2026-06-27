# Python Adapters Testing

This directory contains the Python adapter scripts for various transcription and diarization models used by Scriberr.

## Running Tests

The tests are located in the `tests/` subdirectory of each adapter folder (e.g., `nvidia/tests/`, `pyannote/tests/`). These tests verify that the Python scripts can be executed and produce the expected output.

To run the tests, you need `uv` installed and the `parakeet` environment set up (which serves as a shared environment for these tests).

## Dependency Harness

The adapter `pyproject.toml` files are runtime compatibility surfaces. Run the resolver guard before changing Python, NeMo, Torch, ONNX, or `ml-dtypes` versions:

```bash
python3 scripts/verify-python-adapter-envs.py --mode lock --python 3.12
```

The lock mode checks each adapter pyproject against the runtime Python range, verifies fragile package bands, and imports the resolved ONNX/`ml-dtypes` pair for the NVIDIA environments. Canary-Qwen specifically asserts that `ml_dtypes.float4_e2m1fn` exists so the known ONNX import crash cannot drift back in unnoticed.

Full adapter import checks are available when you want to materialize the Python envs without downloading model weights:

```bash
python3 scripts/verify-python-adapter-envs.py --mode import --torch-index cpu --adapter canary-qwen --python 3.12
python3 scripts/verify-python-adapter-envs.py --mode import --torch-index cpu --adapter nvidia-asr --python 3.12
python3 scripts/verify-python-adapter-envs.py --mode import --torch-index cpu --adapter pyannote --python 3.12
python3 scripts/verify-python-adapter-envs.py --mode import --torch-index cpu --adapter voxtral --python 3.12
```

Import mode can be slow because it installs NeMo, Torch, and the adapter stack, but it does not require a GPU. Use `--torch-index project` when you specifically want to materialize the CUDA wheel selection used by the Docker runtime.

### Prerequisites

1.  Ensure you have `uv` installed.
2.  Ensure the `parakeet` and `pyannote` environments set up within `data/whisperx-env/`. This is typically handled by the application startup.
3.  Ensure you have the test data available (e.g., `tests/data/AMI-Corpus-IB4002.Mix-Headset-clip.wav`).

### Running Tests with pytest

```bash
# Run all NVIDIA adapter tests
uv run --with pytest --project data/whisperx-env/parakeet pytest internal/transcription/adapters/py/nvidia/tests

# Run PyAnnote adapter tests
uv run --with pytest --project data/whisperx-env/pyannote pytest internal/transcription/adapters/py/pyannote/tests
```

### Troubleshooting

*   **Audio file not found**: Ensure `tests/data/AMI-Corpus-IB4002.Mix-Headset-clip.wav` exists.
*   **Environment not found**: Ensure `data/whisperx-env/parakeet` and the `pyannote` one exist and is a valid virtual environment. This may not be true if scriberr hasn't run yet.

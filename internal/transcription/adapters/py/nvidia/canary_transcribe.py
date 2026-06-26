#!/usr/bin/env python3
"""
NVIDIA Canary multilingual transcription and translation script.
"""

import argparse
import json
import os
import sys
import tempfile
import traceback
from typing import Dict, Iterable, List, Optional, Tuple

import librosa
import nemo.collections.asr as nemo_asr
import soundfile as sf
import torch


def log(message: str) -> None:
    print(message, flush=True)


def cuda_memory_snapshot(label: str) -> None:
    if not torch.cuda.is_available():
        log(f"CUDA memory [{label}]: CUDA is not available")
        return

    device = torch.cuda.current_device()
    free_bytes, total_bytes = torch.cuda.mem_get_info(device)
    allocated = torch.cuda.memory_allocated(device)
    reserved = torch.cuda.memory_reserved(device)
    log(
        "CUDA memory [{label}]: device={device_name} free={free:.2f}GiB "
        "total={total:.2f}GiB allocated={allocated:.2f}GiB reserved={reserved:.2f}GiB".format(
            label=label,
            device_name=torch.cuda.get_device_name(device),
            free=free_bytes / 1024**3,
            total=total_bytes / 1024**3,
            allocated=allocated / 1024**3,
            reserved=reserved / 1024**3,
        )
    )


def empty_cuda_cache(label: str) -> None:
    if torch.cuda.is_available():
        torch.cuda.empty_cache()
        cuda_memory_snapshot(label)


def locate_model_file() -> str:
    model_filename = "canary-1b-v2.nemo"
    virtual_env = os.environ.get("VIRTUAL_ENV")
    if not virtual_env:
        raise RuntimeError("VIRTUAL_ENV is not set. Script must be run with 'uv run'.")

    project_root = os.path.dirname(virtual_env)
    model_path = os.path.join(project_root, model_filename)
    if not os.path.exists(model_path):
        raise FileNotFoundError(f"Can't find {model_filename} in project root: {project_root}")
    return model_path


def configure_torch() -> None:
    log(f"Python: {sys.version.split()[0]}")
    log(f"PyTorch: {torch.__version__}")
    log(f"CUDA available: {torch.cuda.is_available()}")
    if torch.cuda.is_available():
        log(f"CUDA runtime: {torch.version.cuda}")
        log(f"CUDA device count: {torch.cuda.device_count()}")
        torch.backends.cuda.matmul.allow_tf32 = True
        torch.backends.cudnn.allow_tf32 = True
        try:
            torch.set_float32_matmul_precision("high")
        except Exception as exc:
            log(f"Warning: failed to set matmul precision: {exc}")


def resolve_device(device: str) -> torch.device:
    if device == "auto":
        return torch.device("cuda" if torch.cuda.is_available() else "cpu")
    if device == "cuda" and not torch.cuda.is_available():
        raise RuntimeError("CUDA was requested but is not available")
    return torch.device(device)


def model_device(model) -> str:
    try:
        parameter = next(model.parameters())
        return str(parameter.device)
    except Exception:
        return "unknown"


def restore_model(model_path: str):
    log("Restoring NVIDIA Canary model on CPU first to reduce peak GPU memory...")
    try:
        return nemo_asr.models.ASRModel.restore_from(model_path, map_location=torch.device("cpu"))
    except TypeError:
        log("restore_from did not accept map_location; retrying with NeMo defaults")
        return nemo_asr.models.ASRModel.restore_from(model_path)


def configure_model(model, torch_device: torch.device, precision: str):
    if torch_device.type == "cuda":
        if precision == "bfloat16":
            log("Converting model to bfloat16")
            model = model.bfloat16()
        elif precision == "float16":
            log("Converting model to float16")
            model = model.half()
        else:
            log("Using model float32 precision")
            model = model.float()
    else:
        if precision != "float32":
            log(f"{precision} on CPU is not recommended; using float32")
        model = model.float()

    model = model.to(torch_device)
    model.eval()
    if hasattr(model, "freeze"):
        model.freeze()
        log("Model frozen for inference")
    log(f"Model device after configuration: {model_device(model)}")
    cuda_memory_snapshot("after model configuration")
    return model


def split_audio_file(audio_path: str, chunk_duration_secs: float, temp_dir: str) -> List[Dict[str, float]]:
    log(f"Loading audio for chunking: {audio_path}")
    audio, sr = librosa.load(audio_path, sr=16000, mono=True)
    total_duration = len(audio) / sr if sr else 0
    chunk_samples = int(chunk_duration_secs * sr)
    if chunk_samples <= 0:
        raise ValueError("chunk duration must be greater than 0")

    log(
        "Audio loaded: sample_rate={sample_rate} duration={duration:.2f}s chunk_len={chunk_len:.2f}s".format(
            sample_rate=sr,
            duration=total_duration,
            chunk_len=chunk_duration_secs,
        )
    )

    chunks = []
    for chunk_index, start_sample in enumerate(range(0, len(audio), chunk_samples)):
        end_sample = min(start_sample + chunk_samples, len(audio))
        chunk_audio = audio[start_sample:end_sample]
        start_time = start_sample / sr
        end_time = end_sample / sr
        chunk_path = os.path.join(temp_dir, f"canary_chunk_{chunk_index:05d}.wav")
        sf.write(chunk_path, chunk_audio, sr)
        chunks.append({
            "path": chunk_path,
            "start": start_time,
            "end": end_time,
            "duration": end_time - start_time,
        })

    log(f"Created {len(chunks)} Canary chunks")
    return chunks


def timestamp_dicts(items: Iterable, offset_seconds: float) -> List[Dict]:
    shifted = []
    for item in items or []:
        try:
            item_dict = dict(item)
        except Exception:
            continue

        for key in ("start", "end"):
            if key in item_dict and item_dict[key] is not None:
                try:
                    item_dict[key] = float(item_dict[key]) + offset_seconds
                except (TypeError, ValueError):
                    pass
        shifted.append(item_dict)
    return shifted


def transcribe_one(
    asr_model,
    audio_path: str,
    source_lang: str,
    target_lang: str,
    timestamps: bool,
    batch_size: int,
):
    kwargs = {
        "source_lang": source_lang,
        "target_lang": target_lang,
        "batch_size": batch_size,
    }
    if timestamps:
        kwargs["timestamps"] = True

    output = asr_model.transcribe([audio_path], **kwargs)
    if not output:
        raise RuntimeError("Canary returned no transcription output")
    return output[0]


def result_text(result_data) -> str:
    text = getattr(result_data, "text", "")
    if text is None:
        return ""
    return str(text)


def result_timestamps(result_data) -> Dict:
    timestamps = getattr(result_data, "timestamp", None)
    if isinstance(timestamps, dict):
        return timestamps
    return {}


def collect_result(
    result_data,
    offset_seconds: float,
    timestamps: bool,
    include_confidence: bool,
) -> Tuple[str, List[Dict], List[Dict], Optional[object]]:
    text = result_text(result_data)
    words: List[Dict] = []
    segments: List[Dict] = []
    if timestamps:
        timestamp_data = result_timestamps(result_data)
        words = timestamp_dicts(timestamp_data.get("word", []), offset_seconds)
        segments = timestamp_dicts(timestamp_data.get("segment", []), offset_seconds)

    confidence = None
    if include_confidence and getattr(result_data, "confidence", None):
        confidence = result_data.confidence
    return text, words, segments, confidence


def transcribe_audio(
    audio_path: str,
    source_lang: str = "en",
    target_lang: str = "en",
    task: str = "transcribe",
    timestamps: bool = True,
    output_file: str = None,
    include_confidence: bool = True,
    preserve_formatting: bool = True,
    batch_size: int = 1,
    chunking: bool = False,
    chunk_duration_secs: float = 40,
    device: str = "auto",
    precision: str = "float16",
):
    model_path = locate_model_file()

    log("Starting NVIDIA Canary transcription")
    log(f"Model path: {model_path}")
    log(f"Audio path: {audio_path}")
    log(f"Task: {task}")
    log(f"Source language: {source_lang}")
    log(f"Target language: {target_lang}")
    log(f"Timestamps: {timestamps}")
    log(f"Batch size: {batch_size}")
    log(f"Chunking: {chunking}")
    log(f"Chunk duration: {chunk_duration_secs}s")
    log(f"Device: {device}")
    log(f"Precision: {precision}")
    log(f"Preserve formatting: {preserve_formatting}")

    configure_torch()
    torch_device = resolve_device(device)
    cuda_memory_snapshot("before model restore")

    asr_model = restore_model(model_path)
    log(f"Model restored: {type(asr_model).__name__}")
    log(f"Model device after restore: {model_device(asr_model)}")
    cuda_memory_snapshot("after model restore")
    asr_model = configure_model(asr_model, torch_device, precision)
    empty_cuda_cache("after model setup cache clear")

    full_text = []
    word_timestamps = []
    segment_timestamps = []
    confidence = None

    if not chunking:
        log("Using native full-file Canary transcription")
        cuda_memory_snapshot("before native transcription")
        try:
            with torch.inference_mode():
                result_data = transcribe_one(asr_model, audio_path, source_lang, target_lang, timestamps, batch_size)
        except torch.cuda.OutOfMemoryError:
            log("CUDA out of memory during native full-file transcription")
            log("Try enabling Canary chunking or reducing timestamp/batch settings.")
            cuda_memory_snapshot("oom")
            raise

        text, words, segments, result_confidence = collect_result(result_data, 0, timestamps, include_confidence)
        full_text.append(text)
        word_timestamps.extend(words)
        segment_timestamps.extend(segments)
        confidence = result_confidence
        log(f"Native transcription complete: {len(text)} characters")
        del result_data
        empty_cuda_cache("after native transcription")
    else:
        log("Using chunked Canary transcription")
        with tempfile.TemporaryDirectory(prefix="canary_chunks_") as temp_dir:
            chunks = split_audio_file(audio_path, chunk_duration_secs, temp_dir)

            for index, chunk in enumerate(chunks, start=1):
                log(
                    "Transcribing chunk {index}/{total}: start={start:.2f}s end={end:.2f}s duration={duration:.2f}s".format(
                        index=index,
                        total=len(chunks),
                        start=chunk["start"],
                        end=chunk["end"],
                        duration=chunk["duration"],
                    )
                )
                cuda_memory_snapshot(f"before chunk {index}")

                try:
                    with torch.inference_mode():
                        result_data = transcribe_one(
                            asr_model,
                            chunk["path"],
                            source_lang,
                            target_lang,
                            timestamps,
                            batch_size,
                        )
                except torch.cuda.OutOfMemoryError:
                    log(f"CUDA out of memory while transcribing chunk {index}")
                    cuda_memory_snapshot("oom")
                    raise

                text, words, segments, result_confidence = collect_result(
                    result_data,
                    chunk["start"],
                    timestamps,
                    include_confidence,
                )
                full_text.append(text)
                word_timestamps.extend(words)
                segment_timestamps.extend(segments)
                if confidence is None:
                    confidence = result_confidence
                log(f"Chunk {index} complete: {len(text)} characters")

                del result_data
                empty_cuda_cache(f"after chunk {index}")

    final_text = " ".join(part for part in full_text if part).strip()
    log(f"Transcription complete: {len(final_text)} characters total")

    output_data = {
        "transcription": final_text,
        "source_language": source_lang,
        "target_language": target_lang,
        "task": task,
        "word_timestamps": word_timestamps,
        "segment_timestamps": segment_timestamps,
        "audio_file": audio_path,
        "model": "canary-1b-v2",
        "batch_size": batch_size,
        "chunking": chunking,
        "chunk_duration_secs": chunk_duration_secs if chunking else None,
        "num_chunks": len(full_text),
        "device": str(torch_device),
        "precision": precision,
    }
    if include_confidence and confidence:
        output_data["confidence"] = confidence

    if output_file:
        with open(output_file, "w", encoding="utf-8") as f:
            json.dump(output_data, f, indent=2, ensure_ascii=False)
        log(f"Results saved to: {output_file}")
    else:
        log(json.dumps(output_data, indent=2, ensure_ascii=False))


def main():
    parser = argparse.ArgumentParser(
        description="Transcribe or translate audio using NVIDIA Canary model"
    )
    parser.add_argument("audio_file", help="Path to audio file")
    parser.add_argument(
        "--source-lang",
        default="en",
        choices=["en", "de", "es", "fr", "hi", "it", "ja", "ko", "pl", "pt", "ru", "zh"],
        help="Source language (default: en)",
    )
    parser.add_argument(
        "--target-lang",
        default="en",
        choices=["en", "de", "es", "fr", "hi", "it", "ja", "ko", "pl", "pt", "ru", "zh"],
        help="Target language (default: en)",
    )
    parser.add_argument(
        "--task",
        choices=["transcribe", "translate"],
        default="transcribe",
        help="Task to perform (default: transcribe)",
    )
    parser.add_argument(
        "--timestamps",
        action="store_true",
        default=True,
        help="Include word and segment level timestamps",
    )
    parser.add_argument(
        "--no-timestamps",
        dest="timestamps",
        action="store_false",
        help="Disable timestamps",
    )
    parser.add_argument("--output", "-o", help="Output file path")
    parser.add_argument(
        "--include-confidence",
        action="store_true",
        default=True,
        help="Include confidence scores",
    )
    parser.add_argument(
        "--no-confidence",
        dest="include_confidence",
        action="store_false",
        help="Exclude confidence scores",
    )
    parser.add_argument(
        "--preserve-formatting",
        action="store_true",
        default=True,
        help="Preserve punctuation and capitalization",
    )
    parser.add_argument(
        "--batch-size",
        type=int,
        default=1,
        help="Batch size for NeMo transcription (default: 1)",
    )
    parser.add_argument(
        "--chunking",
        action="store_true",
        default=False,
        help="Split audio into chunks before Canary transcription",
    )
    parser.add_argument(
        "--no-chunking",
        dest="chunking",
        action="store_false",
        help="Use Canary native full-file transcription",
    )
    parser.add_argument(
        "--chunk-len",
        type=float,
        default=40,
        help="Chunk duration in seconds when chunking is enabled (default: 40)",
    )
    parser.add_argument(
        "--device",
        choices=["auto", "cuda", "cpu"],
        default="auto",
        help="Inference device",
    )
    parser.add_argument(
        "--precision",
        choices=["float16", "bfloat16", "float32"],
        default="float16",
        help="Model precision",
    )

    args = parser.parse_args()

    if not os.path.exists(args.audio_file):
        log(f"Error: Audio file not found: {args.audio_file}")
        sys.exit(1)

    try:
        transcribe_audio(
            audio_path=args.audio_file,
            source_lang=args.source_lang,
            target_lang=args.target_lang,
            task=args.task,
            timestamps=args.timestamps,
            output_file=args.output,
            include_confidence=args.include_confidence,
            preserve_formatting=args.preserve_formatting,
            batch_size=args.batch_size,
            chunking=args.chunking,
            chunk_duration_secs=args.chunk_len,
            device=args.device,
            precision=args.precision,
        )
    except Exception as exc:
        log(f"Error during transcription: {exc}")
        traceback.print_exc()
        sys.exit(1)


if __name__ == "__main__":
    main()

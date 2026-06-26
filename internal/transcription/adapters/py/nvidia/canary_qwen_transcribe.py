#!/usr/bin/env python3
"""
NVIDIA Canary-Qwen transcription script.
"""

import argparse
import json
import os
import sys
import tempfile
from typing import Iterable, List

import librosa
import soundfile as sf
import torch
from nemo.collections.speechlm2.models import SALM


def batched(items: List[dict], batch_size: int) -> Iterable[List[dict]]:
    for idx in range(0, len(items), batch_size):
        yield items[idx:idx + batch_size]


def split_audio_file(audio_path: str, chunk_duration_secs: float, temp_dir: str) -> List[dict]:
    audio, sr = librosa.load(audio_path, sr=16000, mono=True)
    chunk_samples = int(chunk_duration_secs * sr)
    if chunk_samples <= 0:
        raise ValueError("chunk_duration_secs must be greater than 0")

    chunks = []
    for chunk_index, start_sample in enumerate(range(0, len(audio), chunk_samples)):
        end_sample = min(start_sample + chunk_samples, len(audio))
        chunk_audio = audio[start_sample:end_sample]
        start_time = start_sample / sr
        end_time = end_sample / sr
        chunk_path = os.path.join(temp_dir, f"canary_qwen_chunk_{chunk_index}.wav")
        sf.write(chunk_path, chunk_audio, sr)
        chunks.append({
            "path": chunk_path,
            "start": start_time,
            "end": end_time,
        })

    return chunks


def resolve_device(device: str) -> torch.device:
    if device == "auto":
        return torch.device("cuda" if torch.cuda.is_available() else "cpu")
    if device == "cuda" and not torch.cuda.is_available():
        raise RuntimeError("CUDA was requested but is not available")
    return torch.device(device)


def configure_model(model, device: torch.device, precision: str):
    if device.type == "cuda":
        torch.backends.cuda.matmul.allow_tf32 = True
        torch.backends.cudnn.allow_tf32 = True

    if precision == "bfloat16":
        model = model.bfloat16()
    elif precision == "float16":
        if device.type == "cuda":
            model = model.half()
        else:
            print("float16 on CPU is not supported well; using float32 instead")
            model = model.float()
    else:
        model = model.float()

    return model.eval().to(device)


def decode_answer(model, answer_ids) -> str:
    if hasattr(answer_ids, "cpu"):
        answer_ids = answer_ids.cpu()
    if hasattr(answer_ids, "tolist"):
        answer_ids = answer_ids.tolist()
    text = model.tokenizer.ids_to_text(answer_ids)
    return text.replace("<|endoftext|>", "").strip()


def build_prompt(model, prompt: str) -> str:
    prompt = prompt.strip() or "Transcribe the following:"
    if model.audio_locator_tag in prompt:
        return prompt
    return f"{prompt} {model.audio_locator_tag}"


def transcribe_audio(
    audio_path: str,
    output_file: str,
    batch_size: int = 1,
    chunk_duration_secs: float = 40,
    max_new_tokens: int = 256,
    device: str = "auto",
    precision: str = "float16",
    prompt: str = "Transcribe the following:",
    timestamps: bool = True,
):
    print("Loading NVIDIA Canary-Qwen model: nvidia/canary-qwen-2.5b")
    torch_device = resolve_device(device)
    model = SALM.from_pretrained("nvidia/canary-qwen-2.5b")
    model = configure_model(model, torch_device, precision)

    print(f"Processing: {audio_path}")
    print(f"Device: {torch_device}")
    print(f"Precision: {precision}")
    print(f"Batch size: {batch_size}")
    print(f"Chunk duration: {chunk_duration_secs}s")
    print(f"Max new tokens: {max_new_tokens}")

    with tempfile.TemporaryDirectory(prefix="canary_qwen_") as temp_dir:
        chunks = split_audio_file(audio_path, chunk_duration_secs, temp_dir)
        print(f"Created {len(chunks)} chunks")

        prompt_text = build_prompt(model, prompt)
        full_text = []
        segments = []

        with torch.inference_mode():
            for batch in batched(chunks, max(1, batch_size)):
                prompts = [
                    [{
                        "role": "user",
                        "content": prompt_text,
                        "audio": [chunk["path"]],
                    }]
                    for chunk in batch
                ]
                answer_ids = model.generate(
                    prompts=prompts,
                    max_new_tokens=max_new_tokens,
                )

                for chunk, answer in zip(batch, answer_ids):
                    text = decode_answer(model, answer)
                    full_text.append(text)
                    if timestamps:
                        segments.append({
                            "start": chunk["start"],
                            "end": chunk["end"],
                            "text": text,
                        })
                    print(f"Chunk {len(full_text)}/{len(chunks)} complete: {len(text)} characters")

    final_text = " ".join(part for part in full_text if part).strip()

    output_data = {
        "text": final_text,
        "language": "en",
        "segments": segments,
        "word_timestamps": [],
        "model": "nvidia/canary-qwen-2.5b",
        "batch_size": batch_size,
        "chunk_duration_secs": chunk_duration_secs,
        "max_new_tokens": max_new_tokens,
        "has_word_timestamps": False,
    }

    with open(output_file, "w", encoding="utf-8") as f:
        json.dump(output_data, f, indent=2, ensure_ascii=False)

    print(f"Results saved to: {output_file}")


def main():
    parser = argparse.ArgumentParser(
        description="Transcribe audio using NVIDIA Canary-Qwen"
    )
    parser.add_argument("audio_file", help="Path to audio file")
    parser.add_argument("--output", "-o", required=True, help="Output file path")
    parser.add_argument("--batch-size", type=int, default=1, help="Batch size for chunk generation")
    parser.add_argument("--chunk-len", type=float, default=40, help="Chunk duration in seconds")
    parser.add_argument("--max-new-tokens", type=int, default=256, help="Maximum generated tokens per chunk")
    parser.add_argument("--device", choices=["auto", "cuda", "cpu"], default="auto", help="Inference device")
    parser.add_argument(
        "--precision",
        choices=["float16", "bfloat16", "float32"],
        default="float16",
        help="Model precision",
    )
    parser.add_argument(
        "--prompt",
        default="Transcribe the following:",
        help="Prompt text. The audio locator is appended automatically if omitted.",
    )
    parser.add_argument("--timestamps", action="store_true", default=True, help="Include chunk-level timestamps")
    parser.add_argument("--no-timestamps", dest="timestamps", action="store_false", help="Disable chunk-level timestamps")

    args = parser.parse_args()

    if not os.path.exists(args.audio_file):
        print(f"Error: Audio file not found: {args.audio_file}")
        sys.exit(1)

    try:
        transcribe_audio(
            audio_path=args.audio_file,
            output_file=args.output,
            batch_size=args.batch_size,
            chunk_duration_secs=args.chunk_len,
            max_new_tokens=args.max_new_tokens,
            device=args.device,
            precision=args.precision,
            prompt=args.prompt,
            timestamps=args.timestamps,
        )
    except Exception as exc:
        print(f"Error during transcription: {exc}")
        sys.exit(1)


if __name__ == "__main__":
    main()

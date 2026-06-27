#!/usr/bin/env python3
from __future__ import annotations

import argparse
import re
import shlex
import shutil
import subprocess
import sys
import tempfile
import textwrap
import tomllib
from dataclasses import dataclass
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]


@dataclass(frozen=True)
class PackageRange:
    name: str
    min_inclusive: str | None = None
    max_exclusive: str | None = None
    exact: str | None = None


@dataclass(frozen=True)
class SourceExpectation:
    package: str
    field: str
    value: str


@dataclass(frozen=True)
class AdapterSpec:
    key: str
    label: str
    pyproject: Path
    import_code: str
    requires_python: str
    package_ranges: tuple[PackageRange, ...] = ()
    expected_sources: tuple[SourceExpectation, ...] = ()
    pair_import_packages: tuple[str, ...] = ()
    pair_import_code: str | None = None


ADAPTERS: tuple[AdapterSpec, ...] = (
    AdapterSpec(
        key="nvidia-asr",
        label="NVIDIA ASR adapters",
        pyproject=ROOT / "internal/transcription/adapters/py/nvidia/pyproject.toml",
        import_code="import nemo.collections.asr",
        requires_python=">=3.11,<3.13",
        package_ranges=(
            PackageRange("torch", min_inclusive="2.6", max_exclusive="2.8"),
            PackageRange("torchaudio", min_inclusive="2.6", max_exclusive="2.8"),
            PackageRange("ml-dtypes", min_inclusive="0.3.1", max_exclusive="0.5.0"),
            PackageRange("onnx", min_inclusive="1.15.0", max_exclusive="1.18.0"),
        ),
        expected_sources=(
            SourceExpectation("nemo-toolkit", "tag", "v2.5.3"),
        ),
        pair_import_packages=("onnx", "ml-dtypes"),
        pair_import_code=(
            "import ml_dtypes, onnx; "
            "print(f'onnx={onnx.__version__} ml_dtypes={ml_dtypes.__version__}')"
        ),
    ),
    AdapterSpec(
        key="canary-qwen",
        label="Canary-Qwen SALM adapter",
        pyproject=ROOT / "internal/transcription/adapters/py/nvidia/canary_qwen_pyproject.toml",
        import_code="from nemo.collections.speechlm2.models import SALM",
        requires_python=">=3.11,<3.13",
        package_ranges=(
            PackageRange("torch", min_inclusive="2.6", max_exclusive="2.8"),
            PackageRange("torchaudio", min_inclusive="2.6", max_exclusive="2.8"),
            PackageRange("ml-dtypes", min_inclusive="0.5.0", max_exclusive="0.6.0"),
            PackageRange("onnx", min_inclusive="1.18.0", max_exclusive="1.20.0"),
        ),
        expected_sources=(
            SourceExpectation(
                "nemo-toolkit",
                "rev",
                "b366d85e6619092257e7f3063e3838cdea34c054",
            ),
        ),
        pair_import_packages=("onnx", "ml-dtypes"),
        pair_import_code=(
            "import ml_dtypes, onnx; "
            "assert hasattr(ml_dtypes, 'float4_e2m1fn'), "
            "'ml_dtypes.float4_e2m1fn is missing'; "
            "print(f'onnx={onnx.__version__} ml_dtypes={ml_dtypes.__version__}')"
        ),
    ),
    AdapterSpec(
        key="pyannote",
        label="Pyannote diarization adapter",
        pyproject=ROOT / "internal/transcription/adapters/py/pyannote/pyproject.toml",
        import_code="from pyannote.audio import Pipeline",
        requires_python=">=3.10,<3.13",
        package_ranges=(
            PackageRange("pyannote.audio", exact="4.0.2"),
        ),
    ),
    AdapterSpec(
        key="voxtral",
        label="Voxtral adapter",
        pyproject=ROOT / "internal/transcription/adapters/py/voxtral/pyproject.toml",
        import_code=textwrap.dedent(
            """
            import librosa
            import soundfile
            import torch
            import torchaudio
            import mistral_common
            from transformers import AutoProcessor, VoxtralForConditionalGeneration
            """
        ).strip(),
        requires_python=">=3.11,<3.13",
        package_ranges=(
            PackageRange("torch", min_inclusive="2.6", max_exclusive="2.8"),
            PackageRange("torchaudio", min_inclusive="2.6", max_exclusive="2.8"),
            PackageRange("transformers", min_inclusive="4.45.0"),
        ),
    ),
)


ADAPTERS_BY_KEY = {adapter.key: adapter for adapter in ADAPTERS}


class CheckError(Exception):
    pass


def canonical_name(name: str) -> str:
    return re.sub(r"[-_.]+", "-", name).lower()


def version_parts(version: str) -> tuple[int, ...]:
    match = re.match(r"^(\d+(?:\.\d+)*)", version)
    if match is None:
        raise CheckError(f"Cannot compare non-numeric version {version!r}")
    return tuple(int(part) for part in match.group(1).split("."))


def compare_versions(left: str, right: str) -> int:
    left_parts = version_parts(left)
    right_parts = version_parts(right)
    length = max(len(left_parts), len(right_parts))
    left_padded = left_parts + (0,) * (length - len(left_parts))
    right_padded = right_parts + (0,) * (length - len(right_parts))
    return (left_padded > right_padded) - (left_padded < right_padded)


def run(
    args: list[str],
    cwd: Path,
    timeout: int,
    verbose: bool,
    env: dict[str, str] | None = None,
) -> subprocess.CompletedProcess[str]:
    if verbose:
        print(f"+ {shlex.join(args)}", flush=True)
    try:
        completed = subprocess.run(
            args,
            cwd=cwd,
            env=env,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            timeout=timeout,
            check=False,
        )
    except subprocess.TimeoutExpired as exc:
        raise CheckError(f"Timed out after {timeout}s: {shlex.join(args)}") from exc

    if verbose and completed.stdout:
        print(completed.stdout, end="")
    if verbose and completed.stderr:
        print(completed.stderr, end="", file=sys.stderr)

    if completed.returncode != 0:
        output = "\n".join(part for part in (completed.stdout, completed.stderr) if part)
        raise CheckError(
            f"Command failed with exit code {completed.returncode}: {shlex.join(args)}\n"
            f"{output.strip()}"
        )
    return completed


def load_toml(path: Path) -> dict:
    return tomllib.loads(path.read_text())


def validate_pyproject(spec: AdapterSpec) -> None:
    pyproject = load_toml(spec.pyproject)
    project = pyproject.get("project", {})
    requires_python = project.get("requires-python")
    if requires_python != spec.requires_python:
        raise CheckError(
            f"{spec.pyproject} has requires-python={requires_python!r}; "
            f"expected {spec.requires_python!r}"
        )

    sources = pyproject.get("tool", {}).get("uv", {}).get("sources", {})
    for expected in spec.expected_sources:
        source = sources.get(expected.package)
        if not isinstance(source, dict):
            raise CheckError(f"{expected.package} source is not pinned in {spec.pyproject}")
        actual = source.get(expected.field)
        if actual != expected.value:
            raise CheckError(
                f"{expected.package} {expected.field}={actual!r}; expected {expected.value!r}"
            )


def copy_pyproject(spec: AdapterSpec, temp_root: Path, torch_index: str) -> Path:
    workdir = temp_root / spec.key
    workdir.mkdir(parents=True, exist_ok=True)
    pyproject_content = spec.pyproject.read_text()
    if torch_index == "cpu":
        pyproject_content = pyproject_content.replace(
            'url = "https://download.pytorch.org/whl/cu126"',
            'url = "https://download.pytorch.org/whl/cpu"',
            1,
        )
    (workdir / "pyproject.toml").write_text(pyproject_content)
    return workdir


def lock_environment(
    spec: AdapterSpec,
    workdir: Path,
    uv: str,
    python_version: str,
    timeout: int,
    verbose: bool,
) -> dict[str, list[str]]:
    run(
        [
            uv,
            "lock",
            "--project",
            str(workdir),
            "--python",
            python_version,
            "--system-certs",
        ],
        cwd=ROOT,
        timeout=timeout,
        verbose=verbose,
    )

    lock_path = workdir / "uv.lock"
    if not lock_path.exists():
        raise CheckError(f"uv did not write {lock_path}")

    lock_data = load_toml(lock_path)
    packages: dict[str, list[str]] = {}
    for package in lock_data.get("package", []):
        name = package.get("name")
        version = package.get("version")
        if name and version:
            packages.setdefault(canonical_name(name), []).append(version)

    check_package_ranges(spec, packages)
    return packages


def check_package_ranges(spec: AdapterSpec, packages: dict[str, list[str]]) -> None:
    for package_range in spec.package_ranges:
        key = canonical_name(package_range.name)
        versions = packages.get(key)
        if not versions:
            raise CheckError(f"{spec.key}: {package_range.name} was not present in uv.lock")

        for version in versions:
            if package_range.exact is not None and version != package_range.exact:
                raise CheckError(
                    f"{spec.key}: {package_range.name} resolved to {version}; "
                    f"expected {package_range.exact}"
                )
            if (
                package_range.min_inclusive is not None
                and compare_versions(version, package_range.min_inclusive) < 0
            ):
                raise CheckError(
                    f"{spec.key}: {package_range.name} resolved to {version}; "
                    f"expected >= {package_range.min_inclusive}"
                )
            if (
                package_range.max_exclusive is not None
                and compare_versions(version, package_range.max_exclusive) >= 0
            ):
                raise CheckError(
                    f"{spec.key}: {package_range.name} resolved to {version}; "
                    f"expected < {package_range.max_exclusive}"
                )

        unique_versions = ", ".join(sorted(set(versions)))
        print(f"    {package_range.name}: {unique_versions}")


def run_pair_import_check(
    spec: AdapterSpec,
    packages: dict[str, list[str]],
    uv: str,
    python_version: str,
    timeout: int,
    verbose: bool,
) -> None:
    if not spec.pair_import_packages or spec.pair_import_code is None:
        return

    cmd = [
        uv,
        "run",
        "--no-project",
        "--python",
        python_version,
        "--system-certs",
    ]
    for package in spec.pair_import_packages:
        versions = packages.get(canonical_name(package))
        if not versions:
            raise CheckError(f"{spec.key}: cannot import-check missing package {package}")
        if len(set(versions)) != 1:
            raise CheckError(f"{spec.key}: package {package} resolved multiple versions: {versions}")
        cmd.extend(["--with", f"{package}=={versions[0]}"])
    cmd.extend(["python", "-c", spec.pair_import_code])

    completed = run(cmd, cwd=ROOT, timeout=timeout, verbose=verbose)
    output = completed.stdout.strip()
    if output:
        print(f"    pair import: {output}")
    else:
        print("    pair import: ok")


def run_adapter_import_check(
    spec: AdapterSpec,
    workdir: Path,
    uv: str,
    python_version: str,
    timeout: int,
    verbose: bool,
) -> None:
    print(f"    import: {spec.import_code.splitlines()[-1]}")
    run(
        [
            uv,
            "run",
            "--project",
            str(workdir),
            "--python",
            python_version,
            "--locked",
            "--system-certs",
            "python",
            "-c",
            spec.import_code,
        ],
        cwd=ROOT,
        timeout=timeout,
        verbose=verbose,
    )


def selected_adapters(keys: list[str] | None) -> list[AdapterSpec]:
    if not keys:
        return list(ADAPTERS)
    return [ADAPTERS_BY_KEY[key] for key in keys]


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Validate Scriberr Python adapter resolver output and optional imports."
    )
    parser.add_argument(
        "--adapter",
        action="append",
        choices=sorted(ADAPTERS_BY_KEY),
        help="Adapter to check. Repeat to check multiple adapters. Defaults to all.",
    )
    parser.add_argument(
        "--mode",
        choices=("lock", "import"),
        default="lock",
        help="lock checks resolver output; import also materializes the adapter env and imports it.",
    )
    parser.add_argument(
        "--python",
        default="3.12",
        help="Python version uv should resolve against, for example 3.11 or 3.12.",
    )
    parser.add_argument("--uv", default="uv", help="uv executable to run.")
    parser.add_argument(
        "--torch-index",
        choices=("project", "cpu"),
        default="project",
        help="project keeps the adapter pyproject index; cpu rewrites the temp PyTorch index for import checks.",
    )
    parser.add_argument("--timeout", type=int, default=1800, help="Per-command timeout in seconds.")
    parser.add_argument("--verbose", action="store_true", help="Print full uv command output.")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    adapters = selected_adapters(args.adapter)
    failures: list[str] = []

    with tempfile.TemporaryDirectory(prefix="scriberr-adapter-envs-") as temp_dir:
        temp_root = Path(temp_dir)
        for spec in adapters:
            print(f"==> {spec.key} ({spec.label}) on Python {args.python}", flush=True)
            try:
                validate_pyproject(spec)
                workdir = copy_pyproject(spec, temp_root, args.torch_index)
                packages = lock_environment(
                    spec,
                    workdir,
                    args.uv,
                    args.python,
                    args.timeout,
                    args.verbose,
                )
                run_pair_import_check(
                    spec,
                    packages,
                    args.uv,
                    args.python,
                    args.timeout,
                    args.verbose,
                )
                if args.mode == "import":
                    run_adapter_import_check(
                        spec,
                        workdir,
                        args.uv,
                        args.python,
                        args.timeout,
                        args.verbose,
                    )
            except CheckError as exc:
                failures.append(f"{spec.key}: {exc}")
                print(f"    FAILED: {exc}", file=sys.stderr)

    if failures:
        print("\nPython adapter environment checks failed:", file=sys.stderr)
        for failure in failures:
            print(f"  - {failure}", file=sys.stderr)
        return 1

    print("\nPython adapter environment checks passed.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

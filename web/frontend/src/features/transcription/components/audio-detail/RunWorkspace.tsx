import { Activity, AlertCircle, Download, FileText, GitCompareArrows, Info, Loader2, MoreVertical, RefreshCw, ScrollText, Settings2, StopCircle } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
    DropdownMenu,
    DropdownMenuContent,
    DropdownMenuItem,
    DropdownMenuSeparator,
    DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
    Select,
    SelectContent,
    SelectItem,
    SelectTrigger,
    SelectValue,
} from "@/components/ui/select";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { cn } from "@/lib/utils";
import type { WhisperXParams } from "@/components/TranscriptionConfigDialog";
import type { ExecutionRun, Transcript } from "@/features/transcription/hooks/useAudioDetail";

type RunWorkspaceMode = "transcript" | "compare";
type DownloadFormat = "srt" | "txt" | "json";

interface RunWorkspaceProps {
    runs: ExecutionRun[];
    activeRunId?: string;
    selectedRunId?: string;
    compareRunId?: string;
    mode: RunWorkspaceMode;
    selectedTranscript?: Transcript | null;
    compareTranscript?: Transcript | null;
    selectedTranscriptLoading: boolean;
    compareTranscriptLoading: boolean;
    onSelectedRunChange: (runId: string) => void;
    onCompareRunChange: (runId: string) => void;
    onModeChange: (mode: RunWorkspaceMode) => void;
    onRunAgain: () => void;
    onStopRun?: () => void;
    runAgainDisabled?: boolean;
    canStopRun?: boolean;
    stoppingRun?: boolean;
    onOpenRunDetails: (runId?: string) => void;
    onOpenRunLogs: (runId?: string) => void;
    onDownloadRun: (run: ExecutionRun, format: DownloadFormat, transcript?: Transcript | null) => void;
}

export function RunWorkspace({
    runs,
    activeRunId,
    selectedRunId,
    compareRunId,
    mode,
    selectedTranscript,
    compareTranscript,
    selectedTranscriptLoading,
    compareTranscriptLoading,
    onSelectedRunChange,
    onCompareRunChange,
    onModeChange,
    onRunAgain,
    onStopRun,
    runAgainDisabled = false,
    canStopRun = false,
    stoppingRun = false,
    onOpenRunDetails,
    onOpenRunLogs,
    onDownloadRun,
}: RunWorkspaceProps) {
    const selectedRun = runs.find((run) => run.id === selectedRunId) || runs[0];
    const compareRun = runs.find((run) => run.id === compareRunId) || runs.find((run) => run.id !== selectedRun?.id);

    if (runs.length === 0) {
        return (
            <section className="glass-card rounded-[var(--radius-card)] border border-[var(--border-subtle)] shadow-[var(--shadow-card)] p-4 sm:p-5">
                <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
                    <div>
                        <h2 className="flex items-center gap-2 text-base font-bold text-[var(--text-primary)]">
                            <Activity className="h-4 w-4 text-[var(--brand-solid)]" />
                            Runs
                        </h2>
                        <p className="mt-1 text-sm text-[var(--text-secondary)]">
                            No run history exists for this file yet.
                        </p>
                    </div>
                    <RunControl
                        onRunAgain={onRunAgain}
                        onStopRun={onStopRun}
                        runAgainDisabled={runAgainDisabled}
                        canStopRun={canStopRun}
                        stoppingRun={stoppingRun}
                    />
                </div>
            </section>
        );
    }

    return (
        <section className="glass-card rounded-[var(--radius-card)] border border-[var(--border-subtle)] shadow-[var(--shadow-card)] p-4 sm:p-5">
            <div className="flex flex-col gap-4">
                <div className="flex flex-col gap-3 xl:flex-row xl:items-center xl:justify-between">
                    <div className="min-w-0">
                        <h2 className="flex items-center gap-2 text-base font-bold text-[var(--text-primary)]">
                            <Activity className="h-4 w-4 text-[var(--brand-solid)]" />
                            Runs
                            <span className="rounded-full bg-[var(--brand-light)] px-2 py-0.5 text-xs font-semibold text-[var(--brand-solid)]">
                                {runs.length}
                            </span>
                        </h2>
                        <p className="mt-1 text-sm text-[var(--text-secondary)]">
                            Select a run to drive the transcript below, or compare two runs side by side.
                        </p>
                    </div>

                    <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
                        <RunSelect
                            runs={runs}
                            activeRunId={activeRunId}
                            value={selectedRun?.id}
                            onValueChange={onSelectedRunChange}
                            label="Primary run"
                        />
                        <Tabs value={mode} onValueChange={(value) => onModeChange(value as RunWorkspaceMode)}>
                            <TabsList className="h-9 bg-[var(--bg-main)] border border-[var(--border-subtle)]">
                                <TabsTrigger value="transcript" className="gap-2">
                                    <ScrollText className="h-4 w-4" />
                                    Transcript
                                </TabsTrigger>
                                <TabsTrigger value="compare" className="gap-2" disabled={runs.length < 2}>
                                    <GitCompareArrows className="h-4 w-4" />
                                    Compare
                                </TabsTrigger>
                            </TabsList>
                        </Tabs>
                        <RunControl
                            onRunAgain={onRunAgain}
                            onStopRun={onStopRun}
                            runAgainDisabled={runAgainDisabled}
                            canStopRun={canStopRun}
                            stoppingRun={stoppingRun}
                        />
                    </div>
                </div>

                {mode === "compare" && compareRun ? (
                    <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
                        <ComparePanel
                            title="Primary"
                            runs={runs}
                            activeRunId={activeRunId}
                            run={selectedRun}
                            transcript={selectedTranscript}
                            loading={selectedTranscriptLoading}
                            selectedRunId={selectedRun?.id}
                            onRunChange={onSelectedRunChange}
                            onOpenRunDetails={onOpenRunDetails}
                            onOpenRunLogs={onOpenRunLogs}
                            onDownloadRun={onDownloadRun}
                        />
                        <ComparePanel
                            title="Compare"
                            runs={runs.filter((run) => run.id !== selectedRun?.id)}
                            activeRunId={activeRunId}
                            run={compareRun}
                            transcript={compareTranscript}
                            loading={compareTranscriptLoading}
                            selectedRunId={compareRun.id}
                            onRunChange={onCompareRunChange}
                            onOpenRunDetails={onOpenRunDetails}
                            onOpenRunLogs={onOpenRunLogs}
                            onDownloadRun={onDownloadRun}
                        />
                    </div>
                ) : (
                    <SelectedRunPanel
                        run={selectedRun}
                        active={selectedRun?.id === activeRunId}
                        transcript={selectedTranscript}
                        transcriptLoading={selectedTranscriptLoading}
                        onOpenRunDetails={onOpenRunDetails}
                        onOpenRunLogs={onOpenRunLogs}
                        onDownloadRun={onDownloadRun}
                    />
                )}
            </div>
        </section>
    );
}

function RunControl({
    onRunAgain,
    onStopRun,
    runAgainDisabled,
    canStopRun,
    stoppingRun,
}: {
    onRunAgain: () => void;
    onStopRun?: () => void;
    runAgainDisabled: boolean;
    canStopRun: boolean;
    stoppingRun: boolean;
}) {
    if (canStopRun && onStopRun) {
        return (
            <Button
                variant="outline"
                size="sm"
                onClick={onStopRun}
                disabled={stoppingRun}
                className="gap-2 rounded-full border-red-500/30 bg-red-500/10 text-red-600 hover:bg-red-500/15 hover:text-red-700 dark:text-red-300 dark:hover:text-red-200"
            >
                {stoppingRun ? (
                    <Loader2 className="h-4 w-4 animate-spin" />
                ) : (
                    <StopCircle className="h-4 w-4" />
                )}
                Stop Run
            </Button>
        );
    }

    return (
        <Button
            variant="outline"
            size="sm"
            onClick={onRunAgain}
            disabled={runAgainDisabled}
            className="gap-2 rounded-full border-[var(--border-subtle)] bg-[var(--bg-card)]"
        >
            <RefreshCw className="h-4 w-4" />
            Run Again
        </Button>
    );
}

function SelectedRunPanel({
    run,
    active,
    transcript,
    transcriptLoading,
    onOpenRunDetails,
    onOpenRunLogs,
    onDownloadRun,
}: {
    run?: ExecutionRun;
    active: boolean;
    transcript?: Transcript | null;
    transcriptLoading: boolean;
    onOpenRunDetails: (runId?: string) => void;
    onOpenRunLogs: (runId?: string) => void;
    onDownloadRun: (run: ExecutionRun, format: DownloadFormat, transcript?: Transcript | null) => void;
}) {
    if (!run) return null;
    const params = (run.actual_parameters || {}) as Partial<WhisperXParams>;

    return (
        <div className="grid grid-cols-1 gap-4 xl:grid-cols-[minmax(0,1fr)_320px]">
            <div className="space-y-4">
                <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
                    <div>
                        <div className="flex flex-wrap items-center gap-2">
                            <h3 className="text-xl font-bold text-[var(--text-primary)]">Run {run.run_number}</h3>
                            {active && <ActiveBadge />}
                            <StatusPill status={run.status || "unknown"} />
                        </div>
                        <p className="mt-1 text-sm text-[var(--text-secondary)]">
                            {modelLabel(params.model_family, params.model)}
                        </p>
                    </div>
                    <RunActions
                        run={run}
                        transcript={transcript}
                        transcriptLoading={transcriptLoading}
                        onOpenRunDetails={onOpenRunDetails}
                        onOpenRunLogs={onOpenRunLogs}
                        onDownloadRun={onDownloadRun}
                    />
                </div>

                {run.error_message && (
                    <div className="flex gap-2 rounded-[var(--radius-card)] border border-red-500/30 bg-red-500/10 p-3 text-sm text-red-600 dark:text-red-300">
                        <AlertCircle className="mt-0.5 h-4 w-4 shrink-0" />
                        <span>{run.error_message}</span>
                    </div>
                )}

                <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
                    <Metric label="Started" value={run.started_at ? formatDateTime(run.started_at) : "Unknown"} />
                    <Metric label="Duration" value={formatDuration(run.processing_duration)} />
                    <Metric label="Device" value={params.device || "auto"} />
                    <Metric label="Batch" value={String(params.batch_size ?? "N/A")} />
                </div>
            </div>

            <div className="space-y-2 text-sm">
                <h4 className="flex items-center gap-2 text-sm font-semibold text-[var(--text-primary)]">
                    <Settings2 className="h-4 w-4 text-[var(--text-secondary)]" />
                    Settings Snapshot
                </h4>
                <div className="space-y-1.5">
                    {settingsRows(params).map((row) => (
                        <div key={row.label} className="flex items-start justify-between gap-3 border-b border-[var(--border-subtle)] pb-1.5 last:border-0">
                            <span className="text-[var(--text-secondary)]">{row.label}</span>
                            <span className="text-right font-mono text-xs text-[var(--text-primary)] break-all">{row.value}</span>
                        </div>
                    ))}
                </div>
            </div>
        </div>
    );
}

function ComparePanel({
    title,
    runs,
    activeRunId,
    run,
    transcript,
    loading,
    selectedRunId,
    onRunChange,
    onOpenRunDetails,
    onOpenRunLogs,
    onDownloadRun,
}: {
    title: string;
    runs: ExecutionRun[];
    activeRunId?: string;
    run?: ExecutionRun;
    transcript?: Transcript | null;
    loading: boolean;
    selectedRunId?: string;
    onRunChange: (runId: string) => void;
    onOpenRunDetails: (runId?: string) => void;
    onOpenRunLogs: (runId?: string) => void;
    onDownloadRun: (run: ExecutionRun, format: DownloadFormat, transcript?: Transcript | null) => void;
}) {
    if (!run) {
        return (
            <div className="rounded-[var(--radius-card)] border border-[var(--border-subtle)] bg-[var(--bg-main)]/60 p-4 text-sm text-[var(--text-tertiary)]">
                No second run available to compare.
            </div>
        );
    }

    const params = (run.actual_parameters || {}) as Partial<WhisperXParams>;

    return (
        <div className="rounded-[var(--radius-card)] border border-[var(--border-subtle)] bg-[var(--bg-main)]/50 p-4">
            <div className="mb-3 flex items-start justify-between gap-3">
                <div className="min-w-0 flex-1">
                    <span className="text-xs font-semibold uppercase tracking-wide text-[var(--text-tertiary)]">{title}</span>
                    <RunSelect
                        runs={runs}
                        activeRunId={activeRunId}
                        value={selectedRunId}
                        onValueChange={onRunChange}
                        label={`${title} run`}
                        compact
                    />
                    <div className="mt-2 flex flex-wrap items-center gap-2">
                        <StatusPill status={run.status || "unknown"} />
                        <span className="text-xs text-[var(--text-secondary)]">{modelLabel(params.model_family, params.model)}</span>
                    </div>
                </div>
                <RunActions
                    run={run}
                    transcript={transcript}
                    transcriptLoading={loading}
                    onOpenRunDetails={onOpenRunDetails}
                    onOpenRunLogs={onOpenRunLogs}
                    onDownloadRun={onDownloadRun}
                    compact
                />
            </div>
            <TranscriptPreview transcript={transcript} loading={loading} />
        </div>
    );
}

function TranscriptPreview({ transcript, loading }: { transcript?: Transcript | null; loading: boolean }) {
    if (loading) {
        return <div className="rounded-[var(--radius-card)] bg-[var(--bg-card)] p-4 text-sm text-[var(--text-secondary)]">Loading transcript...</div>;
    }
    if (!transcript) {
        return <div className="rounded-[var(--radius-card)] bg-[var(--bg-card)] p-4 text-sm text-[var(--text-tertiary)]">No transcript captured for this run.</div>;
    }
    return (
        <div className="max-h-[520px] overflow-y-auto rounded-[var(--radius-card)] bg-[var(--bg-card)] p-4 text-sm leading-7 text-[var(--text-primary)] whitespace-pre-wrap">
            {transcript.text || "Transcript is empty."}
        </div>
    );
}

function RunActions({
    run,
    transcript,
    transcriptLoading,
    onOpenRunDetails,
    onOpenRunLogs,
    onDownloadRun,
    compact = false,
}: {
    run: ExecutionRun;
    transcript?: Transcript | null;
    transcriptLoading: boolean;
    onOpenRunDetails: (runId?: string) => void;
    onOpenRunLogs: (runId?: string) => void;
    onDownloadRun: (run: ExecutionRun, format: DownloadFormat, transcript?: Transcript | null) => void;
    compact?: boolean;
}) {
    const downloadsDisabled = transcriptLoading || !transcript;

    return (
        <DropdownMenu>
            <DropdownMenuTrigger asChild>
                <Button
                    variant="outline"
                    size={compact ? "icon" : "sm"}
                    className={cn(
                        "border-[var(--border-subtle)] bg-[var(--bg-card)]",
                        compact ? "h-8 w-8 rounded-full" : "gap-2 rounded-full"
                    )}
                >
                    <MoreVertical className="h-4 w-4" />
                    {!compact && <span>Run Actions</span>}
                </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="w-52 glass-card rounded-[var(--radius-card)] border-[var(--border-subtle)] p-1.5 shadow-[var(--shadow-float)]">
                <DropdownMenuItem onClick={() => onOpenRunDetails(run.id)} className="rounded-[8px] cursor-pointer">
                    <Info className="mr-2 h-4 w-4 opacity-70" />
                    Execution Info
                </DropdownMenuItem>
                <DropdownMenuItem onClick={() => onOpenRunLogs(run.id)} disabled={!run.has_logs} className="rounded-[8px] cursor-pointer">
                    <FileText className="mr-2 h-4 w-4 opacity-70" />
                    Logs
                </DropdownMenuItem>
                <DropdownMenuSeparator className="bg-[var(--border-subtle)] my-1" />
                <DropdownMenuItem onClick={() => onDownloadRun(run, "srt", transcript)} disabled={downloadsDisabled} className="rounded-[8px] cursor-pointer">
                    <Download className="mr-2 h-4 w-4 opacity-70" />
                    Download SRT
                </DropdownMenuItem>
                <DropdownMenuItem onClick={() => onDownloadRun(run, "txt", transcript)} disabled={downloadsDisabled} className="rounded-[8px] cursor-pointer">
                    <Download className="mr-2 h-4 w-4 opacity-70" />
                    Download Text
                </DropdownMenuItem>
                <DropdownMenuItem onClick={() => onDownloadRun(run, "json", transcript)} disabled={downloadsDisabled} className="rounded-[8px] cursor-pointer">
                    <Download className="mr-2 h-4 w-4 opacity-70" />
                    Download JSON
                </DropdownMenuItem>
            </DropdownMenuContent>
        </DropdownMenu>
    );
}

function RunSelect({
    runs,
    activeRunId,
    value,
    onValueChange,
    label,
    compact = false,
}: {
    runs: ExecutionRun[];
    activeRunId?: string;
    value?: string;
    onValueChange: (runId: string) => void;
    label: string;
    compact?: boolean;
}) {
    return (
        <Select value={value} onValueChange={onValueChange}>
            <SelectTrigger
                aria-label={label}
                className={cn(
                    "border-[var(--border-subtle)] bg-[var(--bg-card)] text-[var(--text-primary)]",
                    compact ? "mt-1 w-full" : "w-full sm:w-[300px]"
                )}
            >
                <SelectValue placeholder="Select run" />
            </SelectTrigger>
            <SelectContent className="glass-card border-[var(--border-subtle)]">
                {runs.map((run) => {
                    const params = (run.actual_parameters || {}) as Partial<WhisperXParams>;
                    return (
                        <SelectItem key={run.id} value={run.id}>
                            Run {run.run_number} · {modelLabel(params.model_family, params.model)}
                            {run.id === activeRunId ? " · Active" : ""}
                        </SelectItem>
                    );
                })}
            </SelectContent>
        </Select>
    );
}

function Metric({ label, value }: { label: string; value: string }) {
    return (
        <div className="rounded-[var(--radius-card)] bg-[var(--bg-main)]/70 p-3">
            <span className="block text-[10px] font-semibold uppercase tracking-wide text-[var(--text-tertiary)]">{label}</span>
            <span className="mt-1 block truncate font-mono text-sm text-[var(--text-primary)]">{value}</span>
        </div>
    );
}

function StatusPill({ status }: { status: string }) {
    const normalized = status.toLowerCase();
    return (
        <span
            className={cn(
                "inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-[10px] font-semibold uppercase",
                normalized === "completed" && "bg-emerald-500/10 text-emerald-600 dark:text-emerald-300",
                normalized === "failed" && "bg-red-500/10 text-red-600 dark:text-red-300",
                normalized !== "completed" && normalized !== "failed" && "bg-amber-500/10 text-amber-600 dark:text-amber-300"
            )}
        >
            {status}
        </span>
    );
}

function ActiveBadge() {
    return (
        <span className="rounded-full bg-[var(--brand-light)] px-2 py-0.5 text-[10px] font-semibold uppercase text-[var(--brand-solid)]">
            Active
        </span>
    );
}

function settingsRows(params: Partial<WhisperXParams>) {
    return [
        { label: "Task", value: params.task || "transcribe" },
        { label: "Language", value: params.language || "auto" },
        { label: "Precision", value: params.model_family?.startsWith("nvidia_") ? params.nvidia_precision || "default" : params.compute_type || "default" },
        { label: "Timestamps", value: params.nvidia_timestamps === false ? "No" : "Yes" },
        { label: "Chunking", value: params.nvidia_use_chunking === undefined ? "Default" : params.nvidia_use_chunking ? "Yes" : "No" },
        { label: "Chunk Duration", value: params.nvidia_chunk_duration ? `${params.nvidia_chunk_duration}s` : "Default" },
    ].filter((row) => row.value !== "Default" || row.label === "Chunking" || row.label === "Chunk Duration");
}

function modelLabel(modelFamily?: string, model?: string) {
    if (modelFamily === "nvidia_canary") return "NVIDIA Canary 1B";
    if (modelFamily === "nvidia_canary_qwen") return "NVIDIA Canary-Qwen 2.5B";
    if (modelFamily === "nvidia_parakeet") return "NVIDIA Parakeet";
    if (modelFamily === "openai") return `OpenAI ${model || "Whisper"}`;
    if (modelFamily === "whisper") return `Whisper ${model || ""}`.trim();
    return modelFamily || "Transcription";
}

function formatDuration(value?: number | null) {
    if (!value || value <= 0) return "...";
    const seconds = Math.round(value / 1000);
    if (seconds < 60) return `${seconds}s`;
    const minutes = Math.floor(seconds / 60);
    const remainingSeconds = seconds % 60;
    if (minutes < 60) return `${minutes}m ${remainingSeconds}s`;
    const hours = Math.floor(minutes / 60);
    const remainingMinutes = minutes % 60;
    return `${hours}h ${remainingMinutes}m`;
}

function formatDateTime(value: string) {
    const date = new Date(value);
    return `${date.toLocaleDateString()} ${date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}`;
}

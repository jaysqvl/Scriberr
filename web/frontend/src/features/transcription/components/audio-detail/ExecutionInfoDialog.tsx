import { useEffect, useMemo, useState, type ReactNode } from "react";
import {
    Dialog,
    DialogContent,
    DialogHeader,
    DialogTitle,
    DialogDescription,
} from "@/components/ui/dialog";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import {
    CheckCircle2,
    FileText,
    Info,
    Loader2,
    ScrollText,
    SlidersHorizontal,
    UsersRound,
    XCircle,
} from "lucide-react";
import {
    useExecutionRuns,
    useRunLogs,
    useRunTranscript,
    type ExecutionRun,
    type MultiTrackTiming,
} from "@/features/transcription/hooks/useAudioDetail";
import type { WhisperXParams } from "@/components/TranscriptionConfigDialog";
import { cn } from "@/lib/utils";

interface ExecutionInfoDialogProps {
    audioId: string;
    isOpen: boolean;
    onClose: (open: boolean) => void;
}

export function ExecutionInfoDialog({ audioId, isOpen, onClose }: ExecutionInfoDialogProps) {
    const { data: runsData, isLoading } = useExecutionRuns(audioId, isOpen);
    const runs = useMemo(() => runsData?.runs || [], [runsData?.runs]);
    const [selectedRunId, setSelectedRunId] = useState<string | undefined>();

    useEffect(() => {
        if (!isOpen || runs.length === 0) return;
        const selectedStillExists = selectedRunId && runs.some((run) => run.id === selectedRunId);
        if (!selectedStillExists) {
            setSelectedRunId(runsData?.active_run_id || runs[0].id);
        }
    }, [isOpen, runs, runsData?.active_run_id, selectedRunId]);

    const selectedRun = useMemo(
        () => runs.find((run) => run.id === selectedRunId) || runs[0],
        [runs, selectedRunId]
    );
    const { data: transcript, isLoading: transcriptLoading } = useRunTranscript(audioId, selectedRun?.id, isOpen);
    const { data: logs, isLoading: logsLoading } = useRunLogs(audioId, selectedRun?.id, isOpen);

    return (
        <Dialog open={isOpen} onOpenChange={onClose}>
            <DialogContent className="sm:max-w-6xl w-[95vw] bg-[var(--bg-card)] border-[var(--border-subtle)] shadow-[var(--shadow-float)] max-h-[92vh] overflow-hidden p-0 gap-0">
                <DialogHeader className="border-b border-[var(--border-subtle)] px-5 sm:px-6 py-5">
                    <DialogTitle className="text-[var(--text-primary)] flex items-center gap-2 text-xl font-bold tracking-tight">
                        <Info className="h-5 w-5 text-[var(--brand-solid)]" />
                        Runs
                    </DialogTitle>
                    <DialogDescription className="text-[var(--text-secondary)]">
                        Compare every transcription attempt, including settings, timing, transcripts, and logs.
                    </DialogDescription>
                </DialogHeader>

                {isLoading ? (
                    <div className="py-16 flex flex-col items-center justify-center gap-4">
                        <Loader2 className="h-8 w-8 animate-spin text-[var(--brand-solid)]" />
                        <span className="text-[var(--text-tertiary)]">Loading runs...</span>
                    </div>
                ) : runs.length === 0 ? (
                    <div className="py-16 text-center text-[var(--text-tertiary)]">
                        No runs have been recorded for this file yet.
                    </div>
                ) : (
                    <div className="grid grid-cols-1 lg:grid-cols-[320px_1fr] min-h-[620px] max-h-[calc(92vh-104px)]">
                        <aside className="border-b lg:border-b-0 lg:border-r border-[var(--border-subtle)] bg-[var(--bg-main)]/60 overflow-y-auto p-3">
                            <div className="space-y-2">
                                {runs.map((run) => (
                                    <RunCard
                                        key={run.id}
                                        run={run}
                                        selected={run.id === selectedRun?.id}
                                        active={run.id === runsData?.active_run_id}
                                        onClick={() => setSelectedRunId(run.id)}
                                    />
                                ))}
                            </div>
                        </aside>

                        <section className="overflow-y-auto p-4 sm:p-6">
                            {selectedRun && (
                                <RunDetails
                                    run={selectedRun}
                                    transcript={transcript}
                                    transcriptLoading={transcriptLoading}
                                    logs={logs?.content || ""}
                                    logsAvailable={logs?.available ?? false}
                                    logsLoading={logsLoading}
                                />
                            )}
                        </section>
                    </div>
                )}
            </DialogContent>
        </Dialog>
    );
}

function RunCard({
    run,
    selected,
    active,
    onClick,
}: {
    run: ExecutionRun;
    selected: boolean;
    active: boolean;
    onClick: () => void;
}) {
    const params = (run.actual_parameters || {}) as Partial<WhisperXParams>;
    const chips = runChips(run);

    return (
        <button
            type="button"
            onClick={onClick}
            className={cn(
                "w-full text-left rounded-[var(--radius-card)] border p-3 transition-colors",
                selected
                    ? "bg-[var(--bg-card)] border-[var(--brand-solid)] shadow-sm"
                    : "bg-[var(--bg-card)]/70 border-[var(--border-subtle)] hover:border-[var(--border-strong)]"
            )}
        >
            <div className="flex items-start justify-between gap-3">
                <div className="min-w-0">
                    <div className="flex items-center gap-2">
                        <span className="font-semibold text-[var(--text-primary)]">Run {run.run_number}</span>
                        {active && (
                            <span className="rounded-full bg-[var(--brand-light)] px-2 py-0.5 text-[10px] font-semibold uppercase text-[var(--brand-solid)]">
                                Active
                            </span>
                        )}
                    </div>
                    <p className="mt-1 truncate text-xs text-[var(--text-secondary)]">
                        {modelLabel(params.model_family, params.model)}
                    </p>
                </div>
                <StatusPill status={run.status || "unknown"} />
            </div>

            <div className="mt-3 flex flex-wrap gap-1.5">
                {chips.map((chip) => (
                    <span key={chip} className="rounded-md bg-[var(--bg-main)] px-1.5 py-0.5 text-[10px] text-[var(--text-secondary)]">
                        {chip}
                    </span>
                ))}
            </div>

            <div className="mt-3 flex items-center justify-between text-[11px] text-[var(--text-tertiary)]">
                <span>{run.started_at ? formatDateTime(run.started_at) : "Unknown start"}</span>
                <span className="font-mono">{formatDuration(run.processing_duration)}</span>
            </div>
        </button>
    );
}

function RunDetails({
    run,
    transcript,
    transcriptLoading,
    logs,
    logsAvailable,
    logsLoading,
}: {
    run: ExecutionRun;
    transcript?: { text: string } | null;
    transcriptLoading: boolean;
    logs: string;
    logsAvailable: boolean;
    logsLoading: boolean;
}) {
    const params = (run.actual_parameters || {}) as Partial<WhisperXParams>;

    return (
        <div className="space-y-5">
            <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
                <div>
                    <div className="flex flex-wrap items-center gap-2">
                        <h3 className="text-lg font-bold text-[var(--text-primary)]">Run {run.run_number}</h3>
                        <StatusPill status={run.status || "unknown"} />
                    </div>
                    <p className="mt-1 text-sm text-[var(--text-secondary)]">
                        {modelLabel(params.model_family, params.model)}
                    </p>
                </div>
                <div className="flex flex-wrap gap-2">
                    {run.has_transcript && <SmallFlag icon={<ScrollText className="h-3.5 w-3.5" />} label="Transcript" />}
                    {run.has_logs && <SmallFlag icon={<FileText className="h-3.5 w-3.5" />} label="Logs" />}
                </div>
            </div>

            <div className="grid grid-cols-2 xl:grid-cols-4 gap-3">
                <MetricCard
                    label="Started"
                    value={run.started_at ? new Date(run.started_at).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }) : "N/A"}
                    subtext={run.started_at ? new Date(run.started_at).toLocaleDateString() : ""}
                />
                <MetricCard
                    label="Completed"
                    value={run.completed_at ? new Date(run.completed_at).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" }) : "In Progress"}
                    subtext={run.completed_at ? new Date(run.completed_at).toLocaleDateString() : ""}
                />
                <MetricCard label="Duration" value={formatDuration(run.processing_duration)} highlight />
                <MetricCard label="Batch" value={String(params.batch_size ?? "N/A")} />
            </div>

            {run.error_message && (
                <div className="rounded-[var(--radius-card)] border border-red-500/30 bg-red-500/10 p-3 text-sm text-red-600 dark:text-red-300">
                    {run.error_message}
                </div>
            )}

            <Tabs defaultValue="settings" className="space-y-4">
                <TabsList className="w-full sm:w-fit bg-[var(--bg-main)] border border-[var(--border-subtle)]">
                    <TabsTrigger value="settings" className="gap-2">
                        <SlidersHorizontal className="h-4 w-4" />
                        Settings
                    </TabsTrigger>
                    <TabsTrigger value="transcript" className="gap-2">
                        <ScrollText className="h-4 w-4" />
                        Transcript
                    </TabsTrigger>
                    <TabsTrigger value="logs" className="gap-2">
                        <FileText className="h-4 w-4" />
                        Logs
                    </TabsTrigger>
                </TabsList>

                <TabsContent value="settings">
                    <Panel title="Configuration Parameters">
                        <CuratedParamsDisplay params={params} />
                    </Panel>
                    {run.is_multi_track && run.multi_track_timings && run.multi_track_timings.length > 0 && (
                        <Panel title="Track Processing" icon={<UsersRound className="h-4 w-4 text-[var(--text-secondary)]" />}>
                            <TrackTimings timings={run.multi_track_timings} />
                        </Panel>
                    )}
                </TabsContent>

                <TabsContent value="transcript">
                    <Panel title="Transcript Snapshot">
                        {transcriptLoading ? (
                            <LoadingLine label="Loading transcript..." />
                        ) : transcript ? (
                            <div className="max-h-[420px] overflow-y-auto rounded-[var(--radius-card)] bg-[var(--bg-main)] p-4 text-sm leading-7 text-[var(--text-primary)] whitespace-pre-wrap">
                                {transcript.text || "Transcript is empty."}
                            </div>
                        ) : (
                            <EmptyLine label="No transcript captured for this run." />
                        )}
                    </Panel>
                </TabsContent>

                <TabsContent value="logs">
                    <Panel title="Run Logs">
                        {logsLoading ? (
                            <LoadingLine label="Loading logs..." />
                        ) : logsAvailable ? (
                            <pre className="max-h-[420px] overflow-auto rounded-[var(--radius-card)] bg-black/90 p-4 text-xs leading-5 text-zinc-100 whitespace-pre-wrap">
                                {logs}
                            </pre>
                        ) : (
                            <EmptyLine label="No logs available for this run." />
                        )}
                    </Panel>
                </TabsContent>
            </Tabs>
        </div>
    );
}

function Panel({ title, icon, children }: { title: string; icon?: ReactNode; children: ReactNode }) {
    return (
        <div className="rounded-[var(--radius-card)] border border-[var(--border-subtle)] bg-[var(--bg-card)] p-4 sm:p-5 shadow-sm">
            <h3 className="mb-4 flex items-center gap-2 text-base font-bold text-[var(--text-primary)]">
                {icon}
                {title}
            </h3>
            {children}
        </div>
    );
}

function MetricCard({ label, value, subtext, highlight = false }: { label: string; value: string; subtext?: string; highlight?: boolean }) {
    return (
        <div className="bg-[var(--bg-main)] p-3 rounded-[var(--radius-card)] border border-[var(--border-subtle)] flex flex-col justify-center min-h-[74px]">
            <span className="block text-[10px] sm:text-xs font-medium text-[var(--text-tertiary)] uppercase tracking-wider mb-1">{label}</span>
            <span className={cn("block font-mono text-sm sm:text-base", highlight ? "text-[var(--brand-solid)] font-bold" : "text-[var(--text-primary)]")}>
                {value}
            </span>
            {subtext && <span className="block text-[10px] text-[var(--text-secondary)] mt-0.5">{subtext}</span>}
        </div>
    );
}

function StatusPill({ status }: { status: string }) {
    const normalized = status.toLowerCase();
    const Icon = normalized === "completed" ? CheckCircle2 : normalized === "failed" ? XCircle : Loader2;
    return (
        <span
            className={cn(
                "inline-flex items-center gap-1 rounded-full px-2 py-0.5 text-[10px] font-semibold uppercase",
                normalized === "completed" && "bg-emerald-500/10 text-emerald-600 dark:text-emerald-300",
                normalized === "failed" && "bg-red-500/10 text-red-600 dark:text-red-300",
                normalized !== "completed" && normalized !== "failed" && "bg-amber-500/10 text-amber-600 dark:text-amber-300"
            )}
        >
            <Icon className={cn("h-3 w-3", normalized === "processing" && "animate-spin")} />
            {status}
        </span>
    );
}

function SmallFlag({ icon, label }: { icon: ReactNode; label: string }) {
    return (
        <span className="inline-flex items-center gap-1.5 rounded-full border border-[var(--border-subtle)] bg-[var(--bg-main)] px-2.5 py-1 text-xs text-[var(--text-secondary)]">
            {icon}
            {label}
        </span>
    );
}

function LoadingLine({ label }: { label: string }) {
    return (
        <div className="flex items-center gap-2 rounded-[var(--radius-card)] bg-[var(--bg-main)] p-4 text-sm text-[var(--text-secondary)]">
            <Loader2 className="h-4 w-4 animate-spin" />
            {label}
        </div>
    );
}

function EmptyLine({ label }: { label: string }) {
    return (
        <div className="rounded-[var(--radius-card)] bg-[var(--bg-main)] p-4 text-sm text-[var(--text-tertiary)]">
            {label}
        </div>
    );
}

function TrackTimings({ timings }: { timings: MultiTrackTiming[] }) {
    return (
        <div className="space-y-3">
            {timings.map((timing, index) => (
                <div key={`${timing.track_name}-${index}`} className="flex flex-col gap-2 p-3 bg-[var(--bg-main)] rounded-[var(--radius-card)] border border-[var(--border-subtle)]">
                    <div className="flex justify-between items-start gap-2">
                        <span className="font-medium text-[var(--text-primary)] text-sm break-all leading-tight">{timing.track_name}</span>
                        <span className="font-mono text-sm font-bold text-[var(--brand-solid)] flex-shrink-0">
                            {formatDuration(timing.duration)}
                        </span>
                    </div>
                    <div className="flex justify-between text-[11px] text-[var(--text-tertiary)] bg-[var(--bg-card)]/60 p-1.5 rounded-[var(--radius-sm)]">
                        <span>{new Date(timing.start_time).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false })}</span>
                        <span>to</span>
                        <span>{new Date(timing.end_time).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false })}</span>
                    </div>
                </div>
            ))}
        </div>
    );
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
function CuratedParamsDisplay({ params }: { params: any }) {
    const commonKeys = [
        "model_family",
        "task",
        "language",
        "output_format",
        "device",
        "batch_size",
        "diarize",
    ];

    let specificKeys: string[] = [];

    if (params.model_family === "whisper") {
        specificKeys = [
            "model",
            "compute_type",
            "no_align",
            "vad_method",
            ...(params.diarize ? ["diarize_model", "min_speakers", "max_speakers", "hf_token"] : []),
        ];
    } else if (params.model_family === "nvidia_parakeet") {
        specificKeys = [
            "attention_context_left",
            "attention_context_right",
            "nvidia_chunk_duration",
            "nvidia_timestamps",
            "nvidia_precision",
            ...(params.diarize ? ["diarize_model"] : []),
        ];
    } else if (params.model_family === "openai") {
        specificKeys = ["model", "api_key"];
    } else if (params.model_family === "nvidia_canary") {
        specificKeys = [
            "nvidia_target_language",
            "nvidia_timestamps",
            "nvidia_use_chunking",
            "nvidia_chunk_duration",
            "nvidia_precision",
            ...(params.diarize ? ["diarize_model"] : []),
        ];
    } else if (params.model_family === "nvidia_canary_qwen") {
        specificKeys = [
            "nvidia_chunk_duration",
            "nvidia_timestamps",
            "nvidia_precision",
            "max_new_tokens",
            "nvidia_prompt",
            ...(params.diarize ? ["diarize_model"] : []),
        ];
    }

    const entries = [...commonKeys, ...specificKeys]
        .map((key) => {
            let value = params[key];
            if (value === undefined || value === null) return null;
            if (typeof value === "boolean") value = value ? "Yes" : "No";
            if (key === "hf_token" || key === "api_key") value = "******";
            return { key: formatParamKey(key), value: String(value) };
        })
        .filter((entry): entry is { key: string; value: string } => entry !== null);

    return (
        <div className="grid grid-cols-1 sm:grid-cols-2 gap-x-4 gap-y-2 text-sm">
            {entries.map((entry) => (
                <div key={entry.key} className="flex justify-between items-center gap-4 py-1 border-b border-[var(--border-subtle)] last:border-0 sm:last:border-b">
                    <span className="text-[var(--text-secondary)]">{entry.key}</span>
                    <span className="font-mono text-[var(--text-primary)] font-medium text-xs break-all text-right">{entry.value}</span>
                </div>
            ))}
        </div>
    );
}

function runChips(run: ExecutionRun): string[] {
    const params = (run.actual_parameters || {}) as Partial<WhisperXParams>;
    const modelFamily = params.model_family || "";
    const precision = modelFamily.startsWith("nvidia_") ? params.nvidia_precision : params.compute_type;
    const chips = [
        params.device || "auto",
        precision,
        `batch ${params.batch_size ?? 1}`,
    ].filter(Boolean).map(String);

    if (modelFamily === "nvidia_canary") {
        chips.push(params.nvidia_use_chunking ? "chunked" : "native");
        chips.push(params.nvidia_timestamps === false ? "no timestamps" : "timestamps");
    } else if (modelFamily === "nvidia_canary_qwen") {
        chips.push(`${params.nvidia_chunk_duration || 40}s chunks`);
        chips.push(params.nvidia_timestamps === false ? "no timestamps" : "chunk timestamps");
    }

    return chips;
}

function modelLabel(modelFamily?: string, model?: string) {
    if (modelFamily === "nvidia_canary") return "NVIDIA Canary 1B";
    if (modelFamily === "nvidia_canary_qwen") return "NVIDIA Canary-Qwen 2.5B";
    if (modelFamily === "nvidia_parakeet") return "NVIDIA Parakeet";
    if (modelFamily === "openai") return `OpenAI ${model || "Whisper"}`;
    if (modelFamily === "whisper") return `Whisper ${model || ""}`.trim();
    return modelFamily || "Transcription";
}

function formatParamKey(key: string): string {
    return key.split("_").map((word) => word.charAt(0).toUpperCase() + word.slice(1)).join(" ");
}

function formatDateTime(value: string) {
    const date = new Date(value);
    return `${date.toLocaleDateString()} ${date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}`;
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

import { useRef } from "react";
import {
    ArrowDown,
    ArrowUp,
    Clock3,
    Layers3,
    ListOrdered,
    Loader2,
    Plus,
    Square,
    Trash2,
    X,
} from "lucide-react";
import { Button } from "@/components/ui/button";
import {
    AlertDialog,
    AlertDialogAction,
    AlertDialogCancel,
    AlertDialogContent,
    AlertDialogDescription,
    AlertDialogFooter,
    AlertDialogHeader,
    AlertDialogTitle,
    AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { cn } from "@/lib/utils";
import type { ExecutionRun } from "@/features/transcription/hooks/useAudioDetail";
import type { TranscriptionQueueItem } from "@/features/transcription/hooks/transcriptionQueue";
import type { WhisperXParams } from "@/components/TranscriptionConfigDialog";

interface RunQueuePanelProps {
    items: TranscriptionQueueItem[];
    activeItem?: TranscriptionQueueItem | null;
    currentRun?: ExecutionRun;
    currentStatus?: "pending" | "processing";
    runInProgress: boolean;
    loading?: boolean;
    refreshing?: boolean;
    error?: string;
    queueBusy?: boolean;
    busyItemId?: string;
    announcement?: string;
    onAddRun: () => void;
    onRemoveRun: (runId: string) => void;
    onMoveRun: (runId: string, direction: "up" | "down") => void | Promise<void>;
    onClearQueue: () => void;
    onStopRun: () => void;
    onRetry: () => void;
}

export function RunQueuePanel({
    items,
    activeItem,
    currentRun,
    currentStatus = "processing",
    runInProgress,
    loading = false,
    refreshing = false,
    error,
    queueBusy = false,
    busyItemId,
    announcement,
    onAddRun,
    onRemoveRun,
    onMoveRun,
    onClearQueue,
    onStopRun,
    onRetry,
}: RunQueuePanelProps) {
    return (
        <section className="glass-card overflow-hidden rounded-[var(--radius-card)] border border-[var(--border-subtle)] shadow-[var(--shadow-card)]">
            <span className="sr-only" aria-live="polite">
                {items.length === 1 ? "1 run is waiting in the queue." : `${items.length} runs are waiting in the queue.`}
            </span>
            <span className="sr-only" aria-live="polite">{announcement}</span>
            <div className="border-b border-[var(--border-subtle)] p-4 sm:p-5">
                <div className="flex flex-col gap-4 lg:flex-row lg:items-start lg:justify-between">
                    <div className="min-w-0">
                        <div className="flex flex-wrap items-center gap-2">
                            <h2 className="flex items-center gap-2 text-base font-bold text-[var(--text-primary)]">
                                <ListOrdered className="h-4 w-4 text-[var(--brand-solid)]" />
                                Job Queue
                            </h2>
                            <span className="rounded-full bg-[var(--brand-light)] px-2 py-0.5 text-[10px] font-semibold uppercase tracking-wide text-[var(--brand-solid)]">
                                Sequential
                            </span>
                            {refreshing && !loading && (
                                <Loader2 className="h-3.5 w-3.5 animate-spin text-[var(--text-tertiary)] motion-reduce:animate-none" aria-label="Refreshing queue" />
                            )}
                        </div>
                        <p className="mt-1 max-w-2xl text-sm text-[var(--text-secondary)]">
                            Line up different model configurations for this audio file. Runs are saved on the server and processed one at a time.
                        </p>
                    </div>

                    <div className="flex shrink-0 flex-nowrap items-center gap-2">
                        {items.length > 0 && (
                            <AlertDialog>
                                <AlertDialogTrigger asChild>
                                    <Button
                                        variant="ghost"
                                        size="sm"
                                        disabled={queueBusy}
                                        className="gap-2 rounded-full text-[var(--text-secondary)] hover:text-red-600 dark:hover:text-red-300"
                                    >
                                        <Trash2 className="h-4 w-4" />
                                        Clear queue
                                    </Button>
                                </AlertDialogTrigger>
                                <AlertDialogContent className="glass-card bg-[var(--bg-main)]/95 border-[var(--border-subtle)]">
                                    <AlertDialogHeader>
                                        <AlertDialogTitle className="text-[var(--text-primary)]">Clear waiting runs?</AlertDialogTitle>
                                        <AlertDialogDescription className="text-[var(--text-secondary)]">
                                            Cancel all {items.length} waiting {items.length === 1 ? "run" : "runs"}? The active run will continue, and cancelled runs cannot be restored.
                                        </AlertDialogDescription>
                                    </AlertDialogHeader>
                                    <AlertDialogFooter>
                                        <AlertDialogCancel>Keep runs</AlertDialogCancel>
                                        <AlertDialogAction
                                            onClick={onClearQueue}
                                            disabled={queueBusy}
                                            className="bg-red-600 text-white hover:bg-red-700"
                                        >
                                            Cancel waiting runs
                                        </AlertDialogAction>
                                    </AlertDialogFooter>
                                </AlertDialogContent>
                            </AlertDialog>
                        )}
                        <Button
                            size="sm"
                            onClick={onAddRun}
                            disabled={queueBusy}
                            className="gap-2 rounded-full border-0 !text-black shadow-lg shadow-orange-500/15 hover:opacity-90 dark:!text-white"
                            style={{ background: "var(--brand-gradient)" }}
                        >
                            <Plus className="h-4 w-4" />
                            Add run
                        </Button>
                    </div>
                </div>

                <div className="mt-4 flex gap-2 rounded-xl border border-[var(--brand-solid)]/20 bg-[var(--brand-light)]/35 px-3 py-2 text-xs leading-5 text-[var(--text-secondary)]">
                    <Clock3 className="mt-0.5 h-4 w-4 shrink-0 text-[var(--brand-solid)]" />
                    <p>
                        {runInProgress
                            ? currentStatus === "pending"
                                ? "Cancelling the pending run advances the queue immediately. Waiting runs can be reordered or cancelled before they start."
                                : "Stopping the active run starts the next waiting run automatically after the worker finishes cleanup. Waiting runs can be reordered or cancelled before they start."
                            : "The first run starts as soon as a worker is available. Additional waiting runs can be reordered or cancelled before they start."}
                    </p>
                </div>
            </div>

            <div className="p-4 sm:p-5">
                {error && (
                    <div className="mb-3 flex flex-col gap-2 rounded-xl border border-red-500/25 bg-red-500/10 p-3 text-sm text-red-700 sm:flex-row sm:items-center sm:justify-between dark:text-red-300" role="alert">
                        <span>{error}</span>
                        <Button variant="outline" size="sm" onClick={onRetry} className="self-start rounded-full sm:self-auto">
                            Try again
                        </Button>
                    </div>
                )}

                {runInProgress && (
                    <CurrentRunRow
                        activeItem={activeItem}
                        run={currentRun}
                        status={currentStatus}
                        hasNext={items.length > 0}
                        onStopRun={onStopRun}
                    />
                )}

                {runInProgress && items.length > 0 && (
                    <div className="ml-[19px] h-5 border-l border-dashed border-[var(--border-subtle)]" aria-hidden="true" />
                )}

                {loading ? (
                    <div className="flex items-center justify-center gap-2 px-4 py-8 text-sm text-[var(--text-secondary)]">
                        <Loader2 className="h-4 w-4 animate-spin motion-reduce:animate-none" />
                        Loading queue…
                    </div>
                ) : items.length > 0 ? (
                    <ol className="space-y-2" aria-label="Queued transcription runs">
                        {items.map((item, index) => (
                            <QueueItemRow
                                key={item.id}
                                item={item}
                                index={index}
                                itemCount={items.length}
                                isNext={index === 0}
                                hasActiveJob={runInProgress}
                                disabled={queueBusy}
                                busy={busyItemId === item.id}
                                onMoveRun={onMoveRun}
                                onRemoveRun={onRemoveRun}
                            />
                        ))}
                    </ol>
                ) : (
                    <EmptyQueue runInProgress={runInProgress} onAddRun={onAddRun} disabled={queueBusy} />
                )}
            </div>
        </section>
    );
}

function CurrentRunRow({
    activeItem,
    run,
    status,
    hasNext,
    onStopRun,
}: {
    activeItem?: TranscriptionQueueItem | null;
    run?: ExecutionRun;
    status: "pending" | "processing";
    hasNext: boolean;
    onStopRun: () => void;
}) {
    const params = activeItem?.parameters || run?.actual_parameters;
    const isPending = status === "pending";

    return (
        <div className="flex flex-col gap-3 rounded-xl border border-[var(--brand-solid)]/25 bg-[var(--brand-light)]/40 p-3 sm:flex-row sm:items-center sm:justify-between">
            <div className="flex min-w-0 items-start gap-3">
                <span className="mt-0.5 flex h-9 w-9 shrink-0 items-center justify-center rounded-full bg-[var(--brand-solid)] text-white shadow-sm">
                    {isPending ? <Clock3 className="h-4 w-4" /> : <Loader2 className="h-4 w-4 animate-spin motion-reduce:animate-none" />}
                </span>
                <div className="min-w-0">
                    <div className="flex flex-wrap items-center gap-2">
                        <span className="text-xs font-semibold uppercase tracking-wide text-[var(--brand-solid)]">
                            {isPending ? "Waiting for worker" : "Running now"}
                        </span>
                        {run && <span className="text-xs text-[var(--text-tertiary)]">Run {run.run_number}</span>}
                        {activeItem?.profile_name && <span className="text-xs text-[var(--text-tertiary)]">Profile · {activeItem.profile_name}</span>}
                    </div>
                    <p className="truncate text-sm font-semibold text-[var(--text-primary)]">
                        {params ? modelLabel(params) : "Active transcription"}
                    </p>
                    <p className="text-xs text-[var(--text-secondary)]">
                        {isPending
                            ? (hasNext ? "The next waiting run starts after this run completes or is cancelled." : "Add another model while this job waits for a worker.")
                            : (hasNext ? "Stopping this run advances the queue after worker cleanup." : "Add another model while this run finishes.")}
                    </p>
                </div>
            </div>

            <Button
                variant="outline"
                size="sm"
                onClick={onStopRun}
                className="gap-2 self-start rounded-full border-red-500/30 bg-red-500/10 text-red-600 hover:bg-red-500/15 hover:text-red-700 sm:self-auto dark:text-red-300 dark:hover:text-red-200"
            >
                <Square className="h-3.5 w-3.5 fill-current" />
                {isPending ? "Cancel job" : "Stop run"}
            </Button>
        </div>
    );
}

function QueueItemRow({
    item,
    index,
    itemCount,
    isNext,
    hasActiveJob,
    disabled,
    busy,
    onMoveRun,
    onRemoveRun,
}: {
    item: TranscriptionQueueItem;
    index: number;
    itemCount: number;
    isNext: boolean;
    hasActiveJob: boolean;
    disabled: boolean;
    busy: boolean;
    onMoveRun: (runId: string, direction: "up" | "down") => void | Promise<void>;
    onRemoveRun: (runId: string) => void;
}) {
    const params = item.parameters;
    const controlsRef = useRef<HTMLDivElement>(null);

    const moveRun = async (direction: "up" | "down") => {
        try {
            await onMoveRun(item.id, direction);
        } catch {
            // The parent owns mutation error reporting; focus still needs restoring.
        }
        requestAnimationFrame(() => {
            const fallbackDirection = direction === "up" ? "down" : "up";
            controlsRef.current?.querySelector<HTMLButtonElement>(`button[data-queue-direction="${fallbackDirection}"]`)?.focus();
        });
    };

    return (
        <li className="group flex flex-col gap-3 rounded-xl border border-[var(--border-subtle)] bg-[var(--bg-main)]/45 p-3 transition-colors hover:border-[var(--brand-solid)]/25 sm:flex-row sm:items-center">
            <div className="flex min-w-0 flex-1 items-start gap-3">
                <span
                    className={cn(
                        "flex h-9 w-9 shrink-0 items-center justify-center rounded-full border font-mono text-xs font-bold",
                        isNext
                            ? "border-[var(--brand-solid)]/30 bg-[var(--brand-light)] text-[var(--brand-solid)]"
                            : "border-[var(--border-subtle)] bg-[var(--bg-card)] text-[var(--text-secondary)]"
                    )}
                    aria-hidden="true"
                >
                    {index + 1}
                </span>
                <div className="min-w-0">
                    {item.profile_name && (
                        <p className="mb-0.5 truncate text-[10px] font-semibold uppercase tracking-wide text-[var(--text-tertiary)]">
                            Profile · {item.profile_name}
                        </p>
                    )}
                    <div className="flex flex-wrap items-center gap-2">
                        <p className="truncate text-sm font-semibold text-[var(--text-primary)]">{modelLabel(params)}</p>
                        {isNext && (
                            <span className="rounded-full bg-[var(--brand-light)] px-2 py-0.5 text-[10px] font-semibold uppercase tracking-wide text-[var(--brand-solid)]">
                                {hasActiveJob ? "Up next" : "Next"}
                            </span>
                        )}
                    </div>
                    <div className="mt-1 flex flex-wrap gap-x-3 gap-y-1 text-xs text-[var(--text-secondary)]">
                        <span>{deviceLabel(params.device)}</span>
                        <span>{languageLabel(params.language)}</span>
                        <span>{params.diarize ? "Diarization on" : "Diarization off"}</span>
                        {params.batch_size ? <span>Batch {params.batch_size}</span> : null}
                    </div>
                </div>
            </div>

            <div ref={controlsRef} className="flex items-center gap-1 self-end sm:self-auto">
                <Button
                    variant="ghost"
                    size="icon"
                    onClick={() => void moveRun("up")}
                    disabled={disabled || index === 0}
                    data-queue-direction="up"
                    aria-label={`Move queued run ${index + 1} up`}
                    title="Move up"
                    className="h-8 w-8 rounded-full text-[var(--text-secondary)]"
                >
                    <ArrowUp className="h-4 w-4" />
                </Button>
                <Button
                    variant="ghost"
                    size="icon"
                    onClick={() => void moveRun("down")}
                    disabled={disabled || index === itemCount - 1}
                    data-queue-direction="down"
                    aria-label={`Move queued run ${index + 1} down`}
                    title="Move down"
                    className="h-8 w-8 rounded-full text-[var(--text-secondary)]"
                >
                    <ArrowDown className="h-4 w-4" />
                </Button>
                <Button
                    variant="ghost"
                    size="icon"
                    onClick={() => onRemoveRun(item.id)}
                    disabled={disabled}
                    aria-label={`Cancel queued run ${index + 1}`}
                    title="Cancel queued run"
                    className="h-8 w-8 rounded-full text-[var(--text-secondary)] hover:bg-red-500/10 hover:text-red-600 dark:hover:text-red-300"
                >
                    {busy ? <Loader2 className="h-4 w-4 animate-spin motion-reduce:animate-none" /> : <X className="h-4 w-4" />}
                </Button>
            </div>
        </li>
    );
}

function EmptyQueue({
    runInProgress,
    onAddRun,
    disabled,
}: {
    runInProgress: boolean;
    onAddRun: () => void;
    disabled: boolean;
}) {
    return (
        <div className={cn("flex flex-col items-center justify-center px-4 py-7 text-center", runInProgress && "pt-6")}>
            <span className="mb-3 flex h-11 w-11 items-center justify-center rounded-full bg-[var(--bg-main)] text-[var(--text-tertiary)]">
                <Layers3 className="h-5 w-5" />
            </span>
            <p className="text-sm font-semibold text-[var(--text-primary)]">No runs waiting</p>
            <p className="mt-1 max-w-md text-xs leading-5 text-[var(--text-secondary)]">
                Add a saved profile or custom model setup. It will persist here and start when this audio file reaches the front of the worker queue.
            </p>
            <Button variant="outline" size="sm" onClick={onAddRun} disabled={disabled} className="mt-3 gap-2 rounded-full border-[var(--border-subtle)] bg-[var(--bg-card)]">
                <Plus className="h-4 w-4" />
                Add first run
            </Button>
        </div>
    );
}

function modelLabel(params: Partial<WhisperXParams>) {
    if (params.model_family === "nvidia_canary") return "NVIDIA Canary 1B";
    if (params.model_family === "nvidia_canary_qwen") return "NVIDIA Canary-Qwen 2.5B";
    if (params.model_family === "nvidia_parakeet") return "NVIDIA Parakeet";
    if (params.model_family === "mistral_voxtral") return "Mistral Voxtral-mini";
    if (params.model_family === "openai") return `OpenAI ${params.model || "Whisper"}`;
    if (params.model_family === "whisper") return `Whisper ${params.model || ""}`.trim();
    return params.model_family || "Transcription";
}

function deviceLabel(device?: string) {
    if (!device || device === "auto") return "Auto device";
    if (device === "cuda") return "GPU (CUDA)";
    return device.toUpperCase();
}

function languageLabel(language?: string) {
    if (!language || language === "auto") return "Auto language";
    return language.toUpperCase();
}

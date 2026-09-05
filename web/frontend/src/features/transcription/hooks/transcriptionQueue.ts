export interface QueuedRunParameters {
    model_family?: string;
    model?: string;
    device?: string;
    language?: string;
    diarize?: boolean;
    batch_size?: number;
    [key: string]: unknown;
}

export type TranscriptionQueueStatus =
    | "queued"
    | "pending"
    | "processing"
    | "completed"
    | "failed"
    | "cancelled"
    | "canceled"
    | "stopped";

export interface TranscriptionQueueItem {
    id: string;
    position: number;
    status: TranscriptionQueueStatus | string;
    parameters: QueuedRunParameters;
    profile_id?: string;
    profile_name?: string;
    execution_id?: string;
    created_at: string;
    started_at?: string;
}

export interface TranscriptionQueueData {
    job_id: string;
    active_item?: TranscriptionQueueItem | null;
    items: TranscriptionQueueItem[];
    history?: TranscriptionQueueItem[];
}

export interface AddTranscriptionQueueItemInput<TParameters = unknown> {
    parameters: TParameters;
    profile_id?: string;
    profile_name?: string;
}

export interface RunArtifactRefreshSnapshot {
    audioId: string;
    activeItemId?: string;
    activeItemStatus?: string;
    parentStatus?: string;
}

export interface StopRunTargetSnapshot {
    queueItemId?: string;
    executionRunId?: string;
}

export function buildQueueRequest<TParameters>(input: AddTranscriptionQueueItemInput<TParameters>) {
    return {
        parameters: input.parameters,
        ...(input.profile_id ? { profile_id: input.profile_id } : {}),
        ...(input.profile_name ? { profile_name: input.profile_name } : {}),
    };
}

export function sanitizeQueueData(data: TranscriptionQueueData): TranscriptionQueueData {
    return {
        ...data,
        active_item: data.active_item ? sanitizeQueueItem(data.active_item) : data.active_item,
        items: (data.items || []).map(sanitizeQueueItem),
        history: data.history?.map(sanitizeQueueItem),
    };
}

function sanitizeQueueItem(item: TranscriptionQueueItem): TranscriptionQueueItem {
    const parameters = { ...item.parameters };
    delete parameters.api_key;
    delete parameters.hf_token;
    return { ...item, parameters };
}

export function shouldRefreshRunArtifacts(
    previous: RunArtifactRefreshSnapshot | undefined,
    next: RunArtifactRefreshSnapshot
) {
    if (!previous || previous.audioId !== next.audioId) return false;

    if (
        previous.activeItemId !== next.activeItemId
        || previous.activeItemStatus !== next.activeItemStatus
    ) {
        return true;
    }

    const parentWasActive = previous.parentStatus === "pending" || previous.parentStatus === "processing";
    const parentIsTerminal = next.parentStatus === "completed" || next.parentStatus === "failed";
    return parentWasActive && parentIsTerminal;
}

export function isStopRunTargetCurrent(
    target: StopRunTargetSnapshot,
    current: StopRunTargetSnapshot
) {
    if (target.queueItemId) {
        return target.queueItemId === current.queueItemId;
    }

    if (current.queueItemId) return false;
    return !target.executionRunId || target.executionRunId === current.executionRunId;
}

export function getQueuedItems(items: readonly TranscriptionQueueItem[]) {
    return items
        .filter((item) => item.status.toLowerCase() === "queued")
        .sort((left, right) => left.position - right.position);
}

export function moveQueuedItemIds(
    items: readonly TranscriptionQueueItem[],
    itemId: string,
    direction: "up" | "down"
) {
    const orderedIds = getQueuedItems(items).map((item) => item.id);
    const currentIndex = orderedIds.indexOf(itemId);
    const nextIndex = direction === "up" ? currentIndex - 1 : currentIndex + 1;

    if (currentIndex < 0 || nextIndex < 0 || nextIndex >= orderedIds.length) {
        return orderedIds;
    }

    [orderedIds[currentIndex], orderedIds[nextIndex]] = [orderedIds[nextIndex], orderedIds[currentIndex]];
    return orderedIds;
}

export function reorderQueuedItems(
    items: readonly TranscriptionQueueItem[],
    orderedIds: readonly string[]
) {
    const queuedById = new Map(getQueuedItems(items).map((item) => [item.id, item]));
    const reordered = orderedIds
        .map((id, index) => {
            const item = queuedById.get(id);
            return item ? { ...item, position: index + 1 } : undefined;
        })
        .filter((item): item is TranscriptionQueueItem => Boolean(item));

    if (reordered.length !== queuedById.size) return [...items];

    let queuedIndex = 0;
    return items.map((item) => (
        item.status.toLowerCase() === "queued"
            ? reordered[queuedIndex++]
            : item
    ));
}

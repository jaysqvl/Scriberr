import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useAuth } from "@/features/auth/hooks/useAuth";
import type { WhisperXParams } from "@/components/TranscriptionConfigDialog";
import {
    buildQueueRequest,
    reorderQueuedItems,
    sanitizeQueueData,
    type AddTranscriptionQueueItemInput,
    type TranscriptionQueueData,
} from "./transcriptionQueue";

const queueQueryKey = (audioId: string) => ["transcriptionQueue", audioId] as const;

async function getErrorMessage(response: Response, fallback: string) {
    const text = await response.text();
    if (!text) return fallback;

    try {
        const data = JSON.parse(text) as { error?: string; message?: string };
        return data.error || data.message || fallback;
    } catch {
        return text;
    }
}

export function useTranscriptionQueue(audioId: string, enabled = true) {
    const { getAuthHeaders } = useAuth();

    return useQuery({
        queryKey: queueQueryKey(audioId),
        queryFn: async () => {
            const response = await fetch(`/api/v1/transcription/${audioId}/queue`, {
                headers: getAuthHeaders(),
            });
            if (!response.ok) {
                throw new Error(await getErrorMessage(response, "Failed to load the run queue"));
            }
            const data = await response.json() as TranscriptionQueueData;
            return sanitizeQueueData(data);
        },
        enabled: enabled && !!audioId,
        refetchInterval: 3000,
    });
}

function useInvalidateTranscriptionQueue(audioId: string) {
    const queryClient = useQueryClient();

    return async () => {
        await Promise.all([
            queryClient.invalidateQueries({ queryKey: queueQueryKey(audioId) }),
            queryClient.invalidateQueries({ queryKey: ["audio", audioId] }),
            queryClient.invalidateQueries({ queryKey: ["executionRuns", audioId] }),
            queryClient.invalidateQueries({ queryKey: ["executionData", audioId] }),
            queryClient.invalidateQueries({ queryKey: ["transcript", audioId] }),
            queryClient.invalidateQueries({ queryKey: ["logs", audioId] }),
            queryClient.invalidateQueries({ queryKey: ["audioFiles"] }),
        ]);
    };
}

export function useAddTranscriptionQueueItem(audioId: string) {
    const { getAuthHeaders } = useAuth();
    const invalidate = useInvalidateTranscriptionQueue(audioId);

    return useMutation({
        mutationFn: async (input: AddTranscriptionQueueItemInput<WhisperXParams>) => {
            const response = await fetch(`/api/v1/transcription/${audioId}/queue`, {
                method: "POST",
                headers: {
                    ...getAuthHeaders(),
                    "Content-Type": "application/json",
                },
                body: JSON.stringify(buildQueueRequest(input)),
            });
            if (!response.ok) {
                throw new Error(await getErrorMessage(response, "Failed to add the run to the queue"));
            }
        },
        onSuccess: invalidate,
        gcTime: 0,
    });
}

export function useReorderTranscriptionQueue(audioId: string) {
    const { getAuthHeaders } = useAuth();
    const queryClient = useQueryClient();
    const invalidate = useInvalidateTranscriptionQueue(audioId);

    return useMutation({
        mutationFn: async (orderedIds: string[]) => {
            const response = await fetch(`/api/v1/transcription/${audioId}/queue/order`, {
                method: "PUT",
                headers: {
                    ...getAuthHeaders(),
                    "Content-Type": "application/json",
                },
                body: JSON.stringify({ ordered_ids: orderedIds }),
            });
            if (!response.ok) {
                throw new Error(await getErrorMessage(response, "Failed to reorder the queue"));
            }
        },
        onMutate: async (orderedIds) => {
            await queryClient.cancelQueries({ queryKey: queueQueryKey(audioId) });
            const previous = queryClient.getQueryData<TranscriptionQueueData>(queueQueryKey(audioId));
            if (previous) {
                queryClient.setQueryData<TranscriptionQueueData>(queueQueryKey(audioId), {
                    ...previous,
                    items: reorderQueuedItems(previous.items, orderedIds),
                });
            }
            return { previous };
        },
        onError: (_error, _orderedIds, context) => {
            if (context?.previous) {
                queryClient.setQueryData(queueQueryKey(audioId), context.previous);
            }
        },
        onSettled: invalidate,
    });
}

export function useCancelTranscriptionQueueItem(audioId: string) {
    const { getAuthHeaders } = useAuth();
    const queryClient = useQueryClient();
    const invalidate = useInvalidateTranscriptionQueue(audioId);

    return useMutation({
        mutationFn: async (queueId: string) => {
            const response = await fetch(`/api/v1/transcription/${audioId}/queue/${queueId}`, {
                method: "DELETE",
                headers: getAuthHeaders(),
            });
            if (!response.ok) {
                throw new Error(await getErrorMessage(response, "Failed to cancel the queued run"));
            }
        },
        onMutate: async (queueId) => {
            await queryClient.cancelQueries({ queryKey: queueQueryKey(audioId) });
            const previous = queryClient.getQueryData<TranscriptionQueueData>(queueQueryKey(audioId));
            if (previous) {
                queryClient.setQueryData<TranscriptionQueueData>(queueQueryKey(audioId), {
                    ...previous,
                    items: previous.items.filter((item) => item.id !== queueId),
                });
            }
            return { previous };
        },
        onError: (_error, _queueId, context) => {
            if (context?.previous) {
                queryClient.setQueryData(queueQueryKey(audioId), context.previous);
            }
        },
        onSettled: invalidate,
    });
}

export function useClearTranscriptionQueue(audioId: string) {
    const { getAuthHeaders } = useAuth();
    const queryClient = useQueryClient();
    const invalidate = useInvalidateTranscriptionQueue(audioId);

    return useMutation({
        mutationFn: async () => {
            const response = await fetch(`/api/v1/transcription/${audioId}/queue`, {
                method: "DELETE",
                headers: getAuthHeaders(),
            });
            if (!response.ok) {
                throw new Error(await getErrorMessage(response, "Failed to clear the queue"));
            }
        },
        onMutate: async () => {
            await queryClient.cancelQueries({ queryKey: queueQueryKey(audioId) });
            const previous = queryClient.getQueryData<TranscriptionQueueData>(queueQueryKey(audioId));
            if (previous) {
                queryClient.setQueryData<TranscriptionQueueData>(queueQueryKey(audioId), {
                    ...previous,
                    items: previous.items.filter((item) => item.status.toLowerCase() !== "queued"),
                });
            }
            return { previous };
        },
        onError: (_error, _variables, context) => {
            if (context?.previous) {
                queryClient.setQueryData(queueQueryKey(audioId), context.previous);
            }
        },
        onSettled: invalidate,
    });
}

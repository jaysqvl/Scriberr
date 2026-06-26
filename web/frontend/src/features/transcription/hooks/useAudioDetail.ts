import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useAuth } from "@/features/auth/hooks/useAuth";
import type { WhisperXParams } from "@/components/TranscriptionConfigDialog";


// Types
export interface MultiTrackFile {
    id: number;
    file_name: string;
    file_path: string;
    track_index: number;
}

export interface MultiTrackTiming {
    track_name: string;
    start_time: string;
    end_time: string;
    duration: number; // milliseconds
}

export interface ExecutionData {
    id?: string;
    transcription_job_id: string;
    started_at?: string;
    completed_at?: string | null;
    processing_duration?: number | null; // milliseconds
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    actual_parameters?: any;
    status?: string;
    error_message?: string | null;
    created_at?: string;
    updated_at?: string;
    // Multi-track specific fields
    is_multi_track?: boolean;
    multi_track_timings?: MultiTrackTiming[];
    merge_start_time?: string | null;
    merge_end_time?: string | null;
    merge_duration?: number | null; // milliseconds
    multi_track_files?: MultiTrackFile[];
    // Graceful empty response fields
    available?: boolean;
    message?: string;
}

export interface ExecutionRun extends ExecutionData {
    id: string;
    run_number: number;
    actual_parameters?: WhisperXParams;
    has_transcript?: boolean;
    has_logs?: boolean;
}

export interface ExecutionRunsData {
    job_id: string;
    active_run_id?: string;
    runs: ExecutionRun[];
}

export interface LogsData {
    job_id: string;
    run_id?: string;
    available: boolean;
    content: string;
    message?: string;
}

export interface AudioFile {
    id: string;
    title?: string;
    status: "uploaded" | "pending" | "processing" | "completed" | "failed";
    created_at: string;
    audio_path: string;
    diarization?: boolean;
    is_multi_track?: boolean;
    multi_track_files?: MultiTrackFile[];
    merged_audio_path?: string;
    merge_status?: string;
    merge_error?: string;
    parameters?: {
        diarize?: boolean;
        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        [key: string]: any;
    };
}

export interface WordSegment {
    start: number;
    end: number;
    word: string;
    score: number;
    speaker?: string;
}

export interface TranscriptSegment {
    start: number;
    end: number;
    text: string;
    speaker?: string;
}

export interface Transcript {
    text: string;
    segments?: TranscriptSegment[];
    word_segments?: WordSegment[];
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
function normalizeTranscript(data: any): Transcript | null {
    if (data.available === false || !data.transcript) {
        return null;
    }

    if (typeof data.transcript === "string") {
        return { text: data.transcript };
    }
    if (data.transcript.text) {
        return {
            text: data.transcript.text,
            segments: data.transcript.segments,
            word_segments: data.transcript.word_segments,
        };
    }
    if (data.transcript.segments) {
        const fullText = data.transcript.segments
            // eslint-disable-next-line @typescript-eslint/no-explicit-any
            .map((segment: any) => segment.text)
            .join(" ");
        return {
            text: fullText,
            segments: data.transcript.segments,
            word_segments: data.transcript.word_segments,
        };
    }

    return { text: "" };
}

export function useAudioDetail(audioId: string) {
    const { getAuthHeaders } = useAuth();

    return useQuery({
        queryKey: ["audio", audioId],
        queryFn: async () => {
            const response = await fetch(`/api/v1/transcription/${audioId}`, {
                headers: getAuthHeaders(),
            });
            if (!response.ok) throw new Error("Failed to fetch audio details");
            return response.json() as Promise<AudioFile>;
        },
        // Poll while processing or pending
        refetchInterval: (query) => {
            const status = query.state.data?.status;
            if (status === "processing" || status === "pending") {
                return 3000;
            }
            return false;
        },
    });
}

export function useTranscript(audioId: string, enabled: boolean) {
    const { getAuthHeaders } = useAuth();

    return useQuery({
        queryKey: ["transcript", audioId],
        queryFn: async () => {
            const response = await fetch(`/api/v1/transcription/${audioId}/transcript`, {
                headers: getAuthHeaders(),
            });
            if (!response.ok) throw new Error("Failed to fetch transcript");
            const data = await response.json();
            return normalizeTranscript(data);
        },
        enabled: enabled,
    });
}

export function useExecutionData(audioId: string) {
    const { getAuthHeaders } = useAuth();
    return useQuery({
        queryKey: ["executionData", audioId],
        queryFn: async () => {
            const response = await fetch(`/api/v1/transcription/${audioId}/execution`, {
                headers: getAuthHeaders(),
            });
            if (!response.ok) throw new Error("Failed to fetch execution data");
            return response.json() as Promise<ExecutionData>;
        },
        enabled: !!audioId,
    });
}

export function useExecutionRuns(audioId: string, enabled = true) {
    const { getAuthHeaders } = useAuth();
    return useQuery({
        queryKey: ["executionRuns", audioId],
        queryFn: async () => {
            const response = await fetch(`/api/v1/transcription/${audioId}/runs`, {
                headers: getAuthHeaders(),
            });
            if (!response.ok) throw new Error("Failed to fetch execution runs");
            return response.json() as Promise<ExecutionRunsData>;
        },
        enabled: enabled && !!audioId,
    });
}

export function useRunTranscript(audioId: string, runId?: string, enabled = true) {
    const { getAuthHeaders } = useAuth();
    return useQuery({
        queryKey: ["runTranscript", audioId, runId],
        queryFn: async () => {
            const response = await fetch(`/api/v1/transcription/${audioId}/runs/${runId}/transcript`, {
                headers: getAuthHeaders(),
            });
            if (!response.ok) throw new Error("Failed to fetch run transcript");
            const data = await response.json();
            return normalizeTranscript(data);
        },
        enabled: enabled && !!audioId && !!runId,
    });
}

export function useRunLogs(audioId: string, runId?: string, enabled = true) {
    const { getAuthHeaders } = useAuth();
    return useQuery({
        queryKey: ["runLogs", audioId, runId],
        queryFn: async () => {
            const response = await fetch(`/api/v1/transcription/${audioId}/runs/${runId}/logs`, {
                headers: getAuthHeaders(),
            });
            if (!response.ok) throw new Error("Failed to fetch run logs");
            return response.json() as Promise<LogsData>;
        },
        enabled: enabled && !!audioId && !!runId,
    });
}

export function useLogs(audioId: string, enabled = true) {
    const { getAuthHeaders } = useAuth();
    return useQuery({
        queryKey: ["logs", audioId],
        queryFn: async () => {
            const response = await fetch(`/api/v1/transcription/${audioId}/logs`, {
                headers: getAuthHeaders(),
            });
            if (!response.ok) throw new Error("Failed to fetch logs");
            return response.json() as Promise<LogsData>;
        },
        enabled: enabled && !!audioId,
    });
}

export function useUpdateTitle(audioId: string) {
    const { getAuthHeaders } = useAuth();
    const queryClient = useQueryClient();

    return useMutation({
        mutationFn: async (newTitle: string) => {
            const response = await fetch(`/api/v1/transcription/${audioId}/title`, {
                method: "PUT",
                headers: {
                    "Content-Type": "application/json",
                    ...getAuthHeaders(),
                },
                body: JSON.stringify({ title: newTitle }),
            });
            if (!response.ok) {
                const msg = await response.text();
                throw new Error(msg || "Failed to update title");
            }
            return response.json();
        },
        onSuccess: () => {
            queryClient.invalidateQueries({ queryKey: ["audio", audioId] });
            queryClient.invalidateQueries({ queryKey: ["audioFiles"] }); // Update list too
        },
    });
}

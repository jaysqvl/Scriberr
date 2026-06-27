import { useQuery, useMutation, useQueryClient, keepPreviousData, useInfiniteQuery } from '@tanstack/react-query';
import { useAuth } from '@/features/auth/hooks/useAuth';
import { sortProfilesByName } from '@/lib/profiles';
import { uploadResumable, type UploadProgressInfo } from '@/lib/resumableUpload';

export interface AudioFile {
    id: string;
    title?: string;
    status: "uploaded" | "pending" | "processing" | "completed" | "failed";
    created_at: string;
    audio_path: string;
    diarization?: boolean;
    is_multi_track?: boolean;
    error_message?: string;
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    individual_transcripts?: any;
    speakers?: number;
    duration?: number;
}

export interface AudioFilesResponse {
    jobs: AudioFile[];
    pagination: {
        page: number;
        limit: number;
        total: number;
        pages: number;
    };
}

interface AudioListParams {
    page: number;
    limit: number;
    search?: string;
    sortBy?: string;
    sortOrder?: 'asc' | 'desc';
}

export function useAudioList(params: AudioListParams) {
    const { getAuthHeaders } = useAuth();

    return useQuery({
        queryKey: ['audioFiles', params],
        queryFn: async () => {
            const searchParams = new URLSearchParams({
                page: params.page.toString(),
                limit: params.limit.toString(),
            });

            if (params.search) searchParams.set('q', params.search);
            if (params.sortBy) {
                searchParams.set('sort_by', params.sortBy);
                searchParams.set('sort_order', params.sortOrder || 'desc');
            }

            const response = await fetch(`/api/v1/transcription/list?${searchParams}`, {
                headers: getAuthHeaders(),
            });

            if (!response.ok) {
                throw new Error('Failed to fetch audio files');
            }

            return response.json() as Promise<AudioFilesResponse>;
        },
        placeholderData: keepPreviousData,
        refetchInterval: false
    });
}

export function useAudioListInfinite(params: Omit<AudioListParams, 'page'>) {
    const { getAuthHeaders } = useAuth();

    return useInfiniteQuery({
        queryKey: ['audioFiles', 'infinite', params],
        queryFn: async ({ pageParam = 1 }) => {
            const searchParams = new URLSearchParams({
                page: pageParam.toString(),
                limit: params.limit.toString(),
            });

            if (params.search) searchParams.set('q', params.search);
            if (params.sortBy) {
                searchParams.set('sort_by', params.sortBy);
                searchParams.set('sort_order', params.sortOrder || 'desc');
            }

            const response = await fetch(`/api/v1/transcription/list?${searchParams}`, {
                headers: getAuthHeaders(),
            });

            if (!response.ok) {
                throw new Error('Failed to fetch audio files');
            }

            return response.json() as Promise<AudioFilesResponse>;
        },
        getNextPageParam: (lastPage) => {
            if (lastPage.pagination.page < lastPage.pagination.pages) {
                return lastPage.pagination.page + 1;
            }
            return undefined;
        },
        initialPageParam: 1,
        refetchInterval: false
    });
}

export function useAudioUpload() {
    const { getAuthHeaders } = useAuth();
    const queryClient = useQueryClient();

    return useMutation({
        mutationFn: async ({ file, isVideo, onProgress }: { file: File, isVideo: boolean, onProgress?: (progress: UploadProgressInfo) => void }) => {
            return uploadResumable({
                kind: isVideo ? 'video' : 'audio',
                title: file.name.replace(/\.[^/.]+$/, ''),
                files: [{
                    id: isVideo ? 'video' : 'audio',
                    role: isVideo ? 'video' : 'audio',
                    file,
                }],
                headers: getAuthHeaders(),
                onProgress,
            });
        },
        onSuccess: () => {
            queryClient.invalidateQueries({ queryKey: ['audioFiles'] });
        },
    });
}

export function useMultiTrackUpload() {
    const { getAuthHeaders } = useAuth();
    const queryClient = useQueryClient();

    return useMutation({
        mutationFn: async ({ files, aupFile, title, onProgress }: { files: File[], aupFile: File, title: string, onProgress?: (progress: UploadProgressInfo) => void }) => {
            return uploadResumable({
                kind: 'multitrack',
                title,
                files: [
                    { id: 'aup', role: 'aup', file: aupFile },
                    ...files.map((file, index) => ({
                        id: `track-${index}`,
                        role: 'track' as const,
                        file,
                    })),
                ],
                headers: getAuthHeaders(),
                onProgress,
            });
        },
        onSuccess: () => {
            queryClient.invalidateQueries({ queryKey: ['audioFiles'] });
        }
    });
}

export function useYouTubeDownload() {
    const { getAuthHeaders } = useAuth();
    const queryClient = useQueryClient();

    return useMutation({
        mutationFn: async ({ url, title }: { url: string, title?: string }) => {
            const response = await fetch("/api/v1/transcription/youtube", {
                method: "POST",
                headers: {
                    "Content-Type": "application/json",
                    ...getAuthHeaders(),
                },
                body: JSON.stringify({
                    url: url.trim(),
                    title: title?.trim() || undefined,
                }),
            });

            if (!response.ok) {
                const errorData = await response.json();
                throw new Error(errorData.error || "Failed to download YouTube audio");
            }
            return response.json();
        },
        onSuccess: () => {
            queryClient.invalidateQueries({ queryKey: ['audioFiles'] });
        },
    });
}

export interface Profile {
    id: string;
    name: string;
    description?: string;
    is_default: boolean;
}

export function useTranscriptionProfiles() {
    const { getAuthHeaders } = useAuth();
    return useQuery({
        queryKey: ['transcriptionProfiles'],
        queryFn: async () => {
            const response = await fetch("/api/v1/profiles/", {
                headers: getAuthHeaders(),
            });
            if (!response.ok) throw new Error('Failed to load profiles');
            const profiles: Profile[] = await response.json();
            return sortProfilesByName(profiles);
        }
    });
}

export function useQuickTranscription() {
    const { getAuthHeaders } = useAuth();
    return useMutation({
        mutationFn: async ({ file, profileName, onProgress }: { file: File, profileName?: string, onProgress?: (progress: UploadProgressInfo) => void }) => {
            return uploadResumable({
                kind: 'quick',
                title: file.name.replace(/\.[^/.]+$/, ''),
                profileName,
                files: [{
                    id: 'audio',
                    role: 'audio',
                    file,
                }],
                headers: getAuthHeaders(),
                onProgress,
            });
        }
    });
}

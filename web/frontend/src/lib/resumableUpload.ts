type UploadKind = "audio" | "video" | "quick" | "multitrack" | "submit";
type UploadRole = "audio" | "video" | "aup" | "track";

export interface ResumableUploadFile {
    id: string;
    role: UploadRole;
    file: File;
}

export interface UploadProgressInfo {
    uploadedBytes: number;
    totalBytes: number;
    percentage: number;
}

export interface ResumableUploadOptions {
    kind: UploadKind;
    title?: string;
    profileName?: string;
    parameters?: unknown;
    files: ResumableUploadFile[];
    headers: Record<string, string>;
    onProgress?: (progress: UploadProgressInfo) => void;
}

interface UploadSessionFileStatus {
    id: string;
    role: UploadRole;
    name: string;
    size: number;
    chunk_count: number;
    received_bytes: number;
    accepted_chunks: number[];
    missing_chunks: number[];
}

interface UploadSessionResponse {
    id: string;
    kind: UploadKind;
    status: "active" | "completed" | "cancelled";
    token?: string;
    chunk_size: number;
    expires_at: string;
    result_id?: string;
    files: UploadSessionFileStatus[];
}

interface CachedUploadSession {
    id: string;
    token: string;
    cacheKey: string;
    createdAt: number;
}

const CACHE_PREFIX = "scriberr.resumableUpload.";
const MAX_RETRIES = 5;

export async function uploadResumable(options: ResumableUploadOptions): Promise<unknown> {
    const totalBytes = options.files.reduce((sum, item) => sum + item.file.size, 0);
    const session = await getOrCreateSession(options);
    let status = await fetchSessionStatus(session.id, options.headers);
    let uploadedBytes = acceptedBytes(options.files, status);
    options.onProgress?.(toProgress(uploadedBytes, totalBytes));

    for (const uploadFile of options.files) {
        let fileStatus = findStatus(status, uploadFile.id);
        const accepted = new Set(fileStatus.accepted_chunks);

        for (let index = 0; index < fileStatus.chunk_count; index += 1) {
            if (accepted.has(index)) continue;

            const start = index * status.chunk_size;
            const endExclusive = Math.min(start + status.chunk_size, uploadFile.file.size);
            const chunk = uploadFile.file.slice(start, endExclusive);

            try {
                await uploadChunkWithRetry({
                    sessionId: session.id,
                    token: session.token,
                    fileId: uploadFile.id,
                    chunkIndex: index,
                    chunk,
                    start,
                    endExclusive,
                    totalSize: uploadFile.file.size,
                    headers: options.headers,
                });
                accepted.add(index);
                uploadedBytes += chunk.size;
                options.onProgress?.(toProgress(uploadedBytes, totalBytes));
            } catch (error) {
                status = await fetchSessionStatus(session.id, options.headers);
                fileStatus = findStatus(status, uploadFile.id);
                if (fileStatus.accepted_chunks.includes(index)) {
                    accepted.add(index);
                    uploadedBytes = acceptedBytes(options.files, status);
                    options.onProgress?.(toProgress(uploadedBytes, totalBytes));
                    continue;
                }
                throw error;
            }
        }
    }

    const result = await completeSession(session.id, session.token, options.headers);
    removeCachedSession(session.cacheKey);
    options.onProgress?.(toProgress(totalBytes, totalBytes));
    return result;
}

async function getOrCreateSession(options: ResumableUploadOptions): Promise<CachedUploadSession> {
    const cacheKey = buildCacheKey(options);
    const cached = readCachedSession(cacheKey);
    if (cached) {
        try {
            const status = await fetchSessionStatus(cached.id, options.headers);
            if (status.status === "active" && sameFiles(options.files, status.files)) {
                return cached;
            }
        } catch {
            removeCachedSession(cacheKey);
        }
    }

    const response = await fetch("/api/v1/transcription/uploads", {
        method: "POST",
        headers: {
            "Content-Type": "application/json",
            ...options.headers,
        },
        body: JSON.stringify({
            kind: options.kind,
            title: options.title,
            profile_name: options.profileName,
            parameters: options.parameters,
            files: options.files.map((item) => ({
                id: item.id,
                role: item.role,
                name: item.file.name,
                content_type: item.file.type,
                size: item.file.size,
                last_modified: item.file.lastModified,
            })),
        }),
    });

    if (!response.ok) {
        throw new Error(await responseError(response, "Failed to create upload session"));
    }

    const session = (await response.json()) as UploadSessionResponse;
    if (!session.token) {
        throw new Error("Upload session did not return a token");
    }

    const cachedSession: CachedUploadSession = {
        id: session.id,
        token: session.token,
        cacheKey,
        createdAt: Date.now(),
    };
    localStorage.setItem(cacheKey, JSON.stringify(cachedSession));
    return cachedSession;
}

async function fetchSessionStatus(sessionId: string, headers: Record<string, string>): Promise<UploadSessionResponse> {
    const response = await fetch(`/api/v1/transcription/uploads/${sessionId}`, { headers });
    if (!response.ok) throw new Error(await responseError(response, "Failed to fetch upload session"));
    return response.json() as Promise<UploadSessionResponse>;
}

async function uploadChunkWithRetry(args: {
    sessionId: string;
    token: string;
    fileId: string;
    chunkIndex: number;
    chunk: Blob;
    start: number;
    endExclusive: number;
    totalSize: number;
    headers: Record<string, string>;
}) {
    let lastError: unknown;
    for (let attempt = 0; attempt < MAX_RETRIES; attempt += 1) {
        await waitUntilOnline();
        try {
            await uploadChunk(args);
            return;
        } catch (error) {
            lastError = error;
            if (attempt === MAX_RETRIES - 1) break;
            await delay(Math.min(30000, 800 * 2 ** attempt));
        }
    }
    throw lastError instanceof Error ? lastError : new Error("Chunk upload failed");
}

async function uploadChunk(args: {
    sessionId: string;
    token: string;
    fileId: string;
    chunkIndex: number;
    chunk: Blob;
    start: number;
    endExclusive: number;
    totalSize: number;
    headers: Record<string, string>;
}) {
    const hash = await sha256Hex(args.chunk);
    const response = await fetch(`/api/v1/transcription/uploads/${args.sessionId}/files/${args.fileId}/chunks/${args.chunkIndex}`, {
        method: "PUT",
        headers: {
            ...args.headers,
            "Content-Range": `bytes ${args.start}-${args.endExclusive - 1}/${args.totalSize}`,
            "X-Upload-Token": args.token,
            "X-Chunk-SHA256": hash,
        },
        body: args.chunk,
    });

    if (!response.ok) {
        throw new Error(await responseError(response, "Chunk upload failed"));
    }
}

async function completeSession(sessionId: string, token: string, headers: Record<string, string>): Promise<unknown> {
    const response = await fetch(`/api/v1/transcription/uploads/${sessionId}/complete`, {
        method: "POST",
        headers: {
            ...headers,
            "X-Upload-Token": token,
        },
    });
    if (!response.ok) throw new Error(await responseError(response, "Failed to complete upload"));
    return response.json();
}

async function sha256Hex(blob: Blob): Promise<string> {
    const buffer = await blob.arrayBuffer();
    const digest = await crypto.subtle.digest("SHA-256", buffer);
    return Array.from(new Uint8Array(digest))
        .map((byte) => byte.toString(16).padStart(2, "0"))
        .join("");
}

function acceptedBytes(files: ResumableUploadFile[], status: UploadSessionResponse): number {
    return files.reduce((sum, item) => {
        const fileStatus = findStatus(status, item.id);
        return sum + fileStatus.accepted_chunks.reduce((fileSum, index) => {
            const start = index * status.chunk_size;
            const endExclusive = Math.min(start + status.chunk_size, item.file.size);
            return fileSum + Math.max(0, endExclusive - start);
        }, 0);
    }, 0);
}

function findStatus(status: UploadSessionResponse, fileId: string): UploadSessionFileStatus {
    const fileStatus = status.files.find((file) => file.id === fileId);
    if (!fileStatus) throw new Error(`Upload session is missing file ${fileId}`);
    return fileStatus;
}

function sameFiles(files: ResumableUploadFile[], statuses: UploadSessionFileStatus[]): boolean {
    if (files.length !== statuses.length) return false;
    return files.every((item) => {
        const status = statuses.find((file) => file.id === item.id);
        return !!status && status.size === item.file.size && status.name === item.file.name && status.role === item.role;
    });
}

function buildCacheKey(options: ResumableUploadOptions): string {
    const fileKey = options.files
        .map((item) => `${item.id}:${item.role}:${item.file.name}:${item.file.size}:${item.file.lastModified}`)
        .join("|");
    const parametersKey = options.parameters === undefined ? "" : JSON.stringify(options.parameters);
    return `${CACHE_PREFIX}${options.kind}:${options.title || ""}:${options.profileName || ""}:${parametersKey}:${fileKey}`;
}

function readCachedSession(cacheKey: string): CachedUploadSession | null {
    try {
        const raw = localStorage.getItem(cacheKey);
        return raw ? JSON.parse(raw) as CachedUploadSession : null;
    } catch {
        removeCachedSession(cacheKey);
        return null;
    }
}

function removeCachedSession(cacheKey: string) {
    localStorage.removeItem(cacheKey);
}

function toProgress(uploadedBytes: number, totalBytes: number): UploadProgressInfo {
    return {
        uploadedBytes,
        totalBytes,
        percentage: totalBytes > 0 ? Math.min(100, (uploadedBytes / totalBytes) * 100) : 100,
    };
}

async function responseError(response: Response, fallback: string): Promise<string> {
    try {
        const body = await response.json() as { error?: string };
        return body.error || fallback;
    } catch {
        return fallback;
    }
}

function waitUntilOnline(): Promise<void> {
    if (navigator.onLine) return Promise.resolve();
    return new Promise((resolve) => {
        const onOnline = () => {
            window.removeEventListener("online", onOnline);
            resolve();
        };
        window.addEventListener("online", onOnline);
    });
}

function delay(ms: number): Promise<void> {
    return new Promise((resolve) => window.setTimeout(resolve, ms));
}

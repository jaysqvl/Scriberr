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
    const subtle = globalThis.crypto?.subtle;
    const digest = subtle?.digest
        ? new Uint8Array(await subtle.digest("SHA-256", buffer))
        : sha256Bytes(new Uint8Array(buffer));
    return bytesToHex(digest);
}

function bytesToHex(bytes: Uint8Array): string {
    return Array.from(bytes)
        .map((byte) => byte.toString(16).padStart(2, "0"))
        .join("");
}

const SHA256_INITIAL_HASH = [
    0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a,
    0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19,
];

const SHA256_ROUND_CONSTANTS = [
    0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5,
    0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
    0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3,
    0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
    0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc,
    0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
    0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7,
    0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
    0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13,
    0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
    0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3,
    0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
    0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5,
    0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
    0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208,
    0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2,
];

function sha256Bytes(message: Uint8Array): Uint8Array {
    const bitLengthHigh = Math.floor(message.length / 0x20000000);
    const bitLengthLow = (message.length << 3) >>> 0;
    const paddedLength = message.length + 1 + ((64 - ((message.length + 1 + 8) % 64)) % 64) + 8;
    const padded = new Uint8Array(paddedLength);
    padded.set(message);
    padded[message.length] = 0x80;
    padded[paddedLength - 8] = (bitLengthHigh >>> 24) & 0xff;
    padded[paddedLength - 7] = (bitLengthHigh >>> 16) & 0xff;
    padded[paddedLength - 6] = (bitLengthHigh >>> 8) & 0xff;
    padded[paddedLength - 5] = bitLengthHigh & 0xff;
    padded[paddedLength - 4] = (bitLengthLow >>> 24) & 0xff;
    padded[paddedLength - 3] = (bitLengthLow >>> 16) & 0xff;
    padded[paddedLength - 2] = (bitLengthLow >>> 8) & 0xff;
    padded[paddedLength - 1] = bitLengthLow & 0xff;

    const hash = SHA256_INITIAL_HASH.slice();
    const words = new Array<number>(64);

    for (let offset = 0; offset < padded.length; offset += 64) {
        for (let index = 0; index < 16; index += 1) {
            const wordOffset = offset + index * 4;
            words[index] = (
                (padded[wordOffset] << 24) |
                (padded[wordOffset + 1] << 16) |
                (padded[wordOffset + 2] << 8) |
                padded[wordOffset + 3]
            ) >>> 0;
        }

        for (let index = 16; index < 64; index += 1) {
            const s0 = rotateRight(words[index - 15], 7) ^ rotateRight(words[index - 15], 18) ^ (words[index - 15] >>> 3);
            const s1 = rotateRight(words[index - 2], 17) ^ rotateRight(words[index - 2], 19) ^ (words[index - 2] >>> 10);
            words[index] = (words[index - 16] + s0 + words[index - 7] + s1) >>> 0;
        }

        let [a, b, c, d, e, f, g, h] = hash;
        for (let index = 0; index < 64; index += 1) {
            const s1 = rotateRight(e, 6) ^ rotateRight(e, 11) ^ rotateRight(e, 25);
            const ch = (e & f) ^ (~e & g);
            const temp1 = (h + s1 + ch + SHA256_ROUND_CONSTANTS[index] + words[index]) >>> 0;
            const s0 = rotateRight(a, 2) ^ rotateRight(a, 13) ^ rotateRight(a, 22);
            const maj = (a & b) ^ (a & c) ^ (b & c);
            const temp2 = (s0 + maj) >>> 0;

            h = g;
            g = f;
            f = e;
            e = (d + temp1) >>> 0;
            d = c;
            c = b;
            b = a;
            a = (temp1 + temp2) >>> 0;
        }

        hash[0] = (hash[0] + a) >>> 0;
        hash[1] = (hash[1] + b) >>> 0;
        hash[2] = (hash[2] + c) >>> 0;
        hash[3] = (hash[3] + d) >>> 0;
        hash[4] = (hash[4] + e) >>> 0;
        hash[5] = (hash[5] + f) >>> 0;
        hash[6] = (hash[6] + g) >>> 0;
        hash[7] = (hash[7] + h) >>> 0;
    }

    const digest = new Uint8Array(32);
    for (let index = 0; index < hash.length; index += 1) {
        digest[index * 4] = (hash[index] >>> 24) & 0xff;
        digest[index * 4 + 1] = (hash[index] >>> 16) & 0xff;
        digest[index * 4 + 2] = (hash[index] >>> 8) & 0xff;
        digest[index * 4 + 3] = hash[index] & 0xff;
    }
    return digest;
}

function rotateRight(value: number, bits: number): number {
    return (value >>> bits) | (value << (32 - bits));
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

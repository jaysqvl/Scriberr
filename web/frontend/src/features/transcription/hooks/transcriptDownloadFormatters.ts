export interface DownloadTranscriptSegment {
    start: number;
    end: number;
    text: string;
    speaker?: string;
}

export interface DownloadTranscript {
    text: string;
    segments?: DownloadTranscriptSegment[];
}

export interface TranscriptDownloadOptions {
    includeTimestamps: boolean;
    includeSpeakerLabels: boolean;
}

function formatSRTTime(seconds: number): string {
    const hours = Math.floor(seconds / 3600);
    const minutes = Math.floor((seconds % 3600) / 60);
    const secs = Math.floor(seconds % 60);
    const milliseconds = Math.floor((seconds % 1) * 1000);

    return `${hours.toString().padStart(2, "0")}:${minutes.toString().padStart(2, "0")}:${secs.toString().padStart(2, "0")},${milliseconds.toString().padStart(3, "0")}`;
}

function formatTimestamp(seconds: number): string {
    const minutes = Math.floor(seconds / 60);
    const remainingSeconds = Math.floor(seconds % 60);
    return `${minutes}:${remainingSeconds.toString().padStart(2, "0")}`;
}

function getDisplaySpeakerName(originalSpeaker: string, mappings: Record<string, string>): string {
    return mappings[originalSpeaker] || originalSpeaker;
}

export function getUsableTranscriptSegments(transcript?: DownloadTranscript | null): DownloadTranscriptSegment[] {
    if (!Array.isArray(transcript?.segments)) return [];

    return transcript.segments.filter((segment) => segment.text.trim().length > 0);
}

export function getTranscriptText(transcript: DownloadTranscript): string {
    const text = transcript.text.trim();
    if (text) return text;

    return getUsableTranscriptSegments(transcript)
        .map((segment) => segment.text.trim())
        .join(" ");
}

export function formatTranscriptAsSRT(
    transcript: DownloadTranscript,
    speakerMappings: Record<string, string>
): string {
    const segments = getUsableTranscriptSegments(transcript);

    if (segments.length === 0) {
        const text = getTranscriptText(transcript);
        return text ? `1\n00:00:00,000 --> 99:59:59,999\n${text}\n\n` : "";
    }

    return segments.map((segment, index) => {
        let text = segment.text.trim();

        if (segment.speaker) {
            text = `${getDisplaySpeakerName(segment.speaker, speakerMappings)}: ${text}`;
        }

        return `${index + 1}\n${formatSRTTime(segment.start)} --> ${formatSRTTime(segment.end)}\n${text}\n\n`;
    }).join("");
}

export function formatTranscriptAsTXT(
    transcript: DownloadTranscript,
    speakerMappings: Record<string, string>,
    options: TranscriptDownloadOptions
): string {
    const segments = getUsableTranscriptSegments(transcript);

    if ((!options.includeSpeakerLabels && !options.includeTimestamps) || segments.length === 0) {
        return getTranscriptText(transcript);
    }

    return segments.map((segment) => {
        let content = "";

        if (options.includeTimestamps) {
            content += `[${formatTimestamp(segment.start)}] `;
        }

        if (options.includeSpeakerLabels && segment.speaker) {
            content += `${getDisplaySpeakerName(segment.speaker, speakerMappings)}: `;
        }

        return content + segment.text.trim();
    }).join("\n\n");
}

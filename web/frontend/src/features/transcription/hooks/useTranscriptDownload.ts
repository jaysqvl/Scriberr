import type { Transcript } from "@/features/transcription/hooks/useAudioDetail";
import {
    formatTranscriptAsSRT,
    formatTranscriptAsTXT,
    getTranscriptText,
    getUsableTranscriptSegments,
    type TranscriptDownloadOptions,
} from "@/features/transcription/hooks/transcriptDownloadFormatters";

export function useTranscriptDownload() {
    const formatTimestamp = (seconds: number): string => {
        const minutes = Math.floor(seconds / 60);
        const remainingSeconds = Math.floor(seconds % 60);
        return `${minutes}:${remainingSeconds.toString().padStart(2, "0")}`;
    };

    const downloadFile = (content: string, filename: string, contentType: string) => {
        const blob = new Blob([content], { type: contentType });
        const url = URL.createObjectURL(blob);
        const link = document.createElement('a');
        link.href = url;
        link.download = filename;
        document.body.appendChild(link);
        link.click();
        document.body.removeChild(link);
        URL.revokeObjectURL(url);
    };

    const getDisplaySpeakerName = (originalSpeaker: string, mappings: Record<string, string>) => {
        return mappings[originalSpeaker] || originalSpeaker;
    };

    const downloadSRT = (transcript: Transcript, filenameBase: string, speakerMappings: Record<string, string>) => {
        if (!transcript) return;

        const srtContent = formatTranscriptAsSRT(transcript, speakerMappings);
        downloadFile(srtContent, `${filenameBase}.srt`, "application/x-subrip;charset=utf-8");
    };

    const downloadTXT = (
        transcript: Transcript,
        filenameBase: string,
        speakerMappings: Record<string, string>,
        options: TranscriptDownloadOptions
    ) => {
        if (!transcript) return;

        const content = formatTranscriptAsTXT(transcript, speakerMappings, options);
        downloadFile(content, `${filenameBase}.txt`, "text/plain;charset=utf-8");
    };

    const downloadJSON = (
        transcript: Transcript,
        filenameBase: string,
        speakerMappings: Record<string, string>,
        options: TranscriptDownloadOptions
    ) => {
        if (!transcript) return;

        // eslint-disable-next-line @typescript-eslint/no-explicit-any
        let jsonData: any;
        const segments = getUsableTranscriptSegments(transcript);
        const text = getTranscriptText(transcript);

        if (!options.includeSpeakerLabels && !options.includeTimestamps) {
            jsonData = {
                text,
                format: 'simple'
            };
        } else if (segments.length > 0) {
            jsonData = {
                text,
                format: 'segmented',
                segments: segments.map(segment => {
                    // eslint-disable-next-line @typescript-eslint/no-explicit-any
                    const segmentData: any = {
                        text: segment.text.trim()
                    };

                    if (options.includeTimestamps) {
                        segmentData.start = segment.start;
                        segmentData.end = segment.end;
                        segmentData.timestamp = formatTimestamp(segment.start);
                    }

                    if (options.includeSpeakerLabels && segment.speaker) {
                        segmentData.speaker = getDisplaySpeakerName(segment.speaker, speakerMappings);
                    }

                    return segmentData;
                })
            };
        } else {
            jsonData = {
                text,
                format: 'simple'
            };
        }

        downloadFile(JSON.stringify(jsonData, null, 2), `${filenameBase}.json`, 'application/json');
    };

    return { downloadSRT, downloadTXT, downloadJSON };
}

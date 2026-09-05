import { strict as assert } from "node:assert";
import { test } from "node:test";

import {
    formatTranscriptAsSRT,
    formatTranscriptAsTXT,
    type DownloadTranscript,
} from "./transcriptDownloadFormatters.ts";

const timestampFreeTranscript: DownloadTranscript = {
    text: "A transcript returned without timestamp segments.",
    segments: [],
};

test("TXT falls back to transcript text when segments are empty", () => {
    const content = formatTranscriptAsTXT(timestampFreeTranscript, {}, {
        includeSpeakerLabels: true,
        includeTimestamps: true,
    });

    assert.equal(content, timestampFreeTranscript.text);
});

test("SRT falls back to one cue when segments are empty", () => {
    const content = formatTranscriptAsSRT(timestampFreeTranscript, {});

    assert.equal(
        content,
        "1\n00:00:00,000 --> 99:59:59,999\nA transcript returned without timestamp segments.\n\n"
    );
});

test("segment formatting retains timestamps and mapped speaker labels", () => {
    const transcript: DownloadTranscript = {
        text: "Hello there.",
        segments: [{ start: 1.25, end: 3.5, text: " Hello there. ", speaker: "SPEAKER_00" }],
    };

    assert.equal(
        formatTranscriptAsTXT(transcript, { SPEAKER_00: "Alice" }, {
            includeSpeakerLabels: true,
            includeTimestamps: true,
        }),
        "[0:01] Alice: Hello there."
    );
    assert.equal(
        formatTranscriptAsSRT(transcript, { SPEAKER_00: "Alice" }),
        "1\n00:00:01,250 --> 00:00:03,500\nAlice: Hello there.\n\n"
    );
});

test("plain TXT derives content from segments when aggregate text is empty", () => {
    const transcript: DownloadTranscript = {
        text: "",
        segments: [
            { start: 0, end: 1, text: "First sentence." },
            { start: 1, end: 2, text: "Second sentence." },
        ],
    };

    assert.equal(
        formatTranscriptAsTXT(transcript, {}, {
            includeSpeakerLabels: false,
            includeTimestamps: false,
        }),
        "First sentence. Second sentence."
    );
});

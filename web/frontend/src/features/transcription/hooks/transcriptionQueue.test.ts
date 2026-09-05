import assert from "node:assert/strict";
import test from "node:test";
import {
    buildQueueRequest,
    getQueuedItems,
    isStopRunTargetCurrent,
    moveQueuedItemIds,
    reorderQueuedItems,
    sanitizeQueueData,
    shouldRefreshRunArtifacts,
    type TranscriptionQueueItem,
} from "./transcriptionQueue.ts";

const queuedItems: TranscriptionQueueItem[] = [
    queueItem("done", 0, "completed"),
    queueItem("second", 2),
    queueItem("first", 1),
    queueItem("active", 0, "processing"),
];

test("queued items exclude terminal and active records and sort by position", () => {
    assert.deepEqual(getQueuedItems(queuedItems).map((item) => item.id), ["first", "second"]);
});

test("moving a queued item returns the complete server order", () => {
    assert.deepEqual(moveQueuedItemIds(queuedItems, "second", "up"), ["second", "first"]);
    assert.deepEqual(moveQueuedItemIds(queuedItems, "first", "up"), ["first", "second"]);
});

test("optimistic reorder preserves non-queued records", () => {
    const reordered = reorderQueuedItems(queuedItems, ["second", "first"]);
    assert.deepEqual(getQueuedItems(reordered).map((item) => item.id), ["second", "first"]);
    assert.equal(reordered.find((item) => item.id === "done")?.status, "completed");
    assert.equal(reordered.find((item) => item.id === "active")?.status, "processing");
});

test("queue requests keep execution credentials in transit and metadata outside parameters", () => {
    const request = buildQueueRequest({
        parameters: {
            model_family: "openai",
            model: "whisper-1",
            api_key: "transient-secret",
        },
        profile_id: "profile-1",
        profile_name: "Cloud transcription",
    });

    assert.equal(request.parameters.api_key, "transient-secret");
    assert.equal(request.profile_id, "profile-1");
    assert.equal(request.profile_name, "Cloud transcription");
    assert.equal("profile_id" in request.parameters, false);
});

test("queue responses are stripped before entering the client query cache", () => {
    const unsafe = queueItem("unsafe", 1);
    unsafe.parameters.api_key = "should-not-be-cached";
    unsafe.parameters.hf_token = "should-not-be-cached";

    const sanitized = sanitizeQueueData({
        job_id: "job-1",
        active_item: { ...unsafe, status: "processing" },
        items: [unsafe],
    });

    assert.equal("api_key" in sanitized.items[0].parameters, false);
    assert.equal("hf_token" in sanitized.items[0].parameters, false);
    assert.equal("api_key" in sanitized.active_item!.parameters, false);
});

test("run artifacts refresh when active queue identity or status changes", () => {
    const processing = {
        audioId: "job-1",
        activeItemId: "queue-1",
        activeItemStatus: "processing",
        parentStatus: "processing",
    };

    assert.equal(shouldRefreshRunArtifacts(undefined, processing), false);
    assert.equal(shouldRefreshRunArtifacts(processing, processing), false);
    assert.equal(shouldRefreshRunArtifacts(processing, {
        ...processing,
        activeItemId: undefined,
        activeItemStatus: undefined,
        parentStatus: "completed",
    }), true);
    assert.equal(shouldRefreshRunArtifacts(processing, {
        ...processing,
        activeItemId: "queue-2",
        activeItemStatus: "pending",
    }), true);
});

test("run artifacts refresh on the final parent terminal transition", () => {
    const processing = {
        audioId: "job-1",
        parentStatus: "processing",
    };

    assert.equal(shouldRefreshRunArtifacts(processing, {
        ...processing,
        parentStatus: "completed",
    }), true);
    assert.equal(shouldRefreshRunArtifacts(processing, {
        audioId: "job-2",
        parentStatus: "completed",
    }), false);
});

test("stop confirmation remains bound to the captured active run", () => {
    assert.equal(isStopRunTargetCurrent(
        { queueItemId: "queue-a", executionRunId: "execution-a" },
        { queueItemId: "queue-a", executionRunId: "execution-a" }
    ), true);
    assert.equal(isStopRunTargetCurrent(
        { queueItemId: "queue-a", executionRunId: "execution-a" },
        { queueItemId: "queue-b", executionRunId: "execution-b" }
    ), false);
    assert.equal(isStopRunTargetCurrent(
        { executionRunId: "legacy-a" },
        { queueItemId: "queue-b", executionRunId: "execution-b" }
    ), false);
});

function queueItem(
    id: string,
    position: number,
    status: TranscriptionQueueItem["status"] = "queued"
): TranscriptionQueueItem {
    return {
        id,
        position,
        status,
        parameters: { model_family: "whisper", model: "small" },
        created_at: "2026-09-05T00:00:00Z",
    };
}

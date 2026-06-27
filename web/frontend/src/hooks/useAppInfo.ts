import { useQuery } from "@tanstack/react-query";

export interface AppInfoResponse {
  status?: string;
  version?: string;
  commit?: string;
  built?: string;
}

export interface AppInfoDisplay {
  version: string;
  commit?: string;
  built?: string;
}

export const unknownAppInfo: AppInfoDisplay = {
  version: "unknown",
};

const hiddenMetadataValues = new Set(["", "none", "unknown"]);

function cleanValue(value: unknown): string | undefined {
  if (typeof value !== "string") {
    return undefined;
  }
  const trimmed = value.trim();
  return hiddenMetadataValues.has(trimmed.toLowerCase()) ? undefined : trimmed;
}

export function normalizeAppInfo(appInfo: AppInfoResponse | null | undefined): AppInfoDisplay {
  const version = cleanValue(appInfo?.version) ?? "unknown";
  const commit = cleanValue(appInfo?.commit);
  const builtValue = cleanValue(appInfo?.built);
  const builtDate = builtValue ? new Date(builtValue) : null;

  return {
    version,
    commit: commit ? commit.slice(0, 12) : undefined,
    built: builtDate && !Number.isNaN(builtDate.getTime()) ? builtDate.toLocaleString() : undefined,
  };
}

async function fetchAppInfo(): Promise<AppInfoResponse> {
  const response = await fetch("/health", {
    headers: {
      Accept: "application/json",
    },
  });

  if (!response.ok) {
    throw new Error(`Failed to load app info: ${response.status}`);
  }

  return response.json();
}

export function useAppInfo() {
  return useQuery({
    queryKey: ["appInfo"],
    queryFn: fetchAppInfo,
    select: normalizeAppInfo,
    staleTime: 5 * 60 * 1000,
    gcTime: 30 * 60 * 1000,
    retry: 1,
    refetchOnWindowFocus: false,
  });
}

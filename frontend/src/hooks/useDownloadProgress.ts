import { useState, useEffect, useRef } from "react";
import { GetDownloadProgress } from "../../wailsjs/go/main/App";

export interface DownloadProgressInfo {
    is_downloading: boolean;
    mb_downloaded: number;
    speed_mbps: number;
}

interface UseDownloadProgressOptions {
    enabled?: boolean;
    intervalMs?: number;
}

function isSameProgress(a: DownloadProgressInfo, b: DownloadProgressInfo) {
    return a.is_downloading === b.is_downloading
        && a.mb_downloaded === b.mb_downloaded
        && a.speed_mbps === b.speed_mbps;
}

export function useDownloadProgress(options?: UseDownloadProgressOptions) {
    const enabled = options?.enabled ?? true;
    const intervalMs = options?.intervalMs ?? 200;
    const [progress, setProgress] = useState<DownloadProgressInfo>({
        is_downloading: false,
        mb_downloaded: 0,
        speed_mbps: 0,
    });
    const intervalRef = useRef<number | null>(null);

    useEffect(() => {
        if (!enabled) {
            return;
        }

        const pollProgress = async () => {
            try {
                const progressInfo = await GetDownloadProgress();
                setProgress((current) => isSameProgress(current, progressInfo) ? current : progressInfo);
            }
            catch (error) {
                console.error("Failed to get download progress:", error);
            }
        };

        intervalRef.current = window.setInterval(pollProgress, intervalMs);
        pollProgress();

        return () => {
            if (intervalRef.current) {
                clearInterval(intervalRef.current);
            }
        };
    }, [enabled, intervalMs]);

    return progress;
}

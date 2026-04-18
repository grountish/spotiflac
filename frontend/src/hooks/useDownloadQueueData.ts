import { useEffect, useState } from "react";
import { GetDownloadQueue } from "../../wailsjs/go/main/App";
import { backend } from "../../wailsjs/go/models";

interface UseDownloadQueueDataOptions {
    enabled?: boolean;
    intervalMs?: number;
}

function hasQueueInfoChanged(current: backend.DownloadQueueInfo, next: backend.DownloadQueueInfo) {
    return current.is_downloading !== next.is_downloading
        || current.current_speed !== next.current_speed
        || current.total_downloaded !== next.total_downloaded
        || current.session_start_time !== next.session_start_time
        || current.queued_count !== next.queued_count
        || current.completed_count !== next.completed_count
        || current.failed_count !== next.failed_count
        || current.skipped_count !== next.skipped_count
        || current.queue.length !== next.queue.length
        || JSON.stringify(current.queue) !== JSON.stringify(next.queue);
}

export function useDownloadQueueData(options?: UseDownloadQueueDataOptions) {
    const enabled = options?.enabled ?? true;
    const intervalMs = options?.intervalMs ?? 200;
    const [queueInfo, setQueueInfo] = useState<backend.DownloadQueueInfo>(new backend.DownloadQueueInfo({
        is_downloading: false,
        queue: [],
        current_speed: 0,
        total_downloaded: 0,
        session_start_time: 0,
        queued_count: 0,
        completed_count: 0,
        failed_count: 0,
        skipped_count: 0,
    }));

    useEffect(() => {
        if (!enabled) {
            return;
        }

        const fetchQueue = async () => {
            try {
                const info = await GetDownloadQueue();
                setQueueInfo((current) => hasQueueInfoChanged(current, info) ? info : current);
            }
            catch (error) {
                console.error("Failed to get download queue:", error);
            }
        };

        fetchQueue();

        const interval = setInterval(fetchQueue, intervalMs);
        return () => clearInterval(interval);
    }, [enabled, intervalMs]);

    return queueInfo;
}

import { useCallback, useEffect, useState } from "react";
import { GetDownloadedAlbums } from "../../wailsjs/go/main/App";
import type { DownloadedFolderSummary } from "@/types/api";

const DEFAULT_LIMIT = 40;

export function useDownloadedAlbums(limit = DEFAULT_LIMIT) {
    const [albums, setAlbums] = useState<DownloadedFolderSummary[]>([]);
    const [isLoading, setIsLoading] = useState(true);

    const refresh = useCallback(async () => {
        setIsLoading(true);
        try {
            const payload = await GetDownloadedAlbums(limit);
            setAlbums(JSON.parse(payload) as DownloadedFolderSummary[]);
        }
        catch (error) {
            console.error("Failed to load downloaded albums:", error);
            setAlbums([]);
        }
        finally {
            setIsLoading(false);
        }
    }, [limit]);

    useEffect(() => {
        void refresh();

        const handleSettingsUpdate = () => {
            void refresh();
        };

        window.addEventListener("settingsUpdated", handleSettingsUpdate);
        return () => window.removeEventListener("settingsUpdated", handleSettingsUpdate);
    }, [refresh]);

    return {
        albums,
        isLoading,
        refresh,
    };
}

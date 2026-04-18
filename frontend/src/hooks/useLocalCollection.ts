import { useCallback, useEffect, useMemo, useState } from "react";
import { DeleteFiles } from "../../wailsjs/go/main/App";
import { toastWithSound as toast } from "@/lib/toast-with-sound";
import { buildPlayableTracks, resolveLocalTrackPaths, type LocalCollectionPlaybackTarget } from "@/lib/local-playback";
import type { TrackMetadata } from "@/types/api";

interface UseLocalCollectionOptions {
    collectionImageUrl?: string;
    collectionKey: string;
    collectionSubtitle?: string;
    collectionTitle: string;
    refreshToken?: string | number;
    tracks: TrackMetadata[];
    folderName?: string;
    isAlbum?: boolean;
    sharedPlayer: SharedLocalCollectionPlayer;
}

export interface SharedLocalCollectionPlayer {
    activeCollectionKey: string | null;
    currentTrackId: string | null;
    isPlaying: boolean;
    loadingTrackId: string | null;
    activateCollection: (collection: LocalCollectionPlaybackTarget, trackId?: string) => Promise<void>;
    syncCollection: (collection: LocalCollectionPlaybackTarget) => void;
    stopPlayback: () => void;
}

export function useLocalCollection({
    collectionImageUrl,
    collectionKey,
    collectionSubtitle,
    collectionTitle,
    refreshToken,
    tracks,
    folderName,
    isAlbum,
    sharedPlayer,
}: UseLocalCollectionOptions) {
    const [localTrackPaths, setLocalTrackPaths] = useState<Map<string, string>>(new Map());
    const [isCheckingFiles, setIsCheckingFiles] = useState(false);
    const [refreshNonce, setRefreshNonce] = useState(0);
    const confirmDeletion = useCallback((message: string) => {
        if (typeof window === "undefined") {
            return true;
        }

        const confirmFn = window.confirm;
        if (typeof confirmFn !== "function") {
            return true;
        }

        try {
            return confirmFn(message);
        }
        catch {
            return true;
        }
    }, []);

    const refreshLocalFiles = useCallback(() => {
        setRefreshNonce((current) => current + 1);
    }, []);

    useEffect(() => {
        let cancelled = false;

        const run = async () => {
            if (!tracks.some((track) => track.spotify_id)) {
                setLocalTrackPaths(new Map());
                return;
            }

            setIsCheckingFiles(true);
            try {
                if (cancelled) {
                    return;
                }

                const nextPaths = await resolveLocalTrackPaths({
                    tracks,
                    folderName,
                    isAlbum,
                });
                if (cancelled) {
                    return;
                }
                setLocalTrackPaths(nextPaths);
            }
            catch (error) {
                if (!cancelled) {
                    console.error("Failed to resolve local track files:", error);
                }
            }
            finally {
                if (!cancelled) {
                    setIsCheckingFiles(false);
                }
            }
        };

        void run();

        return () => {
            cancelled = true;
        };
    }, [tracks, folderName, isAlbum, refreshNonce, refreshToken]);

    const playableTracks = useMemo(
        () => buildPlayableTracks(tracks, localTrackPaths),
        [tracks, localTrackPaths],
    );

    const playbackTarget = useMemo<LocalCollectionPlaybackTarget>(() => ({
        key: collectionKey,
        title: collectionTitle,
        subtitle: collectionSubtitle,
        imageUrl: collectionImageUrl,
        tracks: playableTracks,
    }), [collectionImageUrl, collectionKey, collectionSubtitle, collectionTitle, playableTracks]);

    const isActiveCollection = sharedPlayer.activeCollectionKey === collectionKey;

    useEffect(() => {
        if (isActiveCollection) {
            sharedPlayer.syncCollection(playbackTarget);
        }
    }, [isActiveCollection, playbackTarget, sharedPlayer]);

    const playCollection = useCallback(async () => {
        if (playableTracks.length === 0) {
            return;
        }

        const requestedTrackId = isActiveCollection && sharedPlayer.currentTrackId && playableTracks.some((track) => track.id === sharedPlayer.currentTrackId)
            ? sharedPlayer.currentTrackId
            : playableTracks[0].id;

        await sharedPlayer.activateCollection(playbackTarget, requestedTrackId);
    }, [isActiveCollection, playableTracks, playbackTarget, sharedPlayer]);

    const playTrack = useCallback(async (spotifyId: string) => {
        const track = playableTracks.find((item) => item.id === spotifyId);
        if (!track) {
            return;
        }

        await sharedPlayer.activateCollection(playbackTarget, spotifyId);
    }, [playableTracks, playbackTarget, sharedPlayer]);

    const deleteTrack = useCallback(async (spotifyId: string) => {
        const path = localTrackPaths.get(spotifyId);
        if (!path) {
            return;
        }

        const confirmed = confirmDeletion("Delete this downloaded file?");
        if (!confirmed) {
            return;
        }

        const previousPaths = localTrackPaths;
        const nextPaths = new Map(previousPaths);
        nextPaths.delete(spotifyId);

        try {
            if (isActiveCollection && sharedPlayer.currentTrackId === spotifyId) {
                sharedPlayer.stopPlayback();
            }
            setLocalTrackPaths(nextPaths);
            await DeleteFiles([path]);
            toast.success("Deleted downloaded file");
            refreshLocalFiles();
        }
        catch (error: any) {
            setLocalTrackPaths(previousPaths);
            toast.error("Failed to delete file", {
                description: error?.message || String(error),
            });
        }
    }, [confirmDeletion, isActiveCollection, localTrackPaths, refreshLocalFiles, sharedPlayer]);

    const deleteAll = useCallback(async () => {
        if (playableTracks.length === 0) {
            return;
        }

        const confirmed = confirmDeletion(`Delete ${playableTracks.length} downloaded file${playableTracks.length === 1 ? "" : "s"}?`);
        if (!confirmed) {
            return;
        }

        const previousPaths = localTrackPaths;
        try {
            if (isActiveCollection) {
                sharedPlayer.stopPlayback();
            }
            setLocalTrackPaths(new Map());
            await DeleteFiles(playableTracks.map((track) => track.filePath));
            toast.success(`Deleted ${playableTracks.length} downloaded file${playableTracks.length === 1 ? "" : "s"}`);
            refreshLocalFiles();
        }
        catch (error: any) {
            setLocalTrackPaths(previousPaths);
            toast.error("Failed to delete files", {
                description: error?.message || String(error),
            });
        }
    }, [confirmDeletion, isActiveCollection, localTrackPaths, playableTracks, refreshLocalFiles, sharedPlayer]);

    const trackIds = tracks.filter((track) => track.spotify_id).map((track) => track.spotify_id!) ;
    const allTracksDownloaded = trackIds.length > 0 && trackIds.every((id) => localTrackPaths.has(id));

    return {
        activeCollectionTitle: isActiveCollection ? collectionTitle : null,
        allTracksDownloaded,
        deleteAll,
        deleteTrack,
        isCheckingFiles,
        isCurrentCollectionActive: isActiveCollection,
        localTrackPaths,
        playableTracks,
        playCollection,
        playTrack,
        refreshLocalFiles,
        localLoadingTrackId: isActiveCollection ? sharedPlayer.loadingTrackId : null,
        currentTrackId: isActiveCollection ? sharedPlayer.currentTrackId : null,
        isPlaying: isActiveCollection ? sharedPlayer.isPlaying : false,
    };
}

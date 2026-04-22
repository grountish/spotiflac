import { useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import { getSettings, type Settings } from "@/lib/settings";
import { toastWithSound as toast } from "@/lib/toast-with-sound";

export interface LocalAudioTrack {
    id: string;
    title: string;
    subtitle?: string;
    imageUrl?: string;
    filePath: string;
}

interface UseLocalAudioPlayerOptions {
    resolveSource: (filePath: string) => Promise<string>;
}

export interface PlaybackProgressSnapshot {
    currentTime: number;
    duration: number;
}

export interface LocalAudioPlayerProgressStore {
    getSnapshot: () => PlaybackProgressSnapshot;
    subscribe: (listener: () => void) => () => void;
}

export type RepeatMode = "off" | "all" | "one";

const PROGRESS_EMIT_INTERVAL_MS = 500;
const CROSSFADE_STEP_INTERVAL_MS = 100;
const PLAYER_VOLUME_KEY = "spotiflac-local-player-volume";
const PLAYER_REPEAT_KEY = "spotiflac-local-player-repeat";
const PLAYER_SHUFFLE_KEY = "spotiflac-local-player-shuffle";

function disposeAudio(audio: HTMLAudioElement | null) {
    if (!audio) {
        return;
    }

    audio.pause();
    audio.onloadedmetadata = null;
    audio.ontimeupdate = null;
    audio.onplay = null;
    audio.onpause = null;
    audio.onended = null;
    audio.onerror = null;
    audio.removeAttribute("src");
    audio.load();
}

function readStoredNumber(key: string, fallback: number) {
    if (typeof window === "undefined") {
        return fallback;
    }

    const value = Number(window.localStorage.getItem(key));
    return Number.isFinite(value) ? Math.min(Math.max(value, 0), 1) : fallback;
}

function readStoredBoolean(key: string, fallback: boolean) {
    if (typeof window === "undefined") {
        return fallback;
    }

    const value = window.localStorage.getItem(key);
    if (value === "true") {
        return true;
    }
    if (value === "false") {
        return false;
    }
    return fallback;
}

function readStoredRepeatMode(): RepeatMode {
    if (typeof window === "undefined") {
        return "off";
    }

    const value = window.localStorage.getItem(PLAYER_REPEAT_KEY);
    if (value === "all" || value === "one") {
        return value;
    }
    return "off";
}

function persistPlayerPreference(key: string, value: string) {
    if (typeof window === "undefined") {
        return;
    }

    window.localStorage.setItem(key, value);
}

function shuffleIds(ids: string[]) {
    const next = [...ids];
    for (let index = next.length - 1; index > 0; index -= 1) {
        const swapIndex = Math.floor(Math.random() * (index + 1));
        [next[index], next[swapIndex]] = [next[swapIndex], next[index]];
    }
    return next;
}

function buildQueueTrackIds(tracks: LocalAudioTrack[], shuffleEnabled: boolean, currentTrackId?: string | null) {
    const ids = tracks.map((track) => track.id);
    if (!shuffleEnabled || ids.length <= 1) {
        return ids;
    }

    if (currentTrackId && ids.includes(currentTrackId)) {
        const remaining = shuffleIds(ids.filter((id) => id !== currentTrackId));
        return [currentTrackId, ...remaining];
    }

    return shuffleIds(ids);
}

export function useLocalAudioPlayer({ resolveSource }: UseLocalAudioPlayerOptions) {
    const [tracks, setTracks] = useState<LocalAudioTrack[]>([]);
    const [queue, setQueue] = useState<LocalAudioTrack[]>([]);
    const [currentTrackId, setCurrentTrackId] = useState<string | null>(null);
    const [loadingTrackId, setLoadingTrackId] = useState<string | null>(null);
    const [isPlaying, setIsPlaying] = useState(false);
    const [volume, setVolumeState] = useState(() => readStoredNumber(PLAYER_VOLUME_KEY, 1));
    const [shuffleEnabled, setShuffleEnabledState] = useState(() => readStoredBoolean(PLAYER_SHUFFLE_KEY, false));
    const [repeatMode, setRepeatModeState] = useState<RepeatMode>(() => readStoredRepeatMode());

    const audioRef = useRef<HTMLAudioElement | null>(null);
    const fadingAudioRef = useRef<HTMLAudioElement | null>(null);
    const currentTrackIdRef = useRef<string | null>(null);
    const currentQueueIndexRef = useRef(-1);
    const queueTrackIdsRef = useRef<string[]>([]);
    const tracksRef = useRef<LocalAudioTrack[]>(tracks);
    const requestTokenRef = useRef(0);
    const lastProgressEmitAtRef = useRef(0);
    const startCrossfadeRef = useRef<() => void>(() => {});
    const crossfadeIntervalRef = useRef<number | null>(null);
    const isCrossfadingRef = useRef(false);
    const crossfadeEnabledRef = useRef(false);
    const crossfadeDurationSecondsRef = useRef(6);
    const crossfadeProgressRef = useRef(0);
    const volumeRef = useRef(volume);
    const shuffleEnabledRef = useRef(shuffleEnabled);
    const repeatModeRef = useRef<RepeatMode>(repeatMode);
    const progressRef = useRef<PlaybackProgressSnapshot>({
        currentTime: 0,
        duration: 0,
    });
    const progressListenersRef = useRef(new Set<() => void>());

    const emitProgress = useCallback(() => {
        for (const listener of progressListenersRef.current) {
            listener();
        }
    }, []);

    const updateProgress = useCallback((nextProgress: PlaybackProgressSnapshot, force = false) => {
        const current = progressRef.current;
        if (!force && current.currentTime === nextProgress.currentTime && current.duration === nextProgress.duration) {
            return;
        }

        progressRef.current = nextProgress;
        emitProgress();
    }, [emitProgress]);

    const clearCrossfadeInterval = useCallback(() => {
        if (crossfadeIntervalRef.current !== null) {
            window.clearInterval(crossfadeIntervalRef.current);
            crossfadeIntervalRef.current = null;
        }
    }, []);

    const getTrackByQueueIndex = useCallback((queueIndex: number) => {
        const trackId = queueTrackIdsRef.current[queueIndex];
        if (!trackId) {
            return null;
        }

        return tracksRef.current.find((track) => track.id === trackId) ?? null;
    }, []);

    const getNextQueueIndex = useCallback((origin: "manual" | "auto") => {
        const total = queueTrackIdsRef.current.length;
        if (total === 0) {
            return null;
        }

        const currentIndex = currentQueueIndexRef.current;
        if (currentIndex < 0) {
            return 0;
        }

        if (origin === "auto" && repeatModeRef.current === "one") {
            return currentIndex;
        }

        if (currentIndex < total - 1) {
            return currentIndex + 1;
        }

        return repeatModeRef.current === "all" ? 0 : null;
    }, []);

    const getPreviousQueueIndex = useCallback(() => {
        const total = queueTrackIdsRef.current.length;
        if (total === 0) {
            return null;
        }

        const currentIndex = currentQueueIndexRef.current;
        if (currentIndex > 0) {
            return currentIndex - 1;
        }

        if (repeatModeRef.current === "all" && total > 1) {
            return total - 1;
        }

        return currentIndex >= 0 ? currentIndex : 0;
    }, []);

    const syncQueueOrder = useCallback((nextTracks: LocalAudioTrack[], options?: {
        currentTrackId?: string | null;
        shuffleEnabled?: boolean;
    }) => {
        const currentId = options?.currentTrackId ?? currentTrackIdRef.current;
        const nextShuffle = options?.shuffleEnabled ?? shuffleEnabledRef.current;
        const nextTrackIds = buildQueueTrackIds(nextTracks, nextShuffle, currentId);
        const trackMap = new Map(nextTracks.map((track) => [track.id, track]));
        queueTrackIdsRef.current = nextTrackIds;
        setQueue(nextTrackIds
            .map((trackId) => trackMap.get(trackId))
            .filter((track): track is LocalAudioTrack => !!track));
        currentQueueIndexRef.current = currentId ? nextTrackIds.indexOf(currentId) : -1;
    }, []);

    const applyOutputVolumes = useCallback((forcedVolume?: number) => {
        const targetVolume = Math.min(Math.max(forcedVolume ?? volumeRef.current, 0), 1);

        if (isCrossfadingRef.current && audioRef.current && fadingAudioRef.current) {
            const progress = Math.min(Math.max(crossfadeProgressRef.current, 0), 1);
            audioRef.current.volume = targetVolume * progress;
            fadingAudioRef.current.volume = targetVolume * Math.max(0, 1 - progress);
            return;
        }

        if (audioRef.current) {
            audioRef.current.volume = targetVolume;
        }
    }, []);

    const disposeFadingAudio = useCallback(() => {
        clearCrossfadeInterval();
        crossfadeProgressRef.current = 0;
        if (fadingAudioRef.current) {
            disposeAudio(fadingAudioRef.current);
            fadingAudioRef.current = null;
        }
        isCrossfadingRef.current = false;
    }, [clearCrossfadeInterval]);

    const applyCrossfadeSettings = useCallback((settings?: Partial<Settings>) => {
        const resolvedSettings = settings ?? getSettings();
        crossfadeEnabledRef.current = !!resolvedSettings.enableCrossfade;
        const duration = Number(resolvedSettings.crossfadeDurationSeconds);
        crossfadeDurationSecondsRef.current = Number.isFinite(duration) && duration > 0 ? duration : 6;
    }, []);

    const stopPlayback = useCallback(() => {
        requestTokenRef.current += 1;
        disposeFadingAudio();
        disposeAudio(audioRef.current);
        audioRef.current = null;
        currentTrackIdRef.current = null;
        currentQueueIndexRef.current = -1;
        setCurrentTrackId(null);
        setLoadingTrackId(null);
        setIsPlaying(false);
        lastProgressEmitAtRef.current = 0;
        updateProgress({
            currentTime: 0,
            duration: 0,
        }, true);
    }, [disposeFadingAudio, updateProgress]);

    const setTrackList = useCallback((nextTracks: LocalAudioTrack[]) => {
        tracksRef.current = nextTracks;
        setTracks(nextTracks);
        syncQueueOrder(nextTracks);

        if (currentTrackIdRef.current && !nextTracks.some((track) => track.id === currentTrackIdRef.current)) {
            stopPlayback();
        }
    }, [stopPlayback, syncQueueOrder]);

    const playTrackByQueueIndex = useCallback(async (queueIndex: number | null) => {
        if (queueIndex === null) {
            return;
        }

        const nextTrack = getTrackByQueueIndex(queueIndex);
        if (!nextTrack) {
            stopPlayback();
            return;
        }

        currentQueueIndexRef.current = queueIndex;
        const requestToken = ++requestTokenRef.current;
        disposeFadingAudio();
        setLoadingTrackId(nextTrack.id);
        disposeAudio(audioRef.current);
        audioRef.current = null;
        currentTrackIdRef.current = nextTrack.id;
        setCurrentTrackId(nextTrack.id);
        setIsPlaying(false);
        lastProgressEmitAtRef.current = 0;
        updateProgress({
            currentTime: 0,
            duration: 0,
        }, true);

        try {
            const src = await resolveSource(nextTrack.filePath);
            if (requestToken !== requestTokenRef.current) {
                return;
            }

            const audio = new Audio();
            audio.preload = "none";
            audio.src = src;
            audioRef.current = audio;

            audio.onloadedmetadata = () => {
                if (requestToken !== requestTokenRef.current || audio !== audioRef.current) {
                    return;
                }

                updateProgress({
                    currentTime: audio.currentTime || 0,
                    duration: Number.isFinite(audio.duration) ? audio.duration : 0,
                }, true);
            };

            audio.ontimeupdate = () => {
                if (requestToken !== requestTokenRef.current || audio !== audioRef.current) {
                    return;
                }

                const now = Date.now();
                if (now - lastProgressEmitAtRef.current >= PROGRESS_EMIT_INTERVAL_MS) {
                    lastProgressEmitAtRef.current = now;
                    updateProgress({
                        currentTime: audio.currentTime || 0,
                        duration: Number.isFinite(audio.duration) ? audio.duration : 0,
                    });
                }

                const remaining = Number.isFinite(audio.duration) ? audio.duration - audio.currentTime : Number.POSITIVE_INFINITY;
                if (!audio.paused && remaining <= crossfadeDurationSecondsRef.current + 0.25) {
                    startCrossfadeRef.current();
                }
            };

            audio.onplay = () => {
                if (requestToken !== requestTokenRef.current || audio !== audioRef.current) {
                    return;
                }

                setIsPlaying(true);
                setLoadingTrackId(null);
            };

            audio.onpause = () => {
                if (requestToken !== requestTokenRef.current || audio !== audioRef.current) {
                    return;
                }

                setIsPlaying(false);
            };

            audio.onerror = () => {
                if (requestToken !== requestTokenRef.current || audio !== audioRef.current) {
                    return;
                }

                toast.error("Failed to play downloaded track", {
                    description: `Could not load "${nextTrack.title}" from disk.`,
                });
                stopPlayback();
            };

            audio.onended = () => {
                if (requestToken !== requestTokenRef.current || audio !== audioRef.current || isCrossfadingRef.current) {
                    return;
                }

                const nextQueueIndex = getNextQueueIndex("auto");
                if (nextQueueIndex !== null && (repeatModeRef.current !== "off" || nextQueueIndex !== currentQueueIndexRef.current)) {
                    void playTrackByQueueIndex(nextQueueIndex);
                    return;
                }

                setIsPlaying(false);
                setLoadingTrackId(null);
                updateProgress({
                    currentTime: Number.isFinite(audio.duration) ? audio.duration : audio.currentTime || 0,
                    duration: Number.isFinite(audio.duration) ? audio.duration : 0,
                }, true);
            };

            applyOutputVolumes();
            await audio.play();
        }
        catch (error: any) {
            if (requestToken !== requestTokenRef.current) {
                return;
            }

            toast.error("Failed to play downloaded track", {
                description: error?.message || `Could not load "${nextTrack.title}" from disk.`,
            });
            stopPlayback();
        }
        finally {
            if (requestToken === requestTokenRef.current) {
                setLoadingTrackId((current) => (current === nextTrack.id ? null : current));
            }
        }
    }, [applyOutputVolumes, disposeFadingAudio, getNextQueueIndex, getTrackByQueueIndex, resolveSource, stopPlayback, updateProgress]);

    const startCrossfade = useCallback(async () => {
        const currentTrackId = currentTrackIdRef.current;
        const activeAudio = audioRef.current;
        const requestToken = requestTokenRef.current;
        const nextQueueIndex = getNextQueueIndex("auto");

        if (!crossfadeEnabledRef.current || repeatModeRef.current === "one" || !activeAudio || !currentTrackId || isCrossfadingRef.current || nextQueueIndex === null) {
            return;
        }

        const nextTrack = getTrackByQueueIndex(nextQueueIndex);
        if (!nextTrack || nextTrack.id === currentTrackId) {
            return;
        }

        const durationSeconds = crossfadeDurationSecondsRef.current;
        if (!Number.isFinite(activeAudio.duration) || activeAudio.duration <= 0) {
            return;
        }

        if (activeAudio.duration - activeAudio.currentTime > durationSeconds) {
            return;
        }

        isCrossfadingRef.current = true;
        crossfadeProgressRef.current = 0;
        setLoadingTrackId(nextTrack.id);

        try {
            const src = await resolveSource(nextTrack.filePath);
            if (requestToken !== requestTokenRef.current || audioRef.current !== activeAudio) {
                isCrossfadingRef.current = false;
                crossfadeProgressRef.current = 0;
                return;
            }

            const nextAudio = new Audio();
            nextAudio.preload = "auto";
            nextAudio.src = src;
            nextAudio.volume = 0;
            await nextAudio.play();

            if (requestToken !== requestTokenRef.current || audioRef.current !== activeAudio) {
                disposeAudio(nextAudio);
                isCrossfadingRef.current = false;
                crossfadeProgressRef.current = 0;
                return;
            }

            fadingAudioRef.current = activeAudio;
            audioRef.current = nextAudio;
            currentTrackIdRef.current = nextTrack.id;
            currentQueueIndexRef.current = nextQueueIndex;
            setCurrentTrackId(nextTrack.id);
            setIsPlaying(true);
            lastProgressEmitAtRef.current = 0;
            updateProgress({
                currentTime: nextAudio.currentTime || 0,
                duration: Number.isFinite(nextAudio.duration) ? nextAudio.duration : 0,
            }, true);

            nextAudio.onloadedmetadata = () => {
                if (requestToken !== requestTokenRef.current || nextAudio !== audioRef.current) {
                    return;
                }

                updateProgress({
                    currentTime: nextAudio.currentTime || 0,
                    duration: Number.isFinite(nextAudio.duration) ? nextAudio.duration : 0,
                }, true);
            };

            nextAudio.ontimeupdate = () => {
                if (requestToken !== requestTokenRef.current || nextAudio !== audioRef.current) {
                    return;
                }

                const now = Date.now();
                if (now - lastProgressEmitAtRef.current >= PROGRESS_EMIT_INTERVAL_MS) {
                    lastProgressEmitAtRef.current = now;
                    updateProgress({
                        currentTime: nextAudio.currentTime || 0,
                        duration: Number.isFinite(nextAudio.duration) ? nextAudio.duration : 0,
                    });
                }

                const remaining = Number.isFinite(nextAudio.duration) ? nextAudio.duration - nextAudio.currentTime : Number.POSITIVE_INFINITY;
                if (!nextAudio.paused && remaining <= crossfadeDurationSecondsRef.current + 0.25) {
                    startCrossfadeRef.current();
                }
            };

            nextAudio.onplay = () => {
                if (requestToken !== requestTokenRef.current || nextAudio !== audioRef.current) {
                    return;
                }

                setIsPlaying(true);
                setLoadingTrackId(null);
            };

            nextAudio.onpause = () => {
                if (requestToken !== requestTokenRef.current || nextAudio !== audioRef.current) {
                    return;
                }

                setIsPlaying(false);
            };

            nextAudio.onended = () => {
                if (requestToken !== requestTokenRef.current || nextAudio !== audioRef.current || isCrossfadingRef.current) {
                    return;
                }

                const nextIndex = getNextQueueIndex("auto");
                if (nextIndex !== null && (repeatModeRef.current !== "off" || nextIndex !== currentQueueIndexRef.current)) {
                    void playTrackByQueueIndex(nextIndex);
                    return;
                }

                setIsPlaying(false);
                updateProgress({
                    currentTime: Number.isFinite(nextAudio.duration) ? nextAudio.duration : nextAudio.currentTime || 0,
                    duration: Number.isFinite(nextAudio.duration) ? nextAudio.duration : 0,
                }, true);
            };

            nextAudio.onerror = () => {
                if (requestToken !== requestTokenRef.current || nextAudio !== audioRef.current) {
                    return;
                }

                toast.error("Crossfade failed", {
                    description: `Could not load "${nextTrack.title}".`,
                });
                stopPlayback();
            };

            activeAudio.onloadedmetadata = null;
            activeAudio.ontimeupdate = null;
            activeAudio.onplay = null;
            activeAudio.onpause = null;
            activeAudio.onended = null;
            activeAudio.onerror = null;

            clearCrossfadeInterval();
            const fadeStartedAt = Date.now();
            const fadeDurationMs = Math.max(durationSeconds * 1000, CROSSFADE_STEP_INTERVAL_MS);
            crossfadeIntervalRef.current = window.setInterval(() => {
                if (requestToken !== requestTokenRef.current) {
                    disposeFadingAudio();
                    return;
                }

                const elapsed = Date.now() - fadeStartedAt;
                const progress = Math.min(elapsed / fadeDurationMs, 1);
                crossfadeProgressRef.current = progress;
                applyOutputVolumes();

                if (progress >= 1) {
                    disposeAudio(activeAudio);
                    fadingAudioRef.current = null;
                    clearCrossfadeInterval();
                    isCrossfadingRef.current = false;
                    crossfadeProgressRef.current = 0;
                    applyOutputVolumes();
                }
            }, CROSSFADE_STEP_INTERVAL_MS);
        }
        catch (error: any) {
            isCrossfadingRef.current = false;
            crossfadeProgressRef.current = 0;
            toast.error("Crossfade failed", {
                description: error?.message || `Could not start "${nextTrack.title}".`,
            });
        }
        finally {
            if (requestToken === requestTokenRef.current) {
                setLoadingTrackId((current) => (current === nextTrack.id ? null : current));
            }
        }
    }, [applyOutputVolumes, clearCrossfadeInterval, disposeFadingAudio, getNextQueueIndex, getTrackByQueueIndex, playTrackByQueueIndex, resolveSource, stopPlayback, updateProgress]);

    useEffect(() => {
        startCrossfadeRef.current = () => {
            void startCrossfade();
        };
    }, [startCrossfade]);

    useEffect(() => {
        applyCrossfadeSettings();

        const handleSettingsUpdate = (event: Event) => {
            applyCrossfadeSettings((event as CustomEvent<Partial<Settings>>).detail);
        };

        window.addEventListener("settingsUpdated", handleSettingsUpdate);
        return () => window.removeEventListener("settingsUpdated", handleSettingsUpdate);
    }, [applyCrossfadeSettings]);

    const setVolume = useCallback((nextVolume: number) => {
        const boundedVolume = Math.min(Math.max(nextVolume, 0), 1);
        volumeRef.current = boundedVolume;
        setVolumeState(boundedVolume);
        persistPlayerPreference(PLAYER_VOLUME_KEY, String(boundedVolume));
        applyOutputVolumes(boundedVolume);
    }, [applyOutputVolumes]);

    const toggleShuffle = useCallback(() => {
        const nextShuffle = !shuffleEnabledRef.current;
        shuffleEnabledRef.current = nextShuffle;
        setShuffleEnabledState(nextShuffle);
        persistPlayerPreference(PLAYER_SHUFFLE_KEY, String(nextShuffle));
        syncQueueOrder(tracksRef.current, {
            currentTrackId: currentTrackIdRef.current,
            shuffleEnabled: nextShuffle,
        });
    }, [syncQueueOrder]);

    const cycleRepeatMode = useCallback(() => {
        const nextRepeatMode: RepeatMode = repeatModeRef.current === "off"
            ? "all"
            : repeatModeRef.current === "all"
                ? "one"
                : "off";
        repeatModeRef.current = nextRepeatMode;
        setRepeatModeState(nextRepeatMode);
        persistPlayerPreference(PLAYER_REPEAT_KEY, nextRepeatMode);
    }, []);

    const toggleTrack = useCallback(async (track: LocalAudioTrack) => {
        if (currentTrackIdRef.current === track.id && audioRef.current) {
            if (audioRef.current.paused) {
                try {
                    setLoadingTrackId(track.id);
                    await audioRef.current.play();
                }
                catch (error: any) {
                    toast.error("Playback failed", {
                        description: error?.message || `Could not resume "${track.title}".`,
                    });
                }
                finally {
                    setLoadingTrackId((current) => (current === track.id ? null : current));
                }
            }
            else {
                disposeFadingAudio();
                audioRef.current.pause();
            }
            return;
        }

        const queueIndex = queueTrackIdsRef.current.indexOf(track.id);
        if (queueIndex === -1) {
            return;
        }

        await playTrackByQueueIndex(queueIndex);
    }, [disposeFadingAudio, playTrackByQueueIndex]);

    const playNext = useCallback(async () => {
        const nextQueueIndex = getNextQueueIndex("manual");
        if (nextQueueIndex === null) {
            return;
        }

        await playTrackByQueueIndex(nextQueueIndex);
    }, [getNextQueueIndex, playTrackByQueueIndex]);

    const playPrevious = useCallback(async () => {
        const audio = audioRef.current;
        if (audio && audio.currentTime > 3) {
            audio.currentTime = 0;
            updateProgress({
                currentTime: 0,
                duration: Number.isFinite(audio.duration) ? audio.duration : 0,
            }, true);
            return;
        }

        const previousQueueIndex = getPreviousQueueIndex();
        if (previousQueueIndex === null) {
            return;
        }

        await playTrackByQueueIndex(previousQueueIndex);
    }, [getPreviousQueueIndex, playTrackByQueueIndex, updateProgress]);

    const seekTo = useCallback((nextTime: number) => {
        const audio = audioRef.current;
        if (!audio || !Number.isFinite(nextTime)) {
            return;
        }

        disposeFadingAudio();
        const boundedTime = Math.min(Math.max(nextTime, 0), Number.isFinite(audio.duration) ? audio.duration : nextTime);
        audio.currentTime = boundedTime;
        lastProgressEmitAtRef.current = Date.now();
        updateProgress({
            currentTime: boundedTime,
            duration: Number.isFinite(audio.duration) ? audio.duration : 0,
        }, true);
    }, [disposeFadingAudio, updateProgress]);

    const seekBy = useCallback((deltaSeconds: number) => {
        const audio = audioRef.current;
        if (!audio || !Number.isFinite(deltaSeconds)) {
            return;
        }

        seekTo((audio.currentTime || 0) + deltaSeconds);
    }, [seekTo]);

    useEffect(() => {
        volumeRef.current = volume;
    }, [volume]);

    useEffect(() => {
        shuffleEnabledRef.current = shuffleEnabled;
    }, [shuffleEnabled]);

    useEffect(() => {
        repeatModeRef.current = repeatMode;
    }, [repeatMode]);

    useEffect(() => {
        return () => {
            requestTokenRef.current += 1;
            disposeFadingAudio();
            disposeAudio(audioRef.current);
            audioRef.current = null;
        };
    }, [disposeFadingAudio]);

    const currentTrack = useMemo(() => {
        if (!currentTrackId) {
            return null;
        }

        return tracks.find((track) => track.id === currentTrackId) ?? null;
    }, [currentTrackId, tracks]);

    const progressStore = useMemo<LocalAudioPlayerProgressStore>(() => ({
        getSnapshot: () => progressRef.current,
        subscribe: (listener) => {
            progressListenersRef.current.add(listener);
            return () => {
                progressListenersRef.current.delete(listener);
            };
        },
    }), []);

    return {
        currentTrack,
        currentTrackId,
        isPlaying,
        loadingTrackId,
        playNext,
        playPrevious,
        progressStore,
        queue,
        repeatMode,
        seekBy,
        seekTo,
        setTrackList,
        setVolume,
        shuffleEnabled,
        stopPlayback,
        toggleShuffle,
        tracks,
        toggleTrack,
        volume,
        cycleRepeatMode,
    };
}

export function useLocalAudioPlayerProgress(progressStore: LocalAudioPlayerProgressStore) {
    return useSyncExternalStore(
        progressStore.subscribe,
        progressStore.getSnapshot,
        progressStore.getSnapshot,
    );
}

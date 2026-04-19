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

const PROGRESS_EMIT_INTERVAL_MS = 500;
const CROSSFADE_STEP_INTERVAL_MS = 100;

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

export function useLocalAudioPlayer({ resolveSource }: UseLocalAudioPlayerOptions) {
    const [tracks, setTracks] = useState<LocalAudioTrack[]>([]);
    const [currentTrackId, setCurrentTrackId] = useState<string | null>(null);
    const [loadingTrackId, setLoadingTrackId] = useState<string | null>(null);
    const [isPlaying, setIsPlaying] = useState(false);

    const audioRef = useRef<HTMLAudioElement | null>(null);
    const fadingAudioRef = useRef<HTMLAudioElement | null>(null);
    const currentTrackIdRef = useRef<string | null>(null);
    const tracksRef = useRef<LocalAudioTrack[]>(tracks);
    const requestTokenRef = useRef(0);
    const lastProgressEmitAtRef = useRef(0);
    const startCrossfadeRef = useRef<() => void>(() => {});
    const crossfadeIntervalRef = useRef<number | null>(null);
    const isCrossfadingRef = useRef(false);
    const crossfadeEnabledRef = useRef(false);
    const crossfadeDurationSecondsRef = useRef(6);
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

    const disposeFadingAudio = useCallback(() => {
        clearCrossfadeInterval();
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

        if (currentTrackIdRef.current && !nextTracks.some((track) => track.id === currentTrackIdRef.current)) {
            stopPlayback();
        }
    }, [stopPlayback]);

    const bindActiveAudioHandlers = useCallback((audio: HTMLAudioElement, track: LocalAudioTrack, requestToken: number) => {
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
                description: `Could not load "${track.title}" from disk.`,
            });
            stopPlayback();
        };

        audio.onended = () => {
            if (requestToken !== requestTokenRef.current || audio !== audioRef.current || isCrossfadingRef.current) {
                return;
            }

            const currentIndex = tracksRef.current.findIndex((candidate) => candidate.id === track.id);
            void playTrackByIndex(currentIndex + 1);
        };
    }, [stopPlayback, updateProgress]);

    const startCrossfade = useCallback(async () => {
        const currentTrackId = currentTrackIdRef.current;
        const activeAudio = audioRef.current;
        const requestToken = requestTokenRef.current;

        if (!crossfadeEnabledRef.current || !activeAudio || !currentTrackId || isCrossfadingRef.current) {
            return;
        }

        const currentIndex = tracksRef.current.findIndex((track) => track.id === currentTrackId);
        const nextTrack = currentIndex >= 0 ? tracksRef.current[currentIndex + 1] : null;
        if (!nextTrack) {
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
        setLoadingTrackId(nextTrack.id);

        try {
            const src = await resolveSource(nextTrack.filePath);
            if (requestToken !== requestTokenRef.current || audioRef.current !== activeAudio) {
                isCrossfadingRef.current = false;
                return;
            }

            const nextAudio = new Audio();
            nextAudio.preload = "auto";
            nextAudio.volume = 0;
            nextAudio.src = src;
            await nextAudio.play();

            if (requestToken !== requestTokenRef.current || audioRef.current !== activeAudio) {
                disposeAudio(nextAudio);
                isCrossfadingRef.current = false;
                return;
            }

            fadingAudioRef.current = activeAudio;
            audioRef.current = nextAudio;
            currentTrackIdRef.current = nextTrack.id;
            setCurrentTrackId(nextTrack.id);
            setIsPlaying(true);
            lastProgressEmitAtRef.current = 0;
            updateProgress({
                currentTime: nextAudio.currentTime || 0,
                duration: Number.isFinite(nextAudio.duration) ? nextAudio.duration : 0,
            }, true);
            bindActiveAudioHandlers(nextAudio, nextTrack, requestToken);

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
                nextAudio.volume = progress;
                activeAudio.volume = Math.max(0, 1 - progress);

                if (progress >= 1) {
                    disposeAudio(activeAudio);
                    fadingAudioRef.current = null;
                    clearCrossfadeInterval();
                    isCrossfadingRef.current = false;
                }
            }, CROSSFADE_STEP_INTERVAL_MS);
        }
        catch (error: any) {
            isCrossfadingRef.current = false;
            toast.error("Crossfade failed", {
                description: error?.message || `Could not start "${nextTrack.title}".`,
            });
        }
        finally {
            if (requestToken === requestTokenRef.current) {
                setLoadingTrackId((current) => (current === nextTrack.id ? null : current));
            }
        }
    }, [bindActiveAudioHandlers, clearCrossfadeInterval, disposeFadingAudio, resolveSource, updateProgress]);

    useEffect(() => {
        startCrossfadeRef.current = () => {
            void startCrossfade();
        };
    }, [startCrossfade]);

    const playTrackByIndex = useCallback(async (index: number) => {
        const nextTrack = tracksRef.current[index];
        if (!nextTrack) {
            stopPlayback();
            return;
        }

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

            const audio = audioRef.current ?? new Audio();
            audio.preload = "none";
            audio.volume = 1;
            audio.src = src;
            audioRef.current = audio;
            bindActiveAudioHandlers(audio, nextTrack, requestToken);

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
    }, [bindActiveAudioHandlers, disposeFadingAudio, resolveSource, stopPlayback]);

    useEffect(() => {
        applyCrossfadeSettings();

        const handleSettingsUpdate = (event: Event) => {
            applyCrossfadeSettings((event as CustomEvent<Partial<Settings>>).detail);
        };

        window.addEventListener("settingsUpdated", handleSettingsUpdate);
        return () => window.removeEventListener("settingsUpdated", handleSettingsUpdate);
    }, [applyCrossfadeSettings]);

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

        const trackIndex = tracksRef.current.findIndex((candidate) => candidate.id === track.id);
        if (trackIndex === -1) {
            return;
        }

        await playTrackByIndex(trackIndex);
    }, [disposeFadingAudio, playTrackByIndex]);

    const playNext = useCallback(async () => {
        if (!tracksRef.current.length) {
            return;
        }

        const currentIndex = tracksRef.current.findIndex((track) => track.id === currentTrackIdRef.current);
        await playTrackByIndex(currentIndex >= 0 ? currentIndex + 1 : 0);
    }, [playTrackByIndex]);

    const playPrevious = useCallback(async () => {
        if (!tracksRef.current.length) {
            return;
        }

        const currentIndex = tracksRef.current.findIndex((track) => track.id === currentTrackIdRef.current);
        await playTrackByIndex(currentIndex > 0 ? currentIndex - 1 : 0);
    }, [playTrackByIndex]);

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
        seekTo,
        setTrackList,
        stopPlayback,
        tracks,
        toggleTrack,
    };
}

export function useLocalAudioPlayerProgress(progressStore: LocalAudioPlayerProgressStore) {
    return useSyncExternalStore(
        progressStore.subscribe,
        progressStore.getSnapshot,
        progressStore.getSnapshot,
    );
}

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
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
    audio.src = "";
}

export function useLocalAudioPlayer({ resolveSource }: UseLocalAudioPlayerOptions) {
    const [tracks, setTracks] = useState<LocalAudioTrack[]>([]);
    const [currentTrackId, setCurrentTrackId] = useState<string | null>(null);
    const [loadingTrackId, setLoadingTrackId] = useState<string | null>(null);
    const [isPlaying, setIsPlaying] = useState(false);
    const [currentTime, setCurrentTime] = useState(0);
    const [duration, setDuration] = useState(0);

    const audioRef = useRef<HTMLAudioElement | null>(null);
    const currentTrackIdRef = useRef<string | null>(null);
    const tracksRef = useRef<LocalAudioTrack[]>(tracks);
    const requestTokenRef = useRef(0);

    const stopPlayback = useCallback(() => {
        requestTokenRef.current += 1;
        disposeAudio(audioRef.current);
        audioRef.current = null;
        currentTrackIdRef.current = null;
        setCurrentTrackId(null);
        setLoadingTrackId(null);
        setIsPlaying(false);
        setCurrentTime(0);
        setDuration(0);
    }, []);

    const setTrackList = useCallback((nextTracks: LocalAudioTrack[]) => {
        tracksRef.current = nextTracks;
        setTracks(nextTracks);

        if (currentTrackIdRef.current && !nextTracks.some((track) => track.id === currentTrackIdRef.current)) {
            stopPlayback();
        }
    }, [stopPlayback]);

    const playTrackByIndex = useCallback(async (index: number) => {
        const nextTrack = tracksRef.current[index];
        if (!nextTrack) {
            stopPlayback();
            return;
        }

        const requestToken = ++requestTokenRef.current;
        setLoadingTrackId(nextTrack.id);
        disposeAudio(audioRef.current);
        audioRef.current = null;
        currentTrackIdRef.current = nextTrack.id;
        setCurrentTrackId(nextTrack.id);
        setIsPlaying(false);
        setCurrentTime(0);
        setDuration(0);

        try {
            const src = await resolveSource(nextTrack.filePath);
            if (requestToken !== requestTokenRef.current) {
                return;
            }

            const audio = new Audio(src);
            audio.preload = "metadata";
            audioRef.current = audio;

            audio.onloadedmetadata = () => {
                setDuration(Number.isFinite(audio.duration) ? audio.duration : 0);
            };

            audio.ontimeupdate = () => {
                setCurrentTime(audio.currentTime || 0);
                if (Number.isFinite(audio.duration)) {
                    setDuration(audio.duration);
                }
            };

            audio.onplay = () => {
                setIsPlaying(true);
                setLoadingTrackId(null);
            };

            audio.onpause = () => {
                setIsPlaying(false);
            };

            audio.onerror = () => {
                if (requestToken !== requestTokenRef.current) {
                    return;
                }

                toast.error("Failed to play downloaded track", {
                    description: `Could not load "${nextTrack.title}" from disk.`,
                });
                stopPlayback();
            };

            audio.onended = () => {
                const currentIndex = tracksRef.current.findIndex((track) => track.id === nextTrack.id);
                void playTrackByIndex(currentIndex + 1);
            };

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
    }, [resolveSource, stopPlayback]);

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
                audioRef.current.pause();
            }
            return;
        }

        const trackIndex = tracksRef.current.findIndex((candidate) => candidate.id === track.id);
        if (trackIndex === -1) {
            return;
        }

        await playTrackByIndex(trackIndex);
    }, [playTrackByIndex]);

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

        const boundedTime = Math.min(Math.max(nextTime, 0), Number.isFinite(audio.duration) ? audio.duration : nextTime);
        audio.currentTime = boundedTime;
        setCurrentTime(boundedTime);
        if (Number.isFinite(audio.duration)) {
            setDuration(audio.duration);
        }
    }, []);

    useEffect(() => {
        return () => {
            requestTokenRef.current += 1;
            disposeAudio(audioRef.current);
            audioRef.current = null;
        };
    }, []);

    const currentTrack = useMemo(() => {
        if (!currentTrackId) {
            return null;
        }

        return tracks.find((track) => track.id === currentTrackId) ?? null;
    }, [currentTrackId, tracks]);

    return {
        currentTrack,
        currentTrackId,
        currentTime,
        duration,
        isPlaying,
        loadingTrackId,
        playNext,
        playPrevious,
        seekTo,
        setTrackList,
        stopPlayback,
        tracks,
        toggleTrack,
    };
}

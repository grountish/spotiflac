import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { useEffect, useState } from "react";
import { Pause, Play, SkipBack, SkipForward } from "lucide-react";
import { useLocalAudioPlayerProgress, type LocalAudioPlayerProgressStore, type LocalAudioTrack } from "@/hooks/useLocalAudioPlayer";

interface LocalPlayerSidebarProps {
    collectionImageUrl?: string;
    collectionSubtitle?: string;
    collectionTitle: string;
    currentTrack: LocalAudioTrack | null;
    currentTrackId: string | null;
    isPlaying: boolean;
    loadingTrackId: string | null;
    onNext: () => void | Promise<void>;
    onPrevious: () => void | Promise<void>;
    progressStore: LocalAudioPlayerProgressStore;
    onSeek: (time: number) => void;
    onToggleTrack: (track: LocalAudioTrack) => void | Promise<void>;
    tracks: LocalAudioTrack[];
}

function formatPlaybackTime(seconds: number) {
    if (!Number.isFinite(seconds) || seconds <= 0) {
        return "0:00";
    }

    const minutes = Math.floor(seconds / 60);
    const remainingSeconds = Math.floor(seconds % 60);
    return `${minutes}:${remainingSeconds.toString().padStart(2, "0")}`;
}

export function LocalPlayerSidebar({
    collectionImageUrl,
    collectionSubtitle,
    collectionTitle,
    currentTrack,
    currentTrackId,
    isPlaying,
    loadingTrackId,
    onNext,
    onPrevious,
    progressStore,
    onSeek,
    onToggleTrack,
    tracks,
}: LocalPlayerSidebarProps) {
    if (tracks.length === 0) {
        return null;
    }

    const { currentTime, duration } = useLocalAudioPlayerProgress(progressStore);

    const currentTrackIndex = tracks.findIndex((track) => track.id === currentTrackId);
    const hasActiveTrack = !!currentTrack && currentTrackIndex >= 0;
    const hasPrevious = hasActiveTrack && currentTrackIndex > 0;
    const hasNext = hasActiveTrack && currentTrackIndex < tracks.length - 1;
    const artworkUrl = currentTrack?.imageUrl || collectionImageUrl || tracks[0]?.imageUrl;
    const visibleTracks = hasActiveTrack
        ? tracks.slice(currentTrackIndex + 1, currentTrackIndex + 5)
        : tracks.slice(0, 4);
    const primaryButtonTrack = currentTrack || tracks[0];
    const [scrubTime, setScrubTime] = useState(currentTime);
    const [isScrubbing, setIsScrubbing] = useState(false);

    useEffect(() => {
        if (!isScrubbing) {
            setScrubTime(currentTime);
        }
    }, [currentTime, isScrubbing]);

    return (
        <aside className="lg:sticky lg:top-6">
            <Card className="overflow-hidden bg-card/95 backdrop-blur supports-[backdrop-filter]:bg-card/80">
                <CardContent className="space-y-5 p-5">
                    <div className="flex items-center justify-between gap-3">
                        <Badge variant="secondary" className="px-3 py-1">
                            {currentTrack ? "Now Playing" : "Downloaded Audio"}
                        </Badge>
                        <div className="text-xs text-muted-foreground">
                            {hasActiveTrack ? `${currentTrackIndex + 1} / ${tracks.length}` : `${tracks.length} tracks`}
                        </div>
                    </div>

                    <div className="overflow-hidden rounded-2xl border bg-muted/30 shadow-sm">
                        {artworkUrl ? (
                            <img
                                src={artworkUrl}
                                alt={currentTrack?.title || collectionTitle}
                                className="aspect-square w-full object-cover"
                            />
                        ) : (
                            <div className="flex aspect-square w-full items-center justify-center bg-[radial-gradient(circle_at_top,_rgba(255,204,0,0.35),_transparent_55%),linear-gradient(160deg,_rgba(255,255,255,0.08),_rgba(255,255,255,0.02))] text-sm text-muted-foreground">
                                No artwork
                            </div>
                        )}
                    </div>

                    <div className="space-y-1">
                        <h3 className="line-clamp-2 text-xl font-semibold leading-tight">
                            {currentTrack?.title || collectionTitle}
                        </h3>
                        <p className="line-clamp-2 text-sm text-muted-foreground">
                            {currentTrack?.subtitle || collectionSubtitle || `${tracks.length} downloaded tracks ready to play`}
                        </p>
                    </div>

                    {currentTrack ? (
                        <div className="space-y-2">
                            <input
                                type="range"
                                min={0}
                                max={duration || 0}
                                step={0.1}
                                value={Math.min(scrubTime, duration || 0)}
                                onMouseDown={() => setIsScrubbing(true)}
                                onTouchStart={() => setIsScrubbing(true)}
                                onChange={(event) => {
                                    const nextTime = Number(event.target.value);
                                    setScrubTime(nextTime);
                                }}
                                onMouseUp={(event) => {
                                    const nextTime = Number((event.target as HTMLInputElement).value);
                                    setIsScrubbing(false);
                                    onSeek(nextTime);
                                }}
                                onTouchEnd={() => {
                                    setIsScrubbing(false);
                                    onSeek(scrubTime);
                                }}
                                onKeyUp={(event) => {
                                    const nextTime = Number((event.target as HTMLInputElement).value);
                                    onSeek(nextTime);
                                }}
                                className="h-2 w-full cursor-pointer appearance-none rounded-full bg-primary/20 accent-primary"
                                aria-label="Seek playback position"
                            />
                            <div className="flex items-center justify-between text-xs font-mono text-muted-foreground">
                                <span>{formatPlaybackTime(isScrubbing ? scrubTime : currentTime)}</span>
                                <span>{formatPlaybackTime(duration)}</span>
                            </div>
                        </div>
                    ) : (
                        <div className="rounded-xl border border-dashed bg-muted/20 px-3 py-4 text-sm text-muted-foreground">
                            Press play to start the downloaded queue, or pick any downloaded track from the list.
                        </div>
                    )}

                    <div className="flex items-center justify-center gap-3">
                        <Button variant="outline" size="icon" onClick={() => void onPrevious()} disabled={!hasPrevious || loadingTrackId !== null}>
                            <SkipBack className="h-4 w-4" />
                        </Button>
                        <Button size="icon-lg" onClick={() => void onToggleTrack(primaryButtonTrack)} disabled={loadingTrackId !== null}>
                            {loadingTrackId === primaryButtonTrack.id ? (
                                <span className="h-4 w-4 animate-spin rounded-full border-2 border-current border-t-transparent" />
                            ) : isPlaying ? (
                                <Pause className="h-5 w-5" />
                            ) : (
                                <Play className="h-5 w-5" />
                            )}
                        </Button>
                        <Button variant="outline" size="icon" onClick={() => void onNext()} disabled={!hasNext || loadingTrackId !== null}>
                            <SkipForward className="h-4 w-4" />
                        </Button>
                    </div>

                    {visibleTracks.length > 0 && (
                        <div className="space-y-3">
                            <div className="text-xs font-medium uppercase tracking-[0.2em] text-muted-foreground">
                                {currentTrack ? "Up Next" : "Ready Queue"}
                            </div>
                            <div className="space-y-2">
                                {visibleTracks.map((track) => (
                                    <button
                                        key={track.id}
                                        type="button"
                                        onClick={() => void onToggleTrack(track)}
                                        className="flex w-full items-center gap-3 rounded-xl border bg-background/60 px-3 py-2 text-left transition-colors hover:bg-muted/60"
                                    >
                                        {track.imageUrl ? (
                                            <img
                                                src={track.imageUrl}
                                                alt={track.title}
                                                className="h-10 w-10 rounded-md object-cover"
                                            />
                                        ) : (
                                            <div className="flex h-10 w-10 items-center justify-center rounded-md bg-muted text-xs text-muted-foreground">
                                                FLAC
                                            </div>
                                        )}
                                        <div className="min-w-0 flex-1">
                                            <div className="truncate text-sm font-medium">{track.title}</div>
                                            <div className="truncate text-xs text-muted-foreground">
                                                {track.subtitle || "Downloaded track"}
                                            </div>
                                        </div>
                                        <Play className="h-4 w-4 shrink-0 text-muted-foreground" />
                                    </button>
                                ))}
                            </div>
                        </div>
                    )}
                </CardContent>
            </Card>
        </aside>
    );
}

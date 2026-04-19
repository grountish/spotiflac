import { Disc3, FolderOpen, ListMusic, LoaderCircle, Pause, Play } from "lucide-react";
import { Button } from "@/components/ui/button";
import type { DownloadedFolderSummary } from "@/types/api";

interface DownloadedAlbumsCarouselProps {
    albums: DownloadedFolderSummary[];
    isLoading?: boolean;
    activeCollectionKey?: string | null;
    isCurrentCollectionPlaying?: boolean;
    onOpenCollection: (folder: DownloadedFolderSummary) => void;
    onPlay: (folder: DownloadedFolderSummary) => void;
    onOpenFolder: (folder: DownloadedFolderSummary) => void;
}

export function DownloadedAlbumsCarousel({
    albums,
    isLoading = false,
    activeCollectionKey,
    isCurrentCollectionPlaying = false,
    onOpenCollection,
    onPlay,
    onOpenFolder,
}: DownloadedAlbumsCarouselProps) {
    if (isLoading) {
        return (
            <div className="space-y-2">
                <span className="text-sm text-muted-foreground">Downloaded Collections</span>
                <div className="flex h-36 items-center justify-center rounded-xl border border-dashed border-border/70 bg-card/40 text-muted-foreground">
                    <LoaderCircle className="h-4 w-4 animate-spin" />
                </div>
            </div>
        );
    }

    if (albums.length === 0) {
        return null;
    }

    return (
        <div className="space-y-3">
            <span className="text-sm text-muted-foreground">
                {albums.length === 1 ? "Downloaded Collection" : "Downloaded Collections"}
            </span>
            <div className="grid grid-cols-[repeat(auto-fill,minmax(130px,130px))] justify-start gap-3">
                {albums.map((folder) => {
                    const isAlbum = folder.kind === "album";
                    const collectionKey = `local-folder:${folder.folder_path}`;
                    const isCurrentCollection = activeCollectionKey === collectionKey;
                    return (
                        <div
                            key={folder.folder_path}
                            className="group relative w-[130px] cursor-pointer overflow-hidden rounded-lg border bg-card transition-colors hover:bg-accent"
                            onClick={() => onOpenCollection(folder)}
                        >
                            <div className="p-2">
                                <div className="relative mb-2 aspect-square w-full overflow-hidden rounded-md bg-muted">
                                    {folder.image ? (
                                        <img src={folder.image} alt={folder.title} className="h-full w-full object-cover" />
                                    ) : (
                                        <div className="flex h-full w-full items-center justify-center text-muted-foreground">
                                            {isAlbum ? <Disc3 className="h-6 w-6" /> : <ListMusic className="h-6 w-6" />}
                                        </div>
                                    )}
                                    <div className="absolute inset-0 flex items-end justify-end bg-gradient-to-t from-black/65 via-black/10 to-transparent p-2 opacity-0 transition-opacity group-hover:opacity-100">
                                        <Button
                                            type="button"
                                            size="icon-sm"
                                            className="h-8 w-8 rounded-full shadow-lg"
                                            onClick={(event) => {
                                                event.stopPropagation();
                                                onPlay(folder);
                                            }}
                                        >
                                            {isCurrentCollection && isCurrentCollectionPlaying ? (
                                                <Pause className="h-3.5 w-3.5 fill-current" />
                                            ) : (
                                                <Play className="h-3.5 w-3.5 fill-current" />
                                            )}
                                        </Button>
                                    </div>
                                </div>
                                <div className="space-y-0.5">
                                    <p className="line-clamp-3 text-xs font-medium" title={folder.title}>
                                        {folder.title}
                                    </p>
                                    <p className="line-clamp-2 text-xs text-muted-foreground" title={folder.subtitle}>
                                        {folder.subtitle}
                                    </p>
                                    <div className="mt-1 flex items-center gap-2 text-[10px] text-muted-foreground">
                                        <span className={`inline-flex items-center rounded px-1.5 py-0.5 ${isAlbum ? "bg-green-500/10 text-green-600 dark:bg-green-500/20 dark:text-green-400" : "bg-purple-500/10 text-purple-600 dark:bg-purple-500/20 dark:text-purple-400"}`}>
                                            {isAlbum ? "Album" : "Playlist"}
                                        </span>
                                        <span>{folder.track_count} tracks</span>
                                    </div>
                                </div>
                                <Button
                                    type="button"
                                    variant="ghost"
                                    size="icon-sm"
                                    className="absolute right-2 top-2 h-7 w-7 rounded-full bg-black/35 text-white opacity-0 transition-opacity hover:bg-black/55 group-hover:opacity-100"
                                    onClick={(event) => {
                                        event.stopPropagation();
                                        onOpenFolder(folder);
                                    }}
                                >
                                    <FolderOpen className="h-3.5 w-3.5" />
                                </Button>
                            </div>
                        </div>
                    );
                })}
            </div>
        </div>
    );
}

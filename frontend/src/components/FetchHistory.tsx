import { LoaderCircle, Pause, Play, X, Music2, Disc3, ListMusic, UserRound } from "lucide-react";
import { Button } from "@/components/ui/button";
export interface HistoryItem {
    id: string;
    url: string;
    type: "track" | "album" | "playlist" | "artist";
    name: string;
    artist: string;
    image: string;
    timestamp: number;
}
interface FetchHistoryProps {
    history: HistoryItem[];
    checkingPlayableIds?: Set<string>;
    currentPlayableId?: string | null;
    isCurrentPlayablePlaying?: boolean;
    onPlay?: (item: HistoryItem) => void;
    onResolvePlayable?: (item: HistoryItem) => void;
    playableIds?: Set<string>;
    onSelect: (item: HistoryItem) => void;
    onRemove: (id: string) => void;
}
export function FetchHistory({
    history,
    checkingPlayableIds,
    currentPlayableId,
    isCurrentPlayablePlaying = false,
    onPlay,
    onResolvePlayable,
    playableIds,
    onSelect,
    onRemove,
}: FetchHistoryProps) {
    if (history.length === 0)
        return null;
    const isPlayableType = (type: HistoryItem["type"]) => type === "album" || type === "playlist";
    const getTypeLabel = (type: string) => {
        switch (type) {
            case "track":
                return "Track";
            case "album":
                return "Album";
            case "playlist":
                return "Playlist";
            case "artist":
                return "Artist";
            default:
                return type;
        }
    };
    const getTypeIcon = (type: string) => {
        switch (type) {
            case "track":
                return Music2;
            case "album":
                return Disc3;
            case "playlist":
                return ListMusic;
            case "artist":
                return UserRound;
            default:
                return null;
        }
    };
    const getTypeBadgeClass = (type: string) => {
        switch (type) {
            case "track":
                return "bg-blue-500/10 text-blue-600 dark:bg-blue-500/20 dark:text-blue-400";
            case "album":
                return "bg-green-500/10 text-green-600 dark:bg-green-500/20 dark:text-green-400";
            case "playlist":
                return "bg-purple-500/10 text-purple-600 dark:bg-purple-500/20 dark:text-purple-400";
            case "artist":
                return "bg-orange-500/10 text-orange-600 dark:bg-orange-500/20 dark:text-orange-400";
            default:
                return "bg-muted text-muted-foreground";
        }
    };
    return (<div className="space-y-2">
      <span className="text-sm text-muted-foreground">{history.length === 1 ? "Recent Fetch" : "Recent Fetches"}</span>
      <div className="flex gap-2 overflow-x-auto pb-2 pt-2">
        {history.map((item) => {
            const canResolvePlayback = isPlayableType(item.type);
            const isCheckingPlayable = checkingPlayableIds?.has(item.id) ?? false;
            const isPlayable = playableIds?.has(item.id) ?? false;
            const isCurrentPlayable = currentPlayableId === item.id;
            const showPlayableOverlay = canResolvePlayback && (isCheckingPlayable || isPlayable);

            return (<div key={item.id} className="relative shrink-0 w-[130px] group cursor-pointer rounded-lg border bg-card hover:bg-accent transition-colors overflow-visible" onClick={() => onSelect(item)} onMouseEnter={() => {
                    if (canResolvePlayback) {
                        onResolvePlayable?.(item);
                    }
                }}>
            <button type="button" className="absolute -top-1.5 -right-1.5 z-10 w-5 h-5 rounded-full bg-red-500 hover:bg-red-600 flex items-center justify-center opacity-0 group-hover:opacity-100 transition-all cursor-pointer shadow-sm" onClick={(e) => {
                e.stopPropagation();
                onRemove(item.id);
            }}>
              <X className="h-3 w-3 text-red-900" strokeWidth={3}/>
            </button>
            <div className="p-2">
              <div className="relative aspect-square w-full rounded-md overflow-hidden mb-2 bg-muted">
                {item.image ? (<img src={item.image} alt={item.name} className="w-full h-full object-cover"/>) : (<div className="w-full h-full flex items-center justify-center text-muted-foreground text-xs">
                    No Image
                  </div>)}
                {showPlayableOverlay && (<div className="absolute inset-0 flex items-end justify-end bg-gradient-to-t from-black/65 via-black/10 to-transparent p-2 opacity-0 transition-opacity group-hover:opacity-100">
                    <Button type="button" size="icon-sm" className="h-8 w-8 rounded-full shadow-lg" disabled={isCheckingPlayable} onClick={(e) => {
                    e.stopPropagation();
                    onPlay?.(item);
                }}>
                      {isCheckingPlayable ? (<LoaderCircle className="h-3.5 w-3.5 animate-spin"/>) : isCurrentPlayable && isCurrentPlayablePlaying ? (<Pause className="h-3.5 w-3.5 fill-current"/>) : (<Play className="h-3.5 w-3.5 fill-current"/>)} 
                    </Button>
                  </div>)}
              </div>
              <div className="space-y-0.5">
                <p className="text-xs font-medium truncate" title={item.name}>
                  {item.name}
                </p>
                <p className="text-xs text-muted-foreground truncate" title={item.artist}>
                  {item.artist}
                </p>
                {(() => {
                const IconComponent = getTypeIcon(item.type);
                return (<span className={`inline-flex items-center gap-1 text-[10px] px-1.5 py-0.5 rounded ${getTypeBadgeClass(item.type)}`}>
                      {IconComponent ? <IconComponent className="h-2.5 w-2.5"/> : null}
                      {getTypeLabel(item.type)}
                    </span>);
            })()}
              </div>
            </div>
          </div>);
        })}
      </div>
    </div>);
}

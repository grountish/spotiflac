import { useEffect } from "react";
import { Button } from "@/components/ui/button";
import { RefreshCw, CheckCircle2, XCircle, Loader2 } from "lucide-react";
import { TidalIcon, QobuzIcon, AmazonIcon, LrclibIcon, MusicBrainzIcon } from "./PlatformIcons";
import { useApiStatus } from "@/hooks/useApiStatus";

export function ApiStatusTab() {
    const { sources, statuses, details, isCheckingAll, refreshAll } = useApiStatus();
    useEffect(() => {
        const hasResults = Object.values(statuses).some((status) => status === "online" || status === "offline");
        if (!hasResults && !isCheckingAll) {
            void refreshAll();
        }
    }, [isCheckingAll, refreshAll, statuses]);

    return (<div className="space-y-6">
      <div className="flex items-center justify-end">
        <Button variant="outline" onClick={() => void refreshAll()} disabled={isCheckingAll} className="gap-2">
          <RefreshCw className={`h-4 w-4 ${isCheckingAll ? "animate-spin" : ""}`}/>
          Refresh All
        </Button>
      </div>

      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4">
        {sources.map((source) => {
            const status = statuses[source.id] || "idle";
            const detail = details[source.id];
            return (<div key={source.id} className="rounded-lg border bg-card p-4 text-card-foreground shadow-sm">
              <div className="flex items-start justify-between gap-3">
                <div className="min-w-0 flex items-start gap-3">
                  {source.type === "tidal" ? <TidalIcon className="mt-0.5 h-5 w-5 shrink-0 text-muted-foreground"/> : source.type === "amazon" ? <AmazonIcon className="mt-0.5 h-5 w-5 shrink-0 text-muted-foreground"/> : source.type === "lrclib" ? <LrclibIcon className="mt-0.5 h-5 w-5 shrink-0 text-muted-foreground"/> : source.type === "musicbrainz" ? <MusicBrainzIcon className="mt-0.5 h-5 w-5 shrink-0 text-muted-foreground"/> : <QobuzIcon className="mt-0.5 h-5 w-5 shrink-0 text-muted-foreground"/>}
                  <div className="min-w-0 space-y-2">
                    <p className="font-medium leading-none">{source.name}</p>
                    {detail?.message ? (<p className={`text-xs leading-5 break-words ${status === "online" ? "text-muted-foreground" : "text-destructive/90"}`}>
                        {detail.message}
                      </p>) : status === "checking" ? (<p className="text-xs text-muted-foreground">
                        Checking endpoint...
                      </p>) : status === "idle" ? (<p className="text-xs text-muted-foreground">
                        Not checked yet
                      </p>) : null}
                    {detail?.checked_url ? (<p className="break-all font-mono text-[11px] leading-5 text-muted-foreground/80">
                        {detail.checked_url}
                      </p>) : null}
                  </div>
                </div>

                <div className="flex shrink-0 items-center">
                  {status === "checking" && <Loader2 className="h-5 w-5 animate-spin text-muted-foreground"/>}
                  {status === "online" && <CheckCircle2 className="h-5 w-5 text-emerald-500"/>}
                  {status === "offline" && <XCircle className="h-5 w-5 text-destructive"/>}
                  {status === "idle" && <div className="h-5 w-5 rounded-full bg-muted"/>}
                </div>
              </div>
            </div>);
        })}
      </div>
    </div>);
}

import { useEffect, useRef, useState } from "react";
import { logger, type LogEntry, type LogLevel } from "@/lib/logger";
import { Terminal, X, Trash2, FolderOpen } from "lucide-react";
import { OpenConfigFolder } from "../../wailsjs/go/main/App";

const levelText: Record<LogLevel, string> = {
    info: "text-foreground/80",
    success: "text-green-500",
    warning: "text-amber-500",
    error: "text-red-400",
    debug: "text-blue-400",
};

const levelGlyph: Record<LogLevel, string> = {
    info: "•",
    success: "✓",
    warning: "!",
    error: "✕",
    debug: "·",
};

function formatTime(d: Date): string {
    try {
        return d.toLocaleTimeString(undefined, { hour12: false });
    } catch {
        return "";
    }
}

// LogConsole renders the app's logger (previously a write-only sink) as a live,
// collapsible activity panel. slskd download steps are routed here from App.tsx
// via the "slskd:log" Wails event, so the search/peer/transfer flow is visible.
export function LogConsole() {
    const [open, setOpen] = useState(false);
    const [entries, setEntries] = useState<LogEntry[]>(() => logger.getLogs());
    const [unseen, setUnseen] = useState(0);
    const scrollRef = useRef<HTMLDivElement>(null);

    useEffect(() => {
        const update = () => {
            const logs = logger.getLogs();
            setEntries(logs);
            if (!open) {
                const last = logs[logs.length - 1];
                if (last && (last.level === "error" || last.level === "warning")) {
                    setUnseen((n) => n + 1);
                }
            }
        };
        const unsubscribe = logger.subscribe(update);
        return () => {
            unsubscribe();
        };
    }, [open]);

    useEffect(() => {
        if (open) {
            setUnseen(0);
            if (scrollRef.current) {
                scrollRef.current.scrollTop = scrollRef.current.scrollHeight;
            }
        }
    }, [open, entries]);

    return (
        <>
            <button
                onClick={() => setOpen((v) => !v)}
                className="fixed bottom-6 right-20 z-50 flex h-10 w-10 items-center justify-center rounded-full border border-border bg-background/90 text-foreground/70 shadow-lg backdrop-blur transition-transform hover:scale-105 hover:text-foreground"
                title="Activity log"
                aria-label="Toggle activity log"
            >
                <Terminal className="h-4 w-4" />
                {unseen > 0 && (
                    <span className="absolute -right-1 -top-1 flex h-4 min-w-4 items-center justify-center rounded-full bg-red-500 px-1 text-[10px] font-bold leading-none text-white">
                        {unseen > 99 ? "99+" : unseen}
                    </span>
                )}
            </button>

            {open && (
                <div className="fixed bottom-[4.5rem] right-6 z-50 flex h-[20rem] w-[30rem] max-w-[calc(100vw-3rem)] flex-col overflow-hidden rounded-xl border border-border bg-background/95 shadow-2xl backdrop-blur animate-in fade-in slide-in-from-bottom-3 duration-200">
                    <div className="flex items-center justify-between border-b border-border px-3 py-2">
                        <div className="flex items-center gap-2 text-[11px] font-bold uppercase tracking-wider text-foreground/70">
                            <Terminal className="h-3.5 w-3.5" />
                            Activity Log
                        </div>
                        <div className="flex items-center gap-0.5">
                            <button
                                onClick={() => OpenConfigFolder()}
                                className="rounded p-1.5 text-foreground/50 transition-colors hover:text-foreground"
                                title="Open config folder (contains slskd/slskd-activity.log)"
                            >
                                <FolderOpen className="h-3.5 w-3.5" />
                            </button>
                            <button
                                onClick={() => logger.clear()}
                                className="rounded p-1.5 text-foreground/50 transition-colors hover:text-foreground"
                                title="Clear"
                            >
                                <Trash2 className="h-3.5 w-3.5" />
                            </button>
                            <button
                                onClick={() => setOpen(false)}
                                className="rounded p-1.5 text-foreground/50 transition-colors hover:text-foreground"
                                title="Close"
                            >
                                <X className="h-3.5 w-3.5" />
                            </button>
                        </div>
                    </div>
                    <div ref={scrollRef} className="flex-1 overflow-y-auto px-3 py-2 font-mono text-[11px] leading-relaxed">
                        {entries.length === 0 ? (
                            <div className="flex h-full items-center justify-center text-foreground/40">
                                No activity yet. Download something to see slskd progress here.
                            </div>
                        ) : (
                            entries.map((e, i) => (
                                <div key={i} className="flex gap-2 break-words">
                                    <span className="shrink-0 tabular-nums text-foreground/30">{formatTime(e.timestamp)}</span>
                                    <span className={`shrink-0 ${levelText[e.level]}`}>{levelGlyph[e.level]}</span>
                                    <span className={`min-w-0 whitespace-pre-wrap ${e.level === "error" ? "text-red-400" : e.level === "warning" ? "text-amber-400/90" : "text-foreground/80"}`}>
                                        {e.message}
                                    </span>
                                </div>
                            ))
                        )}
                    </div>
                </div>
            )}
        </>
    );
}

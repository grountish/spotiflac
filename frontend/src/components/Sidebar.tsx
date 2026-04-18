import { HomeIcon } from "@/components/ui/home";
import { SettingsIcon } from "@/components/ui/settings";
import { Tooltip, TooltipContent, TooltipTrigger, } from "@/components/ui/tooltip";
import { Button } from "@/components/ui/button";
export type PageType = "main" | "settings";
interface SidebarProps {
    currentPage: PageType;
    onPageChange: (page: PageType) => void;
    onNavigateHome?: () => void;
}
export function Sidebar({ currentPage, onPageChange, onNavigateHome }: SidebarProps) {
    return (<div className="fixed left-0 top-0 h-full w-14 bg-card border-r border-border flex flex-col items-center py-14 z-30">
            <div className="flex flex-col gap-2 flex-1">
                <Tooltip delayDuration={0}>
                    <TooltipTrigger asChild>
                        <Button variant={currentPage === "main" ? "secondary" : "ghost"} size="icon" className={`h-10 w-10 ${currentPage === "main" ? "bg-primary/10 text-primary hover:bg-primary/20" : "hover:bg-primary/10 hover:text-primary"}`} onClick={() => onNavigateHome ? onNavigateHome() : onPageChange("main")}>
                            <HomeIcon size={20}/>
                        </Button>
                    </TooltipTrigger>
                    <TooltipContent side="right">
                        <p>Home</p>
                    </TooltipContent>
                </Tooltip>

                <Tooltip delayDuration={0}>
                    <TooltipTrigger asChild>
                        <Button variant={currentPage === "settings" ? "secondary" : "ghost"} size="icon" className={`h-10 w-10 ${currentPage === "settings" ? "bg-primary/10 text-primary hover:bg-primary/20" : "hover:bg-primary/10 hover:text-primary"}`} onClick={() => onPageChange("settings")}>
                            <SettingsIcon size={20}/>
                        </Button>
                    </TooltipTrigger>
                    <TooltipContent side="right">
                        <p>Settings</p>
                    </TooltipContent>
                </Tooltip>
            </div>
        </div>);
}

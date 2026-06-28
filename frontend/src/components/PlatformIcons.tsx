import amazonMusicIcon from "../assets/icons/amzn.png";
import lrclibIcon from "../assets/icons/lrclib.png";
import musicBrainzDarkIcon from "../assets/icons/musicbrainz_d.png";
import musicBrainzLightIcon from "../assets/icons/musicbrainz_l.png";
import qobuzIcon from "../assets/icons/qbz.png";
import songlinkDarkIcon from "../assets/icons/songlink_d.png";
import songlinkLightIcon from "../assets/icons/songlink_l.png";
import songstatsIcon from "../assets/icons/songstats.png";
import tidalDarkIcon from "../assets/icons/tidal_d.png";
import tidalLightIcon from "../assets/icons/tidal_l.png";
type PlatformIconProps = {
    className?: string;
};
function sanitizeClassName(className: string): string {
    return className
        .split(/\s+/)
        .filter(Boolean)
        .filter((part) => part !== "fill-current" && part !== "fill-muted-foreground" && !part.startsWith("text-"))
        .join(" ");
}
function hasRoundedClass(className: string): boolean {
    return className
        .split(/\s+/)
        .some((part) => part.startsWith("rounded"));
}
function getStatusClasses(className: string): string {
    if (className.includes("text-green-500")) {
        return "ring-2 ring-green-500 rounded-sm";
    }
    if (className.includes("text-red-500")) {
        return "ring-2 ring-red-500 rounded-sm opacity-70";
    }
    return "";
}
function PlatformIcon({ src, alt, className = "w-4 h-4", defaultClassName = "" }: {
    src: string;
    alt: string;
    className?: string;
    defaultClassName?: string;
}) {
    const cleanedClassName = sanitizeClassName(className);
    const statusClasses = getStatusClasses(className);
    const imageClassName = [
        cleanedClassName || "w-4 h-4",
        "inline-block shrink-0 object-contain",
        !hasRoundedClass(cleanedClassName) ? defaultClassName : "",
        statusClasses,
    ]
        .filter(Boolean)
        .join(" ");
    return <img src={src} alt={alt} className={imageClassName} loading="lazy" referrerPolicy="no-referrer"/>;
}
function ThemedPlatformIcon({ lightSrc, darkSrc, alt, className = "w-4 h-4", defaultClassName = "" }: {
    lightSrc: string;
    darkSrc: string;
    alt: string;
    className?: string;
    defaultClassName?: string;
}) {
    const cleanedClassName = sanitizeClassName(className);
    const statusClasses = getStatusClasses(className);
    const wrapperClassName = [
        cleanedClassName || "w-4 h-4",
        "relative inline-flex shrink-0",
        !hasRoundedClass(cleanedClassName) ? defaultClassName : "",
        statusClasses,
    ]
        .filter(Boolean)
        .join(" ");
    return <span role="img" aria-label={alt} className={wrapperClassName}>
      <img src={lightSrc} alt="" aria-hidden="true" className="h-full w-full object-contain dark:hidden" loading="lazy" referrerPolicy="no-referrer"/>
      <img src={darkSrc} alt="" aria-hidden="true" className="hidden h-full w-full object-contain dark:block" loading="lazy" referrerPolicy="no-referrer"/>
    </span>;
}
export function TidalIcon({ className = "w-4 h-4" }: PlatformIconProps) {
    return <ThemedPlatformIcon lightSrc={tidalLightIcon} darkSrc={tidalDarkIcon} alt="Tidal" className={className} defaultClassName="rounded-[4px]"/>;
}
export function QobuzIcon({ className = "w-4 h-4" }: PlatformIconProps) {
    return <PlatformIcon src={qobuzIcon} alt="Qobuz" className={className}/>;
}
export function AmazonIcon({ className = "w-4 h-4" }: PlatformIconProps) {
    return <PlatformIcon src={amazonMusicIcon} alt="Amazon Music" className={className} defaultClassName="rounded-[4px]"/>;
}
export function LrclibIcon({ className = "w-4 h-4" }: PlatformIconProps) {
    return <PlatformIcon src={lrclibIcon} alt="LRCLIB" className={className}/>;
}
export function MusicBrainzIcon({ className = "w-4 h-4" }: PlatformIconProps) {
    return <ThemedPlatformIcon lightSrc={musicBrainzLightIcon} darkSrc={musicBrainzDarkIcon} alt="MusicBrainz" className={className}/>;
}
export function SonglinkIcon({ className = "w-4 h-4" }: PlatformIconProps) {
    return <ThemedPlatformIcon lightSrc={songlinkLightIcon} darkSrc={songlinkDarkIcon} alt="Songlink" className={className} defaultClassName="rounded-[3px]"/>;
}
export function SongstatsIcon({ className = "w-4 h-4" }: PlatformIconProps) {
    return <PlatformIcon src={songstatsIcon} alt="Songstats" className={className} defaultClassName="rounded-[3px]"/>;
}

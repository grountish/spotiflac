import { CheckFilesExistence } from "../../wailsjs/go/main/App";
import { buildPlaylistFolderName } from "@/lib/playlist";
import { getSettings } from "@/lib/settings";
import { getFirstArtist, joinPath, sanitizePath } from "@/lib/utils";
import type { LocalAudioTrack } from "@/hooks/useLocalAudioPlayer";
import type { SpotifyMetadataResponse, TrackMetadata } from "@/types/api";

export interface LocalCollectionPlaybackTarget {
    imageUrl?: string;
    key: string;
    subtitle?: string;
    title: string;
    tracks: LocalAudioTrack[];
}

interface LocalTrackFileRequest {
    spotify_id: string;
    track_name: string;
    artist_name: string;
    album_name?: string;
    album_artist?: string;
    release_date?: string;
    track_number?: number;
    disc_number?: number;
    position?: number;
    use_album_track_number?: boolean;
    filename_format?: string;
    include_track_number?: boolean;
    audio_format?: string;
}

interface LocalTrackFileResult {
    spotify_id: string;
    exists: boolean;
    file_path?: string;
}

interface LocalCollectionDescriptor {
    imageUrl?: string;
    isAlbum?: boolean;
    key: string;
    subtitle?: string;
    title: string;
    folderName?: string;
    tracks: TrackMetadata[];
}

function buildOutputDir(folderName: string | undefined, isAlbum: boolean | undefined) {
    const settings = getSettings();
    const os = settings.operatingSystem;
    let outputDir = settings.downloadPath;
    const useAlbumTag = settings.folderTemplate?.includes("{album}");

    if (settings.createPlaylistFolder && folderName && (!isAlbum || !useAlbumTag)) {
        outputDir = joinPath(os, outputDir, sanitizePath(folderName.replace(/\//g, " "), os));
    }

    return {
        outputDir,
        rootDir: settings.downloadPath,
        settings,
    };
}

export async function resolveLocalTrackPaths({
    tracks,
    folderName,
    isAlbum,
}: {
    tracks: TrackMetadata[];
    folderName?: string;
    isAlbum?: boolean;
}) {
    const tracksWithId = tracks.filter((track) => track.spotify_id);
    if (tracksWithId.length === 0) {
        return new Map<string, string>();
    }

    const { outputDir, rootDir, settings } = buildOutputDir(folderName, isAlbum);
    const useAlbumTrackNumber = settings.folderTemplate?.includes("{album}") || false;

    const requests: LocalTrackFileRequest[] = tracksWithId.map((track, index) => ({
        spotify_id: track.spotify_id || "",
        track_name: track.name || "",
        artist_name: settings.useFirstArtistOnly && track.artists ? getFirstArtist(track.artists) : track.artists || "",
        album_name: track.album_name || "",
        album_artist: settings.useFirstArtistOnly && track.album_artist ? getFirstArtist(track.album_artist) : track.album_artist || "",
        release_date: track.release_date || "",
        track_number: track.track_number || 0,
        disc_number: track.disc_number || 0,
        position: index + 1,
        use_album_track_number: useAlbumTrackNumber,
        filename_format: settings.filenameTemplate || "",
        include_track_number: settings.trackNumber || false,
        audio_format: "flac",
    }));

    const results = await CheckFilesExistence(outputDir, rootDir, requests) as LocalTrackFileResult[];
    const localTrackPaths = new Map<string, string>();

    for (const result of results) {
        if (result.exists && result.file_path) {
            localTrackPaths.set(result.spotify_id, result.file_path);
        }
    }

    return localTrackPaths;
}

export function buildPlayableTracks(tracks: TrackMetadata[], localTrackPaths: Map<string, string>): LocalAudioTrack[] {
    return tracks
        .filter((track) => !!track.spotify_id && localTrackPaths.has(track.spotify_id))
        .map((track) => ({
            id: track.spotify_id!,
            title: track.name,
            subtitle: [track.artists, track.album_name].filter(Boolean).join(" • "),
            imageUrl: track.images,
            filePath: localTrackPaths.get(track.spotify_id!)!,
        }));
}

function getLocalCollectionDescriptor(data: SpotifyMetadataResponse): LocalCollectionDescriptor | null {
    if ("album_info" in data) {
        const { album_info, track_list } = data;
        return {
            imageUrl: album_info.images,
            isAlbum: true,
            key: `album:${track_list[0]?.album_id || `${album_info.name}:${album_info.artists}:${album_info.release_date}`}`,
            subtitle: album_info.artists,
            title: album_info.name,
            folderName: album_info.name,
            tracks: track_list,
        };
    }

    if ("playlist_info" in data) {
        const settings = getSettings();
        const playlistName = data.playlist_info.owner.name;
        const playlistFolderName = buildPlaylistFolderName(
            playlistName,
            data.playlist_info.owner.display_name,
            settings.playlistOwnerFolderName,
        );

        return {
            imageUrl: data.playlist_info.cover,
            key: `playlist:${playlistFolderName}`,
            subtitle: data.playlist_info.owner.display_name,
            title: playlistName,
            folderName: playlistFolderName,
            tracks: data.track_list,
        };
    }

    return null;
}

export async function buildLocalCollectionPlaybackTargetFromMetadata(data: SpotifyMetadataResponse): Promise<LocalCollectionPlaybackTarget | null> {
    const descriptor = getLocalCollectionDescriptor(data);
    if (!descriptor) {
        return null;
    }

    const localTrackPaths = await resolveLocalTrackPaths({
        tracks: descriptor.tracks,
        folderName: descriptor.folderName,
        isAlbum: descriptor.isAlbum,
    });

    return {
        imageUrl: descriptor.imageUrl,
        key: descriptor.key,
        subtitle: descriptor.subtitle,
        title: descriptor.title,
        tracks: buildPlayableTracks(descriptor.tracks, localTrackPaths),
    };
}

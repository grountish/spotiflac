package backend

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type DownloadedFolderSummary struct {
	Title          string `json:"title"`
	Subtitle       string `json:"subtitle"`
	Kind           string `json:"kind"`
	FolderName     string `json:"folder_name"`
	RelativePath   string `json:"relative_path"`
	FolderPath     string `json:"folder_path"`
	Image          string `json:"image,omitempty"`
	TrackCount     int    `json:"track_count"`
	AlbumCount     int    `json:"album_count"`
	ArtistCount    int    `json:"artist_count"`
	LatestModified int64  `json:"latest_modified"`
}

type localTrackSample struct {
	album  string
	artist string
}

type localFolderAccumulator struct {
	albumKeys  map[string]struct{}
	artistKeys map[string]struct{}
	folderPath string
	imageFile  string
	latest     time.Time
	samples    []localTrackSample
	trackCount int
}

func getConfiguredDownloadPath() string {
	settings, err := LoadConfigSettings()
	if err != nil || settings == nil {
		return GetDefaultMusicPath()
	}

	downloadPath, _ := settings["downloadPath"].(string)
	downloadPath = strings.TrimSpace(downloadPath)
	if downloadPath == "" {
		return GetDefaultMusicPath()
	}

	return downloadPath
}

func coverDataURL(filePath string) string {
	coverPath, err := ExtractCoverArt(filePath)
	if err != nil || strings.TrimSpace(coverPath) == "" {
		return ""
	}
	defer os.Remove(coverPath)

	data, err := os.ReadFile(coverPath)
	if err != nil || len(data) == 0 {
		return ""
	}

	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(data)
}

func normalizeFolderDisplayName(rootPath string, folderPath string) (string, string) {
	relativePath, err := filepath.Rel(rootPath, folderPath)
	if err != nil {
		relativePath = folderPath
	}

	relativePath = filepath.Clean(relativePath)
	if relativePath == "." {
		base := filepath.Base(rootPath)
		if base == "." || base == string(filepath.Separator) || strings.TrimSpace(base) == "" {
			base = "Download Root"
		}
		return base, "."
	}

	return filepath.Base(folderPath), relativePath
}

func addNonEmpty(set map[string]struct{}, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	set[strings.ToLower(value)] = struct{}{}
}

func normalizeSample(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func resolveFolderCardIdentity(folderName string, sampleOne localTrackSample, sampleTwo *localTrackSample) (string, string, string) {
	albumOne := strings.TrimSpace(sampleOne.album)
	artistOne := strings.TrimSpace(sampleOne.artist)

	if albumOne == "" {
		return folderName, "Playlist", "playlist"
	}

	if sampleTwo == nil {
		if artistOne == "" {
			artistOne = "Unknown Artist"
		}
		return albumOne, artistOne, "album"
	}

	if normalizeSample(sampleOne.album) == normalizeSample(sampleTwo.album) && normalizeSample(sampleOne.artist) == normalizeSample(sampleTwo.artist) {
		if artistOne == "" {
			artistOne = "Unknown Artist"
		}
		return albumOne, artistOne, "album"
	}

	return folderName, "Playlist", "playlist"
}

func LoadDownloadedFolders(limit int) ([]DownloadedFolderSummary, error) {
	rootPath := getConfiguredDownloadPath()
	if strings.TrimSpace(rootPath) == "" {
		return []DownloadedFolderSummary{}, nil
	}

	info, err := os.Stat(rootPath)
	if err != nil || !info.IsDir() {
		return []DownloadedFolderSummary{}, nil
	}

	audioFiles, err := ListAudioFiles(rootPath)
	if err != nil {
		return nil, err
	}

	sort.Slice(audioFiles, func(i, j int) bool {
		return audioFiles[i].Path < audioFiles[j].Path
	})

	folders := make(map[string]*localFolderAccumulator)

	for _, file := range audioFiles {
		folderPath := filepath.Dir(file.Path)
		acc, exists := folders[folderPath]
		if !exists {
			acc = &localFolderAccumulator{
				albumKeys:  make(map[string]struct{}),
				artistKeys: make(map[string]struct{}),
				folderPath: folderPath,
			}
			folders[folderPath] = acc
		}

		acc.trackCount++

		metadata, err := ReadAudioMetadata(file.Path)
		if err == nil && metadata != nil {
			albumName := strings.TrimSpace(metadata.Album)
			albumArtist := strings.TrimSpace(metadata.AlbumArtist)
			if albumArtist == "" {
				albumArtist = strings.TrimSpace(metadata.Artist)
			}

			addNonEmpty(acc.albumKeys, albumName)
			addNonEmpty(acc.artistKeys, albumArtist)

			if len(acc.samples) < 2 {
				acc.samples = append(acc.samples, localTrackSample{
					album:  albumName,
					artist: albumArtist,
				})
			}
		}

		fileInfo, statErr := os.Stat(file.Path)
		if statErr == nil && fileInfo.ModTime().After(acc.latest) {
			acc.latest = fileInfo.ModTime()
			acc.imageFile = file.Path
		}
	}

	summaries := make([]DownloadedFolderSummary, 0, len(folders))
	for _, folder := range folders {
		folderName, relativePath := normalizeFolderDisplayName(rootPath, folder.folderPath)

		title := folderName
		subtitle := "Playlist"
		kind := "playlist"
		if len(folder.samples) > 0 {
			var sampleTwo *localTrackSample
			if len(folder.samples) > 1 {
				sampleTwo = &folder.samples[1]
			}
			title, subtitle, kind = resolveFolderCardIdentity(folderName, folder.samples[0], sampleTwo)
		}

		summaries = append(summaries, DownloadedFolderSummary{
			Title:          title,
			Subtitle:       subtitle,
			Kind:           kind,
			FolderName:     folderName,
			RelativePath:   relativePath,
			FolderPath:     folder.folderPath,
			TrackCount:     folder.trackCount,
			AlbumCount:     len(folder.albumKeys),
			ArtistCount:    len(folder.artistKeys),
			LatestModified: folder.latest.Unix(),
		})
	}

	sort.Slice(summaries, func(i, j int) bool {
		if summaries[i].LatestModified == summaries[j].LatestModified {
			return summaries[i].RelativePath < summaries[j].RelativePath
		}
		return summaries[i].LatestModified > summaries[j].LatestModified
	})

	if limit > 0 && len(summaries) > limit {
		summaries = summaries[:limit]
	}

	for i := range summaries {
		if folder, ok := folders[summaries[i].FolderPath]; ok && folder.imageFile != "" {
			summaries[i].Image = coverDataURL(folder.imageFile)
		}
	}

	return summaries, nil
}

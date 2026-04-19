package backend

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

type SoulseekDownloadRequest struct {
	TrackName            string
	ArtistName           string
	AlbumName            string
	AlbumArtist          string
	ReleaseDate          string
	CoverURL             string
	OutputDir            string
	FilenameFormat       string
	TrackNumber          bool
	Position             int
	UseAlbumTrackNumber  bool
	SpotifyID            string
	Duration             int
	SpotifyTrackNumber   int
	SpotifyDiscNumber    int
	SpotifyTotalTracks   int
	SpotifyTotalDiscs    int
	ISRC                 string
	Copyright            string
	Publisher            string
	Composer             string
	PlaylistName         string
	PlaylistOwner        string
	EmbedMaxQualityCover bool
	Separator            string
}

type soulseekConfig struct {
	Enabled        bool
	BaseURL        string
	APIKey         string
	DownloadPath   string
	SearchTimeout  int
}

type SoulseekConnectionTestResult struct {
	BaseURL              string `json:"base_url"`
	Authenticated        bool   `json:"authenticated"`
	ServerConnected      bool   `json:"server_connected"`
	ServerState          string `json:"server_state,omitempty"`
	ServerAddress        string `json:"server_address,omitempty"`
	LoggedInUsername     string `json:"logged_in_username,omitempty"`
	DownloadPath         string `json:"download_path"`
	DownloadPathExists   bool   `json:"download_path_exists"`
	DownloadPathIsDir    bool   `json:"download_path_is_dir"`
	SharesReady          bool   `json:"shares_ready"`
	SharedDirectories    int    `json:"shared_directories"`
	SharedFiles          int    `json:"shared_files"`
	Message              string `json:"message"`
}

type slskdClient struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

type slskdSearchRequest struct {
	SearchText                string `json:"SearchText"`
	SearchTimeout             int    `json:"SearchTimeout,omitempty"`
	ResponseLimit             int    `json:"ResponseLimit,omitempty"`
	FileLimit                 int    `json:"FileLimit,omitempty"`
	FilterResponses           bool   `json:"FilterResponses,omitempty"`
	MaximumPeerQueueLength    int    `json:"MaximumPeerQueueLength,omitempty"`
	MinimumPeerUploadSpeed    int    `json:"MinimumPeerUploadSpeed,omitempty"`
	MinimumResponseFileCount  int    `json:"MinimumResponseFileCount,omitempty"`
}

type slskdSearch struct {
	ID         string                `json:"id"`
	IsComplete bool                  `json:"isComplete"`
	EndedAt    *time.Time            `json:"endedAt"`
	Responses  []slskdSearchResponse `json:"responses"`
}

type slskdSearchResponse struct {
	Files             []slskdSearchFile `json:"files"`
	HasFreeUploadSlot bool              `json:"hasFreeUploadSlot"`
	QueueLength       int64             `json:"queueLength"`
	UploadSpeed       int               `json:"uploadSpeed"`
	Username          string            `json:"username"`
}

type slskdSearchFile struct {
	BitDepth   *int   `json:"bitDepth"`
	BitRate    *int   `json:"bitRate"`
	Extension  string `json:"extension"`
	Filename   string `json:"filename"`
	Length     *int   `json:"length"`
	SampleRate *int   `json:"sampleRate"`
	Size       int64  `json:"size"`
	IsLocked   bool   `json:"isLocked"`
}

type slskdQueueDownloadRequest struct {
	Filename string `json:"Filename"`
	Size     int64  `json:"Size"`
}

type slskdTransfer struct {
	BytesTransferred float64    `json:"bytesTransferred"`
	EndTime          *time.Time `json:"endTime"`
	Exception        string     `json:"exception"`
	Filename         string     `json:"filename"`
	ID               string     `json:"id"`
	PercentComplete  float64    `json:"percentComplete"`
	Size             int64      `json:"size"`
	State            int        `json:"state"`
	Username         string     `json:"username"`
}

type slskdApplicationState struct {
	Server struct {
		Address     string `json:"address"`
		State       string `json:"state"`
		IsConnected bool   `json:"isConnected"`
		IsLoggedIn  bool   `json:"isLoggedIn"`
	} `json:"server"`
	User struct {
		Username string `json:"username"`
	} `json:"user"`
	Shares struct {
		Ready       bool `json:"ready"`
		Directories int  `json:"directories"`
		Files       int  `json:"files"`
	} `json:"shares"`
}

type soulseekCandidate struct {
	username    string
	file        slskdSearchFile
	score       int
	queueLength int64
}

var soulseekCompletedTransferStates = []int{16, 48, 80, 144, 272, 528, 1040}

func NewSlskdClient(config soulseekConfig) *slskdClient {
	baseURL := strings.TrimSpace(config.BaseURL)
	if baseURL != "" && !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		baseURL = "http://" + baseURL
	}

	return &slskdClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  strings.TrimSpace(config.APIKey),
		httpClient: &http.Client{
			Timeout: 45 * time.Second,
		},
	}
}

func LoadSoulseekConfig() (soulseekConfig, error) {
	settings, err := LoadConfigSettings()
	if err != nil {
		return soulseekConfig{}, err
	}

	config := soulseekConfig{
		Enabled:       getBoolSetting(settings, "enableSoulseekFallback", false),
		BaseURL:       getStringSetting(settings, "soulseekURL"),
		APIKey:        getStringSetting(settings, "soulseekApiKey"),
		DownloadPath:  NormalizePath(getStringSetting(settings, "soulseekDownloadPath")),
		SearchTimeout: getIntSetting(settings, "soulseekSearchTimeout", 20),
	}

	if config.SearchTimeout < 5 {
		config.SearchTimeout = 5
	}
	if config.SearchTimeout > 120 {
		config.SearchTimeout = 120
	}

	return config, nil
}

func DownloadTrackViaSoulseek(req SoulseekDownloadRequest) (string, error) {
	config, err := LoadSoulseekConfig()
	if err != nil {
		return "", fmt.Errorf("failed to load Soulseek settings: %w", err)
	}
	if !config.Enabled {
		return "", fmt.Errorf("soulseek fallback is disabled")
	}
	if config.BaseURL == "" || config.APIKey == "" || config.DownloadPath == "" {
		return "", fmt.Errorf("soulseek fallback is enabled but incomplete. set slskd URL, API key, and download folder in Settings")
	}

	client := NewSlskdClient(config)
	candidate, err := client.findBestCandidate(req, config.SearchTimeout)
	if err != nil {
		return "", err
	}

	downloadStartedAt := time.Now()
	transfer, err := client.queueAndWaitForDownload(candidate, config.SearchTimeout)
	if err != nil {
		return "", err
	}

	locatedPath, err := locateSoulseekDownload(config.DownloadPath, transfer.Filename, transfer.Size, downloadStartedAt.Add(-5*time.Second))
	if err != nil {
		return "", err
	}

	finalPath, err := moveSoulseekDownloadIntoPlace(locatedPath, req)
	if err != nil {
		return "", err
	}

	if err := applySoulseekMetadata(finalPath, req); err != nil {
		return "", err
	}

	return finalPath, nil
}

func TestSoulseekConnection(baseURL, apiKey, downloadPath string) (SoulseekConnectionTestResult, error) {
	config := soulseekConfig{
		BaseURL:      strings.TrimSpace(baseURL),
		APIKey:       strings.TrimSpace(apiKey),
		DownloadPath: NormalizePath(strings.TrimSpace(downloadPath)),
	}

	result := SoulseekConnectionTestResult{
		BaseURL:      config.BaseURL,
		DownloadPath: config.DownloadPath,
	}

	if config.BaseURL == "" {
		result.Message = "Missing slskd URL"
		return result, fmt.Errorf("missing slskd URL")
	}
	if config.APIKey == "" {
		result.Message = "Missing slskd API key"
		return result, fmt.Errorf("missing slskd API key")
	}
	if config.DownloadPath == "" {
		result.Message = "Missing Soulseek download folder"
		return result, fmt.Errorf("missing Soulseek download folder")
	}

	client := NewSlskdClient(config)
	var appState slskdApplicationState
	if err := client.doJSON(http.MethodGet, "/api/v0/application", nil, &appState); err != nil {
		result.Message = fmt.Sprintf("slskd API request failed: %v", err)
		return result, err
	}

	result.Authenticated = true
	result.ServerConnected = appState.Server.IsConnected && appState.Server.IsLoggedIn
	result.ServerState = appState.Server.State
	result.ServerAddress = appState.Server.Address
	result.LoggedInUsername = appState.User.Username
	result.SharesReady = appState.Shares.Ready
	result.SharedDirectories = appState.Shares.Directories
	result.SharedFiles = appState.Shares.Files

	if info, err := os.Stat(config.DownloadPath); err == nil {
		result.DownloadPathExists = true
		result.DownloadPathIsDir = info.IsDir()
	} else if os.IsNotExist(err) {
		result.DownloadPathExists = false
		result.DownloadPathIsDir = false
	} else {
		result.Message = fmt.Sprintf("slskd connected but failed to inspect download folder: %v", err)
		return result, err
	}

	switch {
	case !result.DownloadPathExists:
		result.Message = "slskd API works, but the configured Soulseek download folder does not exist"
	case !result.DownloadPathIsDir:
		result.Message = "slskd API works, but the configured Soulseek download folder is not a directory"
	case !result.ServerConnected:
		result.Message = "slskd API works, but slskd is not fully connected/logged into Soulseek"
	default:
		result.Message = "slskd connection succeeded"
	}

	return result, nil
}

func (c *slskdClient) findBestCandidate(req SoulseekDownloadRequest, searchTimeout int) (soulseekCandidate, error) {
	queries := buildSoulseekQueries(req)
	var best soulseekCandidate
	found := false

	for _, query := range queries {
		search, err := c.createSearch(query, searchTimeout)
		if err != nil {
			return soulseekCandidate{}, err
		}

		search, err = c.waitForSearch(search.ID, searchTimeout)
		if err != nil {
			return soulseekCandidate{}, err
		}

		candidate, ok := scoreSoulseekSearch(search, req)
		if !ok {
			continue
		}

		if !found || candidate.score > best.score || (candidate.score == best.score && candidate.queueLength < best.queueLength) {
			best = candidate
			found = true
		}
	}

	if !found {
		return soulseekCandidate{}, fmt.Errorf("soulseek fallback did not find a suitable FLAC match")
	}

	return best, nil
}

func (c *slskdClient) createSearch(query string, searchTimeout int) (slskdSearch, error) {
	reqBody := slskdSearchRequest{
		SearchText:               query,
		SearchTimeout:            searchTimeout,
		ResponseLimit:            40,
		FileLimit:                100,
		FilterResponses:          true,
		MaximumPeerQueueLength:   20,
		MinimumPeerUploadSpeed:   0,
		MinimumResponseFileCount: 1,
	}

	var search slskdSearch
	if err := c.doJSON(http.MethodPost, "/api/v0/searches", reqBody, &search); err != nil {
		return slskdSearch{}, fmt.Errorf("soulseek search failed: %w", err)
	}

	if search.ID == "" {
		return slskdSearch{}, fmt.Errorf("soulseek search returned no id")
	}

	return search, nil
}

func (c *slskdClient) waitForSearch(searchID string, searchTimeout int) (slskdSearch, error) {
	deadline := time.Now().Add(time.Duration(searchTimeout+10) * time.Second)
	var search slskdSearch

	for time.Now().Before(deadline) {
		if err := c.doJSON(http.MethodGet, fmt.Sprintf("/api/v0/searches/%s?includeResponses=true", searchID), nil, &search); err != nil {
			return slskdSearch{}, fmt.Errorf("failed to poll soulseek search: %w", err)
		}

		if search.IsComplete || search.EndedAt != nil {
			return search, nil
		}

		time.Sleep(1 * time.Second)
	}

	return search, fmt.Errorf("soulseek search timed out")
}

func (c *slskdClient) queueAndWaitForDownload(candidate soulseekCandidate, searchTimeout int) (slskdTransfer, error) {
	requests := []slskdQueueDownloadRequest{{
		Filename: candidate.file.Filename,
		Size:     candidate.file.Size,
	}}
	if err := c.doJSON(http.MethodPost, fmt.Sprintf("/api/v0/transfers/downloads/%s", candidate.username), requests, nil); err != nil {
		return slskdTransfer{}, fmt.Errorf("failed to enqueue soulseek download: %w", err)
	}

	deadline := time.Now().Add(time.Duration(searchTimeout+600) * time.Second)
	for time.Now().Before(deadline) {
		var transfers []slskdTransfer
		if err := c.doJSON(http.MethodGet, fmt.Sprintf("/api/v0/transfers/downloads/%s", candidate.username), nil, &transfers); err != nil {
			return slskdTransfer{}, fmt.Errorf("failed to poll soulseek transfer: %w", err)
		}

		for _, transfer := range transfers {
			if transfer.Filename != candidate.file.Filename {
				continue
			}

			if transfer.Exception != "" && !isSoulseekTransferComplete(transfer.State) {
				return slskdTransfer{}, fmt.Errorf("soulseek download failed: %s", transfer.Exception)
			}
			if isSoulseekTransferComplete(transfer.State) || (transfer.Size > 0 && transfer.BytesTransferred >= float64(transfer.Size)) {
				return transfer, nil
			}
		}

		time.Sleep(2 * time.Second)
	}

	return slskdTransfer{}, fmt.Errorf("soulseek download timed out")
}

func (c *slskdClient) doJSON(method, path string, payload any, out any) error {
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-API-Key", c.apiKey)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	if out == nil || len(respBody) == 0 {
		return nil
	}

	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("failed to decode soulseek response: %w", err)
	}
	return nil
}

func scoreSoulseekSearch(search slskdSearch, req SoulseekDownloadRequest) (soulseekCandidate, bool) {
	var best soulseekCandidate
	found := false

	trackTokens := normalizedTokens(req.TrackName)
	artistTokens := normalizedTokens(GetFirstArtist(req.ArtistName))
	albumTokens := normalizedTokens(req.AlbumName)

	for _, response := range search.Responses {
		for _, file := range response.Files {
			if file.IsLocked || !strings.EqualFold(strings.TrimPrefix(file.Extension, "."), "flac") {
				continue
			}

			normalizedName := normalizeSoulseekText(file.Filename)
			score := 0

			score += tokenMatchScore(normalizedName, trackTokens, 25)
			score += tokenMatchScore(normalizedName, artistTokens, 18)
			score += tokenMatchScore(normalizedName, albumTokens, 6)

			if file.BitDepth != nil {
				score += minInt(*file.BitDepth, 24)
			}
			if file.SampleRate != nil {
				score += minInt(*file.SampleRate/1000, 192)
			}
			if response.HasFreeUploadSlot {
				score += 12
			}
			if response.QueueLength == 0 {
				score += 8
			} else if response.QueueLength <= 5 {
				score += 4
			} else if response.QueueLength > 25 {
				score -= 8
			}

			if req.Duration > 0 && file.Length != nil {
				diff := absInt(*file.Length - req.Duration)
				switch {
				case diff <= 2:
					score += 40
				case diff <= 5:
					score += 28
				case diff <= 10:
					score += 12
				case diff > 20:
					score -= 30
				}
			}

			if score < 35 {
				continue
			}

			candidate := soulseekCandidate{
				username:    response.Username,
				file:        file,
				score:       score,
				queueLength: response.QueueLength,
			}

			if !found || candidate.score > best.score || (candidate.score == best.score && candidate.queueLength < best.queueLength) {
				best = candidate
				found = true
			}
		}
	}

	return best, found
}

func buildSoulseekQueries(req SoulseekDownloadRequest) []string {
	parts := []string{
		strings.TrimSpace(GetFirstArtist(req.ArtistName)),
		strings.TrimSpace(req.TrackName),
		strings.TrimSpace(req.AlbumName),
	}
	query1 := strings.Join(filterEmptyStrings(parts), " ")
	query2 := strings.Join(filterEmptyStrings([]string{parts[0], parts[1]}), " ")
	query3 := strings.Join(filterEmptyStrings([]string{parts[1], parts[0]}), " ")
	return uniqueNonEmptyStrings(query1, query2, query3)
}

func locateSoulseekDownload(downloadRoot, remoteFilename string, size int64, earliestModTime time.Time) (string, error) {
	downloadRoot = strings.TrimSpace(downloadRoot)
	if downloadRoot == "" {
		return "", fmt.Errorf("soulseek download folder is empty")
	}

	targetBase := filepath.Base(strings.ReplaceAll(remoteFilename, "\\", "/"))
	var matches []string
	err := filepath.WalkDir(downloadRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}
		if size > 0 && info.Size() != size {
			return nil
		}
		if !strings.EqualFold(filepath.Base(path), targetBase) {
			return nil
		}
		if !info.ModTime().After(earliestModTime) {
			return nil
		}

		matches = append(matches, path)
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("soulseek download completed but the file was not found under %s", downloadRoot)
	}

	slices.SortFunc(matches, func(a, b string) int {
		aInfo, _ := os.Stat(a)
		bInfo, _ := os.Stat(b)
		if aInfo == nil || bInfo == nil {
			return 0
		}
		if aInfo.ModTime().Equal(bInfo.ModTime()) {
			return strings.Compare(a, b)
		}
		if aInfo.ModTime().After(bInfo.ModTime()) {
			return -1
		}
		return 1
	})

	return matches[0], nil
}

func moveSoulseekDownloadIntoPlace(sourcePath string, req SoulseekDownloadRequest) (string, error) {
	expectedFilename := BuildExpectedFilename(
		req.TrackName,
		req.ArtistName,
		req.AlbumName,
		req.AlbumArtist,
		req.ReleaseDate,
		req.FilenameFormat,
		req.PlaylistName,
		req.PlaylistOwner,
		req.TrackNumber,
		req.Position,
		req.SpotifyDiscNumber,
		req.UseAlbumTrackNumber,
		req.ISRC,
	)
	targetPath := filepath.Join(req.OutputDir, expectedFilename)

	resolvedPath, alreadyExists := ResolveOutputPathForDownload(targetPath, GetRedownloadWithSuffixSetting())
	if alreadyExists {
		_ = os.Remove(sourcePath)
		return "EXISTS:" + resolvedPath, nil
	}

	if err := os.MkdirAll(filepath.Dir(resolvedPath), 0o755); err != nil {
		return "", err
	}

	sourceAbs, _ := filepath.Abs(sourcePath)
	targetAbs, _ := filepath.Abs(resolvedPath)
	if sourceAbs == targetAbs {
		return resolvedPath, nil
	}

	if err := moveFile(sourcePath, resolvedPath); err != nil {
		return "", err
	}

	return resolvedPath, nil
}

func applySoulseekMetadata(filePath string, req SoulseekDownloadRequest) error {
	if !strings.EqualFold(filepath.Ext(filePath), ".flac") {
		return nil
	}

	var coverPath string
	if req.CoverURL != "" {
		tmpFile, err := os.CreateTemp("", "spotiflac-soulseek-cover-*.jpg")
		if err == nil {
			coverPath = tmpFile.Name()
			tmpFile.Close()
			defer os.Remove(coverPath)

			coverClient := NewCoverClient()
			if err := coverClient.DownloadCoverToPath(req.CoverURL, coverPath, req.EmbedMaxQualityCover); err != nil {
				coverPath = ""
			}
		}
	}

	albumArtist := strings.TrimSpace(req.AlbumArtist)
	if albumArtist == "" {
		albumArtist = req.ArtistName
	}

	trackNumber := req.SpotifyTrackNumber
	if trackNumber == 0 {
		trackNumber = req.Position
	}

	metadata := Metadata{
		Title:       req.TrackName,
		Artist:      req.ArtistName,
		Album:       req.AlbumName,
		AlbumArtist: albumArtist,
		Separator:   req.Separator,
		Date:        extractYear(req.ReleaseDate),
		ReleaseDate: req.ReleaseDate,
		TrackNumber: trackNumber,
		TotalTracks: req.SpotifyTotalTracks,
		DiscNumber:  req.SpotifyDiscNumber,
		TotalDiscs:  req.SpotifyTotalDiscs,
		URL:         buildSoulseekSpotifyURL(req.SpotifyID),
		Copyright:   req.Copyright,
		Publisher:   req.Publisher,
		Composer:    req.Composer,
		ISRC:        req.ISRC,
	}

	if err := EmbedMetadata(filePath, metadata, coverPath); err != nil {
		return fmt.Errorf("failed to embed metadata into Soulseek fallback file: %w", err)
	}

	return nil
}

func moveFile(sourcePath, targetPath string) error {
	if err := os.Rename(sourcePath, targetPath); err == nil {
		return nil
	}

	sourceFile, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	targetFile, err := os.Create(targetPath)
	if err != nil {
		return err
	}

	if _, err := io.Copy(targetFile, sourceFile); err != nil {
		targetFile.Close()
		_ = os.Remove(targetPath)
		return err
	}
	if err := targetFile.Close(); err != nil {
		return err
	}

	return os.Remove(sourcePath)
}

func buildSoulseekSpotifyURL(spotifyID string) string {
	spotifyID = strings.TrimSpace(spotifyID)
	if spotifyID == "" {
		return ""
	}
	return "https://open.spotify.com/track/" + spotifyID
}

func isSoulseekTransferComplete(state int) bool {
	return slices.Contains(soulseekCompletedTransferStates, state)
}

func normalizeSoulseekText(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	replacer := strings.NewReplacer(
		"_", " ",
		"-", " ",
		".", " ",
		"/", " ",
		"\\", " ",
		"(", " ",
		")", " ",
		"[", " ",
		"]", " ",
	)
	value = replacer.Replace(value)
	return strings.Join(strings.Fields(value), " ")
}

func normalizedTokens(value string) []string {
	normalized := normalizeSoulseekText(value)
	if normalized == "" {
		return nil
	}
	return strings.Fields(normalized)
}

func tokenMatchScore(haystack string, tokens []string, perToken int) int {
	if len(tokens) == 0 {
		return 0
	}

	score := 0
	for _, token := range tokens {
		if len(token) < 2 {
			continue
		}
		if strings.Contains(haystack, token) {
			score += perToken
		}
	}
	return score
}

func filterEmptyStrings(values []string) []string {
	filtered := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			filtered = append(filtered, value)
		}
	}
	return filtered
}

func uniqueNonEmptyStrings(values ...string) []string {
	seen := map[string]struct{}{}
	unique := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
	}
	return unique
}

func getStringSetting(settings map[string]interface{}, key string) string {
	if settings == nil {
		return ""
	}
	value, _ := settings[key].(string)
	return strings.TrimSpace(value)
}

func getBoolSetting(settings map[string]interface{}, key string, fallback bool) bool {
	if settings == nil {
		return fallback
	}
	value, ok := settings[key].(bool)
	if !ok {
		return fallback
	}
	return value
}

func getIntSetting(settings map[string]interface{}, key string, fallback int) int {
	if settings == nil {
		return fallback
	}
	switch value := settings[key].(type) {
	case float64:
		return int(value)
	case float32:
		return int(value)
	case int:
		return value
	case int64:
		return int(value)
	case json.Number:
		if parsed, err := value.Int64(); err == nil {
			return int(parsed)
		}
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
			return parsed
		}
	}
	return fallback
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func absInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

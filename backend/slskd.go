package backend

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	slskdDefaultURL            = "http://localhost:5030"
	slskdPollInterval          = 1 * time.Second
	slskdSearchTimeout         = 15 * time.Second
	slskdTransferTimeout       = 45 * time.Minute
	slskdRecentFileGracePeriod = 10 * time.Minute
	slskdMaxDownloadAttempts   = 6
	// Quiet period (since the last received response) after which slskd finalizes a
	// search. slskd only exposes the response list once the search is complete, and
	// searches run serially (slskd allows one operation at a time), so this is the
	// per-tier latency — kept tight so a found track lands fast.
	slskdSearchQuietPeriod = 4 * time.Second
	// Total budget for the serial query ladder. Once spent, we stop trying the
	// looser relaxation tiers and let the caller fall back to streaming, instead of
	// walking every tier (~tiers × quiet period) on a track that isn't on Soulseek.
	slskdSearchBudget = 6 * time.Second
)

var slskdNonAlnumPattern = regexp.MustCompile(`[^a-z0-9]+`)

type SlskdDownloader struct {
	apiBase      string
	apiKey       string
	client       *http.Client
	downloadsDir string
	preferMp3    bool
	trackLabel   string
	emit         func(SlskdLogEvent)
}

// SlskdLogEvent is a single diagnostic step in a slskd download. It is printed to
// stdout, appended to ~/.spotiflac/slskd/slskd-activity.log, and (when a logger is
// attached) emitted to the UI as a "slskd:log" event so the previously opaque
// search/peer/transfer flow becomes visible after the user clicks download.
type SlskdLogEvent struct {
	Phase    string `json:"phase"`   // search | peer | transfer | result
	Level    string `json:"level"`   // info | success | warn | error
	Message  string `json:"message"`
	Track    string `json:"track,omitempty"`
	Query    string `json:"query,omitempty"`
	Username string `json:"username,omitempty"`
	Filename string `json:"filename,omitempty"`
	Count    int    `json:"count,omitempty"`
}

type SlskdSearchRequest struct {
	ID             string `json:"id"`
	SearchText     string `json:"searchText"`
	ResponseLimit  int    `json:"responseLimit,omitempty"`
	FileLimit      int    `json:"fileLimit,omitempty"`
	SearchTimeout  int    `json:"searchTimeout,omitempty"`
	FilterResponse bool   `json:"filterResponses,omitempty"`
}

type SlskdSearchState struct {
	ID            string `json:"id"`
	SearchText    string `json:"searchText"`
	State         string `json:"state"`
	IsComplete    bool   `json:"isComplete"`
	ResponseCount int    `json:"responseCount"`
	FileCount     int    `json:"fileCount"`
}

type SlskdFile struct {
	Filename          string `json:"filename"`
	Size              int64  `json:"size"`
	Extension         string `json:"extension"`
	BitRate           int    `json:"bitRate"`
	BitDepth          int    `json:"bitDepth"`
	SampleRate        int    `json:"sampleRate"`
	IsVariableBitRate bool   `json:"isVariableBitRate"`
	Length            int    `json:"length"`
}

type SlskdSearchResponse struct {
	Username          string      `json:"username"`
	HasFreeUploadSlot bool        `json:"hasFreeUploadSlot"`
	QueueLength       int         `json:"queueLength"`
	UploadSpeed       int64       `json:"uploadSpeed"`
	FileCount         int         `json:"fileCount"`
	LockedFileCount   int         `json:"lockedFileCount"`
	Files             []SlskdFile `json:"files"`
	LockedFiles       []SlskdFile `json:"lockedFiles"`
}

type SlskdTransfer struct {
	ID              string  `json:"id"`
	Username        string  `json:"username"`
	Filename        string  `json:"filename"`
	Size            int64   `json:"size"`
	State           string  `json:"state"`
	PercentComplete float64 `json:"percentComplete"`
	Exception       string  `json:"exception"`
}

type SlskdTransferDirectory struct {
	Files []SlskdTransfer `json:"files"`
}

type SlskdTransferUser struct {
	Username    string                   `json:"username"`
	Directories []SlskdTransferDirectory `json:"directories"`
}

type slskdScoredCandidate struct {
	Response SlskdSearchResponse
	File     SlskdFile
	Score    int
}

func NewSlskdDownloader() (*SlskdDownloader, error) {
	settings, err := LoadConfigSettings()
	if err != nil || settings == nil {
		return nil, fmt.Errorf("slskd settings are not configured")
	}

	preferMp3, _ := settings["slskdPreferMp3"].(bool)

	rawURL, _ := settings["slskdURL"].(string)
	apiKey, _ := settings["slskdAPIKey"].(string)
	downloadsDir, _ := settings["slskdDownloadsDir"].(string)

	baseURL := strings.TrimSpace(rawURL)
	if baseURL == "" {
		baseURL = slskdDefaultURL
	}
	baseURL = strings.TrimRight(baseURL, "/")

	apiBase := baseURL
	if !strings.HasSuffix(strings.ToLower(apiBase), "/api/v0") {
		apiBase += "/api/v0"
	}

	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("slskd API key is required")
	}

	downloadsDir = strings.TrimSpace(downloadsDir)
	if downloadsDir == "" {
		return nil, fmt.Errorf("slskd downloads directory is required")
	}

	info, statErr := os.Stat(downloadsDir)
	if statErr != nil || !info.IsDir() {
		return nil, fmt.Errorf("slskd downloads directory is invalid: %s", downloadsDir)
	}

	return &SlskdDownloader{
		apiBase: apiBase,
		apiKey:  strings.TrimPrefix(apiKey, "Bearer "),
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		downloadsDir: downloadsDir,
		preferMp3:    preferMp3,
	}, nil
}

// SetLogger attaches a sink for diagnostic events (the App wires this to a Wails
// "slskd:log" event so the UI can show live progress).
func (s *SlskdDownloader) SetLogger(emit func(SlskdLogEvent)) {
	s.emit = emit
}

func (s *SlskdDownloader) logEvent(ev SlskdLogEvent) {
	if ev.Level == "" {
		ev.Level = "info"
	}
	if ev.Track == "" {
		ev.Track = s.trackLabel
	}
	fmt.Printf("[slskd:%s] %s\n", ev.Phase, ev.Message)
	appendSlskdActivityLog(ev)
	if s.emit != nil {
		s.emit(ev)
	}
}

// appendSlskdActivityLog writes a human-readable line to a persistent activity log
// so there is a record even in a packaged .app where stdout is not visible.
func appendSlskdActivityLog(ev SlskdLogEvent) {
	appDir, err := EnsureAppDir()
	if err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(appDir, "slskd", "slskd-activity.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()

	track := ev.Track
	if track == "" {
		track = "-"
	}
	fmt.Fprintf(f, "[%s] [%s/%s] [%s] %s\n", time.Now().Format("2006-01-02 15:04:05"), ev.Phase, ev.Level, track, ev.Message)
}

func (s *SlskdDownloader) addHeaders(req *http.Request) {
	req.Header.Set("User-Agent", songLinkUserAgent)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", s.apiKey)
}

func (s *SlskdDownloader) doJSON(method, path string, payload interface{}, out interface{}) error {
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("failed to encode slskd request: %w", err)
		}
		body = strings.NewReader(string(data))
	}

	req, err := http.NewRequest(method, s.apiBase+path, body)
	if err != nil {
		return err
	}
	s.addHeaders(req)

	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet := strings.TrimSpace(string(respBody))
		if len(snippet) > 300 {
			snippet = snippet[:300] + "..."
		}
		if snippet == "" {
			return fmt.Errorf("slskd API returned status %d", resp.StatusCode)
		}
		return fmt.Errorf("slskd API returned status %d: %s", resp.StatusCode, snippet)
	}

	if out == nil || len(respBody) == 0 {
		return nil
	}

	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("failed to decode slskd response: %w", err)
	}

	return nil
}

func normalizeSlskdText(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "\\", " ")
	value = strings.ReplaceAll(value, "/", " ")
	value = slskdNonAlnumPattern.ReplaceAllString(value, " ")
	return strings.Join(strings.Fields(value), " ")
}

func tokenizeSlskdText(value string) []string {
	normalized := normalizeSlskdText(value)
	if normalized == "" {
		return nil
	}

	parts := strings.Fields(normalized)
	tokens := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) >= 2 {
			tokens = append(tokens, part)
		}
	}
	return tokens
}

func slskdContainsAllTokens(haystack string, tokens []string) bool {
	if len(tokens) == 0 {
		return false
	}
	for _, token := range tokens {
		if !strings.Contains(haystack, token) {
			return false
		}
	}
	return true
}

func slskdTokenMatchScore(haystack string, tokens []string, points int) int {
	score := 0
	for _, token := range tokens {
		if strings.Contains(haystack, token) {
			score += points
		}
	}
	return score
}

func slskdIsLosslessExtension(ext string) bool {
	switch strings.ToLower(strings.TrimSpace(ext)) {
	case ".flac", ".wav", ".ape", ".wv", ".aiff", ".aif", ".alac":
		return true
	default:
		return false
	}
}

func slskdIsAudioExtension(ext string) bool {
	switch strings.ToLower(strings.TrimSpace(ext)) {
	case ".flac", ".wav", ".ape", ".wv", ".aiff", ".aif", ".alac", ".m4a", ".aac", ".mp3", ".ogg", ".opus":
		return true
	default:
		return false
	}
}

func slskdExtensionScore(ext string, requestedLossless bool, preferLossy bool) int {
	if preferLossy {
		// Prefer lossy (esp. MP3) over lossless. Rank MP3 highest, then other
		// lossy formats, then lossless below them as a last resort.
		switch strings.ToLower(strings.TrimSpace(ext)) {
		case ".mp3":
			return 3000
		case ".m4a", ".aac":
			return 2200
		case ".ogg", ".opus":
			return 2000
		case ".flac", ".wav", ".aiff", ".aif", ".ape", ".wv", ".alac":
			return 1200
		default:
			return 0
		}
	}
	switch strings.ToLower(strings.TrimSpace(ext)) {
	case ".flac":
		return 3000
	case ".wav", ".aiff", ".aif":
		return 2800
	case ".ape", ".wv", ".alac":
		return 2600
	case ".m4a":
		if requestedLossless {
			return 1200
		}
		return 1700
	case ".aac":
		return 1400
	case ".mp3":
		return 1500
	case ".ogg", ".opus":
		return 1300
	default:
		return 0
	}
}

func slskdRequestedLosslessFormat(audioFormat string) bool {
	switch strings.ToLower(strings.TrimSpace(audioFormat)) {
	case "", "flac", "lossless", "hi_res_lossless", "hi_res", "6", "7", "27":
		return true
	default:
		return false
	}
}

func buildSlskdSearchQuery(query, trackName, artistName, albumName string) string {
	if strings.TrimSpace(query) != "" {
		return strings.TrimSpace(query)
	}

	parts := make([]string, 0, 3)
	for _, part := range []string{trackName, artistName, albumName} {
		part = strings.TrimSpace(part)
		if part != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, " ")
}

// slskdFirstArtist returns the primary artist from a possibly multi-artist credit.
// Soulseek matches every search token against the peer's path (AND semantics), so
// ANDing in a secondary artist that isn't in the shared path silently kills the
// search — observed with "Refracted Alex Heffes; Ryuichi Sakamoto" (0 responses)
// vs "Refracted Alex Heffes" (matches). Unlike GetFirstArtist this also splits on
// the ";" separator the app uses for multiple artists.
func slskdFirstArtist(artist string) string {
	artist = strings.TrimSpace(artist)
	if artist == "" {
		return ""
	}
	lower := strings.ToLower(artist)
	cut := len(artist)
	for _, sep := range []string{";", ",", " & ", "/", " feat.", " feat ", " ft.", " ft ", " featuring ", " x ", " vs ", " vs. ", " with "} {
		if idx := strings.Index(lower, sep); idx >= 0 && idx < cut {
			cut = idx
		}
	}
	first := strings.TrimSpace(artist[:cut])
	if first == "" {
		return artist
	}
	return first
}

// buildSlskdSearchLadder returns the precise query first, then progressively
// relaxed fallbacks. Soulseek requires a peer path to contain EVERY token, so a
// single over-specified term (commonly a second artist) can yield 0 responses for
// a track that is in fact well-seeded. Each tier still pins the track name plus a
// strong discriminator (first artist or album), so relaxation does not pull in
// unrelated files. Tiers are de-duplicated and empty fields skipped.
func buildSlskdSearchLadder(primary, track, artist, album string) []string {
	track = strings.TrimSpace(track)
	album = strings.TrimSpace(album)

	ladder := make([]string, 0, 4)
	seen := make(map[string]bool)
	add := func(q string) {
		q = strings.Join(strings.Fields(q), " ")
		if q == "" {
			return
		}
		key := strings.ToLower(q)
		if seen[key] {
			return
		}
		seen[key] = true
		ladder = append(ladder, q)
	}

	add(primary)
	if track != "" {
		first := slskdFirstArtist(artist)
		if first != "" {
			add(track + " " + first)
		}
		if album != "" {
			add(track + " " + album)
		}
		if first != "" && album != "" {
			add(track + " " + first + " " + album)
		}
	}
	return ladder
}

func (s *SlskdDownloader) waitForSearch(searchID string) (SlskdSearchState, error) {
	deadline := time.Now().Add(slskdSearchTimeout)
	var lastState SlskdSearchState
	for time.Now().Before(deadline) {
		var state SlskdSearchState
		if err := s.doJSON(http.MethodGet, "/searches/"+searchID, nil, &state); err == nil {
			lastState = state
			if state.IsComplete {
				return state, nil
			}
		}
		time.Sleep(750 * time.Millisecond)
	}
	return lastState, nil
}

func (s *SlskdDownloader) fetchSearchResponses(query string) (string, []SlskdSearchResponse, error) {
	searchID := uuid.NewString()
	searchReq := SlskdSearchRequest{
		ID:             searchID,
		SearchText:     query,
		ResponseLimit:  50,
		FileLimit:      1000,
		SearchTimeout:  int(slskdSearchQuietPeriod / time.Millisecond),
		FilterResponse: true,
	}

	if err := s.doJSON(http.MethodPost, "/searches", searchReq, nil); err != nil {
		return "", nil, err
	}

	// Wait for the search to FULLY complete before reading responses. slskd only
	// exposes the response list once the search is finished; reading it early (or
	// breaking on the first non-empty result) yields a partial set — typically
	// just the fast "ghost" indexer — and hides the slower real peers.
	state, _ := s.waitForSearch(searchID)
	if !state.IsComplete {
		deadline := time.Now().Add(slskdSearchTimeout)
		for time.Now().Before(deadline) {
			time.Sleep(750 * time.Millisecond)
			if err := s.doJSON(http.MethodGet, "/searches/"+searchID, nil, &state); err != nil {
				continue
			}
			if state.IsComplete {
				break
			}
		}
	}

	var responses []SlskdSearchResponse
	if err := s.doJSON(http.MethodGet, "/searches/"+searchID+"/responses", nil, &responses); err != nil {
		return searchID, nil, err
	}

	return searchID, responses, nil
}

func scoreSlskdCandidate(response SlskdSearchResponse, file SlskdFile, requestedLossless bool, preferLossy bool, expectedDuration int, trackName, artistName, albumName string) int {
	score := 0
	ext := filepath.Ext(file.Filename)
	if ext == "" && file.Extension != "" {
		ext = "." + strings.TrimPrefix(file.Extension, ".")
	}
	normExt := strings.ToLower(strings.TrimSpace(ext))

	score += slskdExtensionScore(ext, requestedLossless, preferLossy)
	if requestedLossless && slskdIsLosslessExtension(ext) {
		score += 600
	}
	if requestedLossless && !slskdIsLosslessExtension(ext) {
		score -= 400
	}

	if preferLossy {
		// Favor MP3, and especially ~320 kbps, while pushing lossless below it.
		if normExt == ".mp3" {
			score += 600
			if file.BitRate >= 256 {
				score += 300
			}
		} else if slskdIsLosslessExtension(ext) {
			score -= 300
		}
	}

	score += min(file.BitRate, 320)
	score += min(file.BitDepth*50, 1600)
	score += min(file.SampleRate/100, 2000)

	filenameText := normalizeSlskdText(file.Filename)
	trackTokens := tokenizeSlskdText(trackName)
	artistTokens := tokenizeSlskdText(artistName)
	albumTokens := tokenizeSlskdText(albumName)

	score += slskdTokenMatchScore(filenameText, trackTokens, 180)
	score += slskdTokenMatchScore(filenameText, artistTokens, 120)
	score += slskdTokenMatchScore(filenameText, albumTokens, 60)

	if slskdContainsAllTokens(filenameText, trackTokens) {
		score += 500
	}
	if slskdContainsAllTokens(filenameText, artistTokens) {
		score += 350
	}
	if albumTokens != nil && slskdContainsAllTokens(filenameText, albumTokens) {
		score += 120
	}

	if response.HasFreeUploadSlot {
		score += 100
	}
	score += min(int(response.UploadSpeed/1024), 200)
	score -= min(response.QueueLength, 100)

	if expectedDuration > 0 && file.Length > 0 {
		diff := expectedDuration - file.Length
		if diff < 0 {
			diff = -diff
		}
		if diff <= 5 {
			score += 400
		} else if diff <= 15 {
			score += 150
		} else {
			score -= min(diff*10, 800)
		}
	}

	return score
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (s *SlskdDownloader) rankCandidates(responses []SlskdSearchResponse, requestedLossless bool, preferLossy bool, expectedDuration int, trackName, artistName, albumName string) ([]slskdScoredCandidate, error) {
	candidates := make([]slskdScoredCandidate, 0)
	totalFiles := 0
	audioLikeFiles := 0
	lockedFiles := 0
	for _, response := range responses {
		totalFiles += len(response.Files)
		lockedFiles += len(response.LockedFiles)
		for _, file := range response.Files {
			ext := filepath.Ext(file.Filename)
			if ext == "" && file.Extension != "" {
				ext = "." + strings.TrimPrefix(file.Extension, ".")
			}
			if !slskdIsAudioExtension(ext) {
				continue
			}
			audioLikeFiles++

			candidates = append(candidates, slskdScoredCandidate{
				Response: response,
				File:     file,
				Score:    scoreSlskdCandidate(response, file, requestedLossless, preferLossy, expectedDuration, trackName, artistName, albumName),
			})
		}
	}

	if len(candidates) == 0 {
		switch {
		case len(responses) == 0:
			return nil, fmt.Errorf("slskd search returned no responses")
		case totalFiles == 0 && lockedFiles > 0:
			return nil, fmt.Errorf("slskd search returned only locked files")
		case totalFiles == 0:
			return nil, fmt.Errorf("slskd search returned responses with no downloadable files")
		case audioLikeFiles == 0:
			return nil, fmt.Errorf("slskd search returned files, but none looked like audio")
		default:
			return nil, fmt.Errorf("slskd search returned no usable audio files")
		}
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].Score > candidates[j].Score
	})

	return candidates, nil
}

func flattenSlskdTransfers(groups []SlskdTransferUser) []SlskdTransfer {
	flat := make([]SlskdTransfer, 0)
	for _, group := range groups {
		for _, directory := range group.Directories {
			flat = append(flat, directory.Files...)
		}
	}
	return flat
}

func (s *SlskdDownloader) getTransfers() ([]SlskdTransfer, error) {
	var groups []SlskdTransferUser
	if err := s.doJSON(http.MethodGet, "/transfers/downloads", nil, &groups); err != nil {
		return nil, err
	}
	return flattenSlskdTransfers(groups), nil
}

func (s *SlskdDownloader) findTransfer(filename string, size int64, transfers []SlskdTransfer) *SlskdTransfer {
	for _, transfer := range transfers {
		if transfer.Size == size && strings.EqualFold(transfer.Filename, filename) {
			found := transfer
			return &found
		}
	}
	return nil
}

func (s *SlskdDownloader) enqueueAndWait(candidate *slskdScoredCandidate) (*SlskdTransfer, time.Time, error) {
	startedAt := time.Now()

	body := []map[string]interface{}{
		{
			"filename": candidate.File.Filename,
			"size":     candidate.File.Size,
		},
	}

	path := "/transfers/downloads/" + candidate.Response.Username
	if err := s.doJSON(http.MethodPost, path, body, nil); err != nil {
		return nil, startedAt, err
	}

	deadline := time.Now().Add(slskdTransferTimeout)
	for time.Now().Before(deadline) {
		transfers, err := s.getTransfers()
		if err == nil {
			if transfer := s.findTransfer(candidate.File.Filename, candidate.File.Size, transfers); transfer != nil {
				switch strings.TrimSpace(transfer.State) {
				case "Completed, Succeeded":
					return transfer, startedAt, nil
				case "Completed, Cancelled", "Completed, TimedOut", "Completed, Errored", "Completed, Rejected":
					if strings.TrimSpace(transfer.Exception) != "" {
						return nil, startedAt, fmt.Errorf("slskd transfer failed: %s (%s)", transfer.State, strings.TrimSpace(transfer.Exception))
					}
					return nil, startedAt, fmt.Errorf("slskd transfer failed: %s", transfer.State)
				}
			}
		}
		time.Sleep(slskdPollInterval)
	}

	return nil, startedAt, fmt.Errorf("slskd transfer timed out")
}

func slskdRemoteBaseName(filename string) string {
	filename = strings.ReplaceAll(filename, "\\", "/")
	return filepath.Base(filename)
}

func (s *SlskdDownloader) findDownloadedFile(remoteFilename string, size int64, startedAt time.Time) (string, error) {
	baseName := strings.ToLower(slskdRemoteBaseName(remoteFilename))
	type match struct {
		path    string
		sizeHit bool
		modTime time.Time
	}

	matches := make([]match, 0)
	cutoff := startedAt.Add(-slskdRecentFileGracePeriod)

	err := filepath.Walk(s.downloadsDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info == nil || info.IsDir() {
			return nil
		}

		if info.ModTime().Before(cutoff) {
			return nil
		}

		if strings.ToLower(info.Name()) != baseName {
			return nil
		}

		matches = append(matches, match{
			path:    path,
			sizeHit: size <= 0 || info.Size() == size,
			modTime: info.ModTime(),
		})
		return nil
	})
	if err != nil {
		return "", err
	}

	if len(matches) == 0 {
		return "", fmt.Errorf("slskd completed the transfer but the file was not found under %s", s.downloadsDir)
	}

	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].sizeHit != matches[j].sizeHit {
			return matches[i].sizeHit
		}
		return matches[i].modTime.After(matches[j].modTime)
	})

	return matches[0].path, nil
}

func copySlskdFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}

	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(dst)
		return err
	}

	if err := out.Close(); err != nil {
		return err
	}

	if info, err := os.Stat(src); err == nil {
		_ = os.Chmod(dst, info.Mode())
	}

	return nil
}

func (s *SlskdDownloader) Download(query, outputDir, quality, filenameFormat, playlistName, playlistOwner string, includeTrackNumber bool, position int, spotifyTrackName, spotifyArtistName, spotifyAlbumName, spotifyAlbumArtist, spotifyReleaseDate, spotifyCoverURL string, spotifyTrackNumber, spotifyDiscNumber, spotifyTotalTracks int, embedMaxQualityCover bool, spotifyTotalDiscs int, spotifyCopyright, spotifyPublisher, spotifyComposer, metadataSeparator, isrcOverride, spotifyURL string, duration int, useFirstArtistOnly bool, useSingleGenre bool, embedGenre bool) (string, error) {
	if outputDir != "." {
		if err := os.MkdirAll(outputDir, 0755); err != nil {
			return "", fmt.Errorf("failed to create output directory: %w", err)
		}
	}

	type mbResult struct {
		ISRC     string
		Metadata Metadata
	}

	metaChan := make(chan mbResult, 1)
	if embedGenre && spotifyURL != "" {
		go func() {
			res := mbResult{}
			var isrc string
			parts := strings.Split(spotifyURL, "/")
			if len(parts) > 0 {
				sID := strings.Split(parts[len(parts)-1], "?")[0]
				if sID != "" {
					client := NewSongLinkClient()
					if val, err := client.GetISRC(sID); err == nil {
						isrc = val
					}
				}
			}
			res.ISRC = isrc
			if isrc != "" {
				if ShouldSkipMusicBrainzMetadataFetch() {
					fmt.Println("Skipping MusicBrainz metadata fetch because status check is offline.")
				} else if fetchedMeta, err := FetchMusicBrainzMetadata(isrc, spotifyTrackName, spotifyArtistName, spotifyAlbumName, useSingleGenre, embedGenre); err == nil {
					res.Metadata = fetchedMeta
				}
			}
			metaChan <- res
		}()
	} else {
		close(metaChan)
	}

	s.trackLabel = strings.TrimSpace(spotifyTrackName)
	if s.trackLabel == "" {
		s.trackLabel = strings.TrimSpace(query)
	}

	searchQuery := buildSlskdSearchQuery(query, spotifyTrackName, spotifyArtistName, spotifyAlbumName)
	if searchQuery == "" {
		return "", fmt.Errorf("slskd search query is empty")
	}

	preferLossy := s.preferMp3
	requestedLossless := slskdRequestedLosslessFormat(quality) && !preferLossy

	// Search with a relaxation ladder: the precise query first, then progressively
	// looser fallbacks. A single over-specified token (often a second artist) can
	// make Soulseek's AND-match return 0 responses for a well-seeded track; the
	// looser tiers recover it without pulling in unrelated files.
	ladder := buildSlskdSearchLadder(searchQuery, spotifyTrackName, spotifyArtistName, spotifyAlbumName)
	s.logEvent(SlskdLogEvent{Phase: "search", Message: fmt.Sprintf("searching slskd (%d query tier(s))", len(ladder)), Query: ladder[0]})

	// slskd permits only ONE search operation at a time — concurrent POST /searches
	// returns HTTP 429 ("Only one concurrent operation is permitted"), so the ladder
	// MUST run serially. Walk it precise-query-first and stop at the first tier that
	// yields usable candidates: most tracks match on tier 1, so we pay a single
	// quiet period; only genuine misses fall through to the looser tiers. The quiet
	// period itself is kept short (slskdSearchQuietPeriod) to bound that cost.
	var candidates []slskdScoredCandidate
	var searchErr error
	searchStart := time.Now()
	for i, q := range ladder {
		// Time-cap the ladder. The precise tier (i == 0) always runs; past that,
		// bail if the budget is spent so a miss falls back to streaming quickly
		// instead of grinding through every looser tier.
		if i > 0 && time.Since(searchStart) >= slskdSearchBudget {
			s.logEvent(SlskdLogEvent{Phase: "search", Level: "warn", Message: fmt.Sprintf("search budget (%s) reached after %d tier(s); falling back to streaming", slskdSearchBudget, i)})
			break
		}
		s.logEvent(SlskdLogEvent{Phase: "search", Query: q, Message: fmt.Sprintf("query %d/%d: %q", i+1, len(ladder), q)})
		_, responses, fErr := s.fetchSearchResponses(q)
		if fErr != nil {
			searchErr = fErr
			s.logEvent(SlskdLogEvent{Phase: "search", Level: "error", Query: q, Message: fmt.Sprintf("search request failed: %v", fErr)})
			continue
		}
		ranked, rankErr := s.rankCandidates(responses, requestedLossless, preferLossy, duration, spotifyTrackName, spotifyArtistName, spotifyAlbumName)
		if rankErr != nil {
			searchErr = rankErr
			s.logEvent(SlskdLogEvent{Phase: "search", Level: "warn", Query: q, Count: len(responses), Message: fmt.Sprintf("%q → %d peer(s), no usable files (%v)", q, len(responses), rankErr)})
			continue
		}
		candidates = ranked
		if i == 0 {
			s.logEvent(SlskdLogEvent{Phase: "search", Level: "success", Query: q, Count: len(candidates), Message: fmt.Sprintf("%q → %d candidate file(s) from %d peer(s)", q, len(candidates), len(responses))})
		} else {
			s.logEvent(SlskdLogEvent{Phase: "search", Level: "success", Query: q, Count: len(candidates), Message: fmt.Sprintf("recovered with relaxed query %q (%d candidate(s) from %d peer(s)) after %d stricter attempt(s) returned nothing", q, len(candidates), len(responses), i)})
		}
		break
	}

	if len(candidates) == 0 {
		if searchErr != nil {
			s.logEvent(SlskdLogEvent{Phase: "result", Level: "error", Message: fmt.Sprintf("no result after %d query tier(s): %v", len(ladder), searchErr)})
			return "", searchErr
		}
		s.logEvent(SlskdLogEvent{Phase: "result", Level: "error", Message: "no result: slskd search returned no responses"})
		return "", fmt.Errorf("slskd search returned no responses")
	}

	// Try peers in ranked order until one actually serves the file. The top
	// candidate is often a "ghost" indexer that lists files but rejects every
	// transfer ("File not shared"), or a peer that has gone offline. Without
	// this fallback a single bad source fails the whole download even when
	// other peers have the track.
	var candidate *slskdScoredCandidate
	var transfer *SlskdTransfer
	var startedAt time.Time
	var err error
	failedUsers := make(map[string]bool)
	attempts := 0
	for i := range candidates {
		c := &candidates[i]
		if failedUsers[c.Response.Username] {
			continue
		}
		if attempts >= slskdMaxDownloadAttempts {
			break
		}
		attempts++

		s.logEvent(SlskdLogEvent{Phase: "peer", Username: c.Response.Username, Filename: slskdRemoteBaseName(c.File.Filename), Message: fmt.Sprintf("attempt %d/%d — requesting %q from %s", attempts, slskdMaxDownloadAttempts, slskdRemoteBaseName(c.File.Filename), c.Response.Username)})
		t, st, enqErr := s.enqueueAndWait(c)
		if enqErr == nil {
			candidate = c
			transfer = t
			startedAt = st
			err = nil
			s.logEvent(SlskdLogEvent{Phase: "peer", Level: "success", Username: c.Response.Username, Filename: slskdRemoteBaseName(c.File.Filename), Message: fmt.Sprintf("downloading from %s", c.Response.Username)})
			break
		}

		err = enqErr
		failedUsers[c.Response.Username] = true
		s.logEvent(SlskdLogEvent{Phase: "peer", Level: "warn", Username: c.Response.Username, Message: fmt.Sprintf("peer %s rejected/failed (%v) — trying next", c.Response.Username, enqErr)})
	}

	if candidate == nil || transfer == nil {
		if err != nil {
			s.logEvent(SlskdLogEvent{Phase: "result", Level: "error", Message: fmt.Sprintf("all %d peer attempt(s) failed: %v", attempts, err)})
			return "", err
		}
		s.logEvent(SlskdLogEvent{Phase: "result", Level: "error", Message: "no peer could serve the requested file"})
		return "", fmt.Errorf("slskd: no peer could serve the requested file")
	}

	sourcePath, err := s.findDownloadedFile(transfer.Filename, transfer.Size, startedAt)
	if err != nil {
		s.logEvent(SlskdLogEvent{Phase: "transfer", Level: "error", Username: transfer.Username, Message: fmt.Sprintf("transfer reported complete but file not found: %v", err)})
		return "", err
	}

	isrc := strings.TrimSpace(isrcOverride)
	var mbMeta Metadata
	if spotifyURL != "" {
		result := <-metaChan
		if isrc == "" {
			isrc = result.ISRC
		}
		mbMeta = result.Metadata
	}

	filenameArtist := spotifyArtistName
	filenameAlbumArtist := spotifyAlbumArtist
	if useFirstArtistOnly {
		filenameArtist = GetFirstArtist(spotifyArtistName)
		filenameAlbumArtist = GetFirstArtist(spotifyAlbumArtist)
	}

	filenameBase := BuildExpectedFilenameBase(spotifyTrackName, filenameArtist, spotifyAlbumName, filenameAlbumArtist, spotifyReleaseDate, filenameFormat, playlistName, playlistOwner, includeTrackNumber, position, spotifyDiscNumber, false, isrc)
	ext := strings.ToLower(filepath.Ext(sourcePath))
	if ext == "" {
		ext = strings.ToLower(filepath.Ext(candidate.File.Filename))
	}
	if ext == "" && strings.TrimSpace(candidate.File.Extension) != "" {
		ext = "." + strings.TrimPrefix(strings.ToLower(candidate.File.Extension), ".")
	}
	if ext == "" {
		ext = ".mp3"
	}

	targetPath := filepath.Join(outputDir, filenameBase+ext)
	targetPath, alreadyExists := ResolveOutputPathForDownload(targetPath, GetRedownloadWithSuffixSetting())
	if alreadyExists {
		return "EXISTS:" + targetPath, nil
	}

	if err := copySlskdFile(sourcePath, targetPath); err != nil {
		return "", fmt.Errorf("failed to copy downloaded slskd file: %w", err)
	}

	coverPath := ""
	if spotifyCoverURL != "" {
		coverPath = targetPath + ".cover.jpg"
		coverClient := NewCoverClient()
		if err := coverClient.DownloadCoverToPath(spotifyCoverURL, coverPath, embedMaxQualityCover); err != nil {
			fmt.Printf("Warning: Failed to download Spotify cover: %v\n", err)
			coverPath = ""
		} else {
			defer os.Remove(coverPath)
		}
	}

	trackNumberToEmbed := spotifyTrackNumber
	if trackNumberToEmbed == 0 {
		trackNumberToEmbed = 1
	}

	metadata := Metadata{
		Title:       spotifyTrackName,
		Artist:      spotifyArtistName,
		Album:       spotifyAlbumName,
		AlbumArtist: spotifyAlbumArtist,
		Date:        spotifyReleaseDate,
		TrackNumber: trackNumberToEmbed,
		TotalTracks: spotifyTotalTracks,
		DiscNumber:  spotifyDiscNumber,
		TotalDiscs:  spotifyTotalDiscs,
		URL:         spotifyURL,
		Comment:     spotifyURL,
		Copyright:   spotifyCopyright,
		Publisher:   spotifyPublisher,
		Composer:    spotifyComposer,
		Separator:   metadataSeparator,
		Description: "https://github.com/spotbye/SpotiFLAC",
		ISRC:        isrc,
		Genre:       mbMeta.Genre,
	}

	if err := EmbedMetadataToConvertedFile(targetPath, metadata, coverPath); err != nil {
		fmt.Printf("Warning: Failed to embed metadata for slskd file: %v\n", err)
	}

	s.logEvent(SlskdLogEvent{Phase: "result", Level: "success", Filename: filepath.Base(targetPath), Message: fmt.Sprintf("downloaded successfully → %s", filepath.Base(targetPath))})
	fmt.Println("✓ Downloaded successfully from slskd")
	return targetPath, nil
}

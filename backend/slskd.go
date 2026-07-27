package backend

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/google/uuid"
	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

const (
	slskdDefaultURL            = "http://localhost:5030"
	slskdPollInterval          = 1 * time.Second
	slskdSearchTimeout         = 15 * time.Second
	slskdTransferTimeout       = 45 * time.Minute
	slskdRecentFileGracePeriod = 10 * time.Minute
	slskdMaxDownloadAttempts   = 6
	// Fast-fail guards for a single peer attempt. Without these, a peer whose
	// transfer never starts (offline, long remote queue, ghost share) pins the
	// whole download on the blind 45-minute cap — which reads as a hang in the
	// UI. slskdTransferTimeout stays as the absolute cap so a large file that IS
	// moving, however slowly, is never cut off.
	slskdTransferAppearTimeout = 30 * time.Second // enqueue → transfer visible in slskd's queue
	slskdTransferQueuedTimeout = 2 * time.Minute  // stuck in Requested/Queued/Initializing with no bytes
	slskdTransferStallTimeout  = 90 * time.Second // InProgress but percentComplete not advancing
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
			// slskd's Kestrel occasionally writes a stray "400 Bad Request" into a
			// kept-alive connection sitting idle between transfer polls, which Go
			// surfaces as a logged "Unsolicited response received on idle HTTP
			// channel" warning. Polling is ~1 req/s against a (usually local)
			// server, so per-request connections cost nothing and remove the noise.
			Transport: &http.Transport{
				DisableKeepAlives: true,
			},
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

var slskdDiacriticStripper = transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)

var slskdSpecialLatinReplacer = strings.NewReplacer(
	"ø", "o", "Ø", "O",
	"æ", "ae", "Æ", "AE",
	"œ", "oe", "Œ", "OE",
	"ß", "ss",
	"ł", "l", "Ł", "L",
	"đ", "d", "Đ", "D",
	"þ", "th", "Þ", "Th",
	"ð", "d", "Ð", "D",
)

// foldSlskdDiacritics transliterates accented Latin text to plain ASCII
// ("Vasaré" → "Vasare", "Ugnė" → "Ugne"). Soulseek matches every query token
// literally against peers' file paths, and peers overwhelmingly name files in
// ASCII — one accented token in a query guarantees 0 results for those files.
func foldSlskdDiacritics(value string) string {
	value = slskdSpecialLatinReplacer.Replace(value)
	if folded, _, err := transform.String(slskdDiacriticStripper, value); err == nil {
		return folded
	}
	return value
}

func normalizeSlskdText(value string) string {
	value = strings.ToLower(strings.TrimSpace(foldSlskdDiacritics(value)))
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

	// Queries go out ASCII-folded: peers name files transliterated, so an
	// accented token ("Vasaré") matches nothing even when the track is well
	// seeded. The unfolded primary query runs as a last-resort tier for the
	// rare peer who keeps diacritics in file names.
	addFolded := func(q string) { add(foldSlskdDiacritics(q)) }

	addFolded(primary)
	if track != "" {
		first := slskdFirstArtist(artist)
		if first != "" {
			addFolded(track + " " + first)
		}
		if album != "" {
			addFolded(track + " " + album)
		}
		if first != "" && album != "" {
			addFolded(track + " " + first + " " + album)
		}
	}
	add(primary)
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

func (s *SlskdDownloader) findTransfer(username, filename string, size int64, transfers []SlskdTransfer) *SlskdTransfer {
	for _, transfer := range transfers {
		if transfer.Size == size && strings.EqualFold(transfer.Username, username) && strings.EqualFold(transfer.Filename, filename) {
			found := transfer
			return &found
		}
	}
	return nil
}

// cancelTransfer best-effort cancels an enqueued download so an abandoned peer
// attempt doesn't keep occupying slskd's queue (and the peer's upload slot)
// while we move on to the next candidate.
func (s *SlskdDownloader) cancelTransfer(username, id string) {
	if strings.TrimSpace(id) == "" {
		return
	}
	path := "/transfers/downloads/" + url.PathEscape(username) + "/" + url.PathEscape(id)
	_ = s.doJSON(http.MethodDelete, path, nil, nil)
}

// slskdAlbumPeer remembers which peer's remote folder served a track of an
// album, so sibling tracks download straight from that folder instead of
// re-rolling the Soulseek search lottery (peers online, reply rate limits,
// query-token mismatches) once per track.
type slskdAlbumPeer struct {
	Username  string
	Directory string
	Files     []SlskdFile // full remote paths, audio only
}

const slskdAlbumPeerCacheMax = 32

// Package-level: a fresh SlskdDownloader is constructed per track, so the
// cache must outlive any single downloader to span an album.
var (
	slskdAlbumPeersMu   sync.Mutex
	slskdAlbumPeers     = make(map[string]*slskdAlbumPeer)
	slskdAlbumPeerOrder []string
)

func slskdAlbumKey(album, albumArtist string) string {
	album = normalizeSlskdText(album)
	if album == "" {
		return ""
	}
	return album + "|" + normalizeSlskdText(albumArtist)
}

func slskdLookupAlbumPeer(album, albumArtist string) *slskdAlbumPeer {
	key := slskdAlbumKey(album, albumArtist)
	if key == "" {
		return nil
	}
	slskdAlbumPeersMu.Lock()
	defer slskdAlbumPeersMu.Unlock()
	return slskdAlbumPeers[key]
}

func slskdStoreAlbumPeer(key string, entry *slskdAlbumPeer) {
	slskdAlbumPeersMu.Lock()
	defer slskdAlbumPeersMu.Unlock()
	if _, exists := slskdAlbumPeers[key]; !exists {
		slskdAlbumPeerOrder = append(slskdAlbumPeerOrder, key)
		if len(slskdAlbumPeerOrder) > slskdAlbumPeerCacheMax {
			delete(slskdAlbumPeers, slskdAlbumPeerOrder[0])
			slskdAlbumPeerOrder = slskdAlbumPeerOrder[1:]
		}
	}
	slskdAlbumPeers[key] = entry
}

func slskdForgetAlbumPeer(album, albumArtist, username string) {
	key := slskdAlbumKey(album, albumArtist)
	if key == "" {
		return
	}
	slskdAlbumPeersMu.Lock()
	defer slskdAlbumPeersMu.Unlock()
	if entry := slskdAlbumPeers[key]; entry != nil && strings.EqualFold(entry.Username, username) {
		delete(slskdAlbumPeers, key)
	}
}

func slskdRemoteDirName(filename string) string {
	if idx := strings.LastIndexAny(filename, "\\/"); idx > 0 {
		return filename[:idx]
	}
	return ""
}

type slskdBrowseDirectory struct {
	Name  string      `json:"name"`
	Files []SlskdFile `json:"files"`
}

type slskdBrowseResponse struct {
	Directories []slskdBrowseDirectory `json:"directories"`
}

// fetchPeerDirectoryFiles lists one remote folder via the peer's share browse.
// Browse entries carry bare file names (no path), so they are re-joined onto
// the directory with the Soulseek path separator.
func (s *SlskdDownloader) fetchPeerDirectoryFiles(username, dir string) ([]SlskdFile, error) {
	var browse slskdBrowseResponse
	if err := s.doJSON(http.MethodGet, "/users/"+url.PathEscape(username)+"/browse", nil, &browse); err != nil {
		return nil, err
	}
	want := strings.TrimRight(dir, "\\/")
	for _, d := range browse.Directories {
		if !strings.EqualFold(strings.TrimRight(d.Name, "\\/"), want) {
			continue
		}
		files := make([]SlskdFile, 0, len(d.Files))
		for _, f := range d.Files {
			if !strings.ContainsAny(f.Filename, "\\/") {
				f.Filename = dir + "\\" + f.Filename
			}
			files = append(files, f)
		}
		return files, nil
	}
	return nil, fmt.Errorf("folder %q not found in %s's share", dir, username)
}

// albumFolderCandidates ranks the cached folder's files against the current
// track. Files must contain every track-title token — the folder holds the
// whole album, and ranking alone could let a wrong sibling through.
func (s *SlskdDownloader) albumFolderCandidates(entry *slskdAlbumPeer, requestedLossless, preferLossy bool, duration int, trackName, artistName, albumName string) []slskdScoredCandidate {
	trackTokens := tokenizeSlskdText(trackName)
	if len(trackTokens) == 0 || len(entry.Files) == 0 {
		return nil
	}
	synth := SlskdSearchResponse{Username: entry.Username, HasFreeUploadSlot: true, Files: entry.Files}
	ranked, err := s.rankCandidates([]SlskdSearchResponse{synth}, requestedLossless, preferLossy, duration, trackName, artistName, albumName)
	if err != nil {
		return nil
	}
	matched := make([]slskdScoredCandidate, 0, 3)
	for _, c := range ranked {
		if !slskdContainsAllTokens(normalizeSlskdText(c.File.Filename), trackTokens) {
			continue
		}
		matched = append(matched, c)
		if len(matched) == 3 {
			break
		}
	}
	return matched
}

func (s *SlskdDownloader) rememberAlbumPeer(album, albumArtist, username, remoteFilename string) {
	key := slskdAlbumKey(album, albumArtist)
	dir := slskdRemoteDirName(remoteFilename)
	if key == "" || dir == "" {
		return
	}
	slskdAlbumPeersMu.Lock()
	existing := slskdAlbumPeers[key]
	slskdAlbumPeersMu.Unlock()
	if existing != nil && strings.EqualFold(existing.Username, username) && strings.EqualFold(existing.Directory, dir) {
		return
	}
	files, err := s.fetchPeerDirectoryFiles(username, dir)
	if err != nil {
		s.logEvent(SlskdLogEvent{Phase: "peer", Level: "warn", Username: username, Message: fmt.Sprintf("could not index %s's album folder for reuse (%v)", username, err)})
		return
	}
	audio := make([]SlskdFile, 0, len(files))
	for _, f := range files {
		ext := filepath.Ext(f.Filename)
		if ext == "" && f.Extension != "" {
			ext = "." + strings.TrimPrefix(f.Extension, ".")
		}
		if slskdIsAudioExtension(ext) {
			audio = append(audio, f)
		}
	}
	if len(audio) == 0 {
		return
	}
	slskdStoreAlbumPeer(key, &slskdAlbumPeer{Username: username, Directory: dir, Files: audio})
	s.logEvent(SlskdLogEvent{Phase: "peer", Level: "success", Username: username, Count: len(audio), Message: fmt.Sprintf("remembered %s's album folder (%d audio file(s)) — remaining tracks of this album will skip searching", username, len(audio))})
}

func (s *SlskdDownloader) enqueueAndWait(candidate *slskdScoredCandidate) (*SlskdTransfer, time.Time, error) {
	startedAt := time.Now()

	body := []map[string]interface{}{
		{
			"filename": candidate.File.Filename,
			"size":     candidate.File.Size,
		},
	}

	path := "/transfers/downloads/" + url.PathEscape(candidate.Response.Username)
	if err := s.doJSON(http.MethodPost, path, body, nil); err != nil {
		return nil, startedAt, err
	}

	username := candidate.Response.Username
	baseName := slskdRemoteBaseName(candidate.File.Filename)
	deadline := time.Now().Add(slskdTransferTimeout)
	var (
		transferID  string
		everSeen    bool
		lastState   string
		lastPercent float64
		// progressAt marks the last observed forward motion (state change or
		// percent advance); the stall guards measure against it.
		progressAt = time.Now()
		lastLogAt  time.Time
	)
	for time.Now().Before(deadline) {
		transfers, err := s.getTransfers()
		if err != nil {
			time.Sleep(slskdPollInterval)
			continue
		}

		transfer := s.findTransfer(username, candidate.File.Filename, candidate.File.Size, transfers)
		if transfer == nil {
			if everSeen {
				return nil, startedAt, fmt.Errorf("slskd transfer disappeared from the download queue")
			}
			if time.Since(startedAt) > slskdTransferAppearTimeout {
				return nil, startedAt, fmt.Errorf("transfer did not appear in slskd's download queue within %s", slskdTransferAppearTimeout)
			}
			time.Sleep(slskdPollInterval)
			continue
		}
		everSeen = true
		transferID = transfer.ID

		state := strings.TrimSpace(transfer.State)
		if state == "Completed, Succeeded" {
			return transfer, startedAt, nil
		}
		if strings.HasPrefix(state, "Completed") {
			if strings.TrimSpace(transfer.Exception) != "" {
				return nil, startedAt, fmt.Errorf("slskd transfer failed: %s (%s)", state, strings.TrimSpace(transfer.Exception))
			}
			return nil, startedAt, fmt.Errorf("slskd transfer failed: %s", state)
		}

		if state != lastState || transfer.PercentComplete > lastPercent+0.01 {
			progressAt = time.Now()
		}
		if state != lastState {
			s.logEvent(SlskdLogEvent{Phase: "transfer", Username: username, Filename: baseName, Message: fmt.Sprintf("transfer state: %s", state)})
			lastLogAt = time.Now()
		} else if state == "InProgress" && time.Since(lastLogAt) >= 5*time.Second {
			s.logEvent(SlskdLogEvent{Phase: "transfer", Username: username, Filename: baseName, Message: fmt.Sprintf("downloading… %.0f%%", transfer.PercentComplete)})
			lastLogAt = time.Now()
		}
		lastState = state
		if transfer.PercentComplete > lastPercent {
			lastPercent = transfer.PercentComplete
		}

		stallBudget := slskdTransferQueuedTimeout
		if state == "InProgress" {
			stallBudget = slskdTransferStallTimeout
		}
		if time.Since(progressAt) > stallBudget {
			s.cancelTransfer(username, transferID)
			return nil, startedAt, fmt.Errorf("transfer stalled in state %q for %s (%.0f%% complete) — cancelled", state, stallBudget, lastPercent)
		}

		time.Sleep(slskdPollInterval)
	}

	s.cancelTransfer(username, transferID)
	return nil, startedAt, fmt.Errorf("slskd transfer timed out after %s", slskdTransferTimeout)
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

	// Try peers in ranked order until one actually serves the file. The top
	// candidate is often a "ghost" indexer that lists files but rejects every
	// transfer ("File not shared"), or a peer that has gone offline. Without
	// this fallback a single bad source fails the whole download even when
	// other peers have the track.
	attemptDownload := func(cands []slskdScoredCandidate) (*slskdScoredCandidate, *SlskdTransfer, time.Time, error) {
		var lastErr error
		failedUsers := make(map[string]bool)
		attempts := 0
		for i := range cands {
			c := &cands[i]
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
				s.logEvent(SlskdLogEvent{Phase: "peer", Level: "success", Username: c.Response.Username, Filename: slskdRemoteBaseName(c.File.Filename), Message: fmt.Sprintf("downloading from %s", c.Response.Username)})
				return c, t, st, nil
			}

			lastErr = enqErr
			failedUsers[c.Response.Username] = true
			s.logEvent(SlskdLogEvent{Phase: "peer", Level: "warn", Username: c.Response.Username, Message: fmt.Sprintf("peer %s rejected/failed (%v) — trying next", c.Response.Username, enqErr)})
		}
		if lastErr == nil {
			lastErr = fmt.Errorf("slskd: no peer could serve the requested file")
		}
		return nil, nil, time.Time{}, lastErr
	}

	var candidate *slskdScoredCandidate
	var transfer *SlskdTransfer
	var startedAt time.Time

	// Album-folder fast path: a previous track of this album already came from
	// some peer's folder. Pull the sibling straight from that folder — skips
	// the search lottery entirely and keeps the whole album from one source.
	if entry := slskdLookupAlbumPeer(spotifyAlbumName, spotifyAlbumArtist); entry != nil {
		if cached := s.albumFolderCandidates(entry, requestedLossless, preferLossy, duration, spotifyTrackName, spotifyArtistName, spotifyAlbumName); len(cached) > 0 {
			s.logEvent(SlskdLogEvent{Phase: "peer", Username: entry.Username, Count: len(cached), Message: fmt.Sprintf("album folder cache: %d matching file(s) in %s's folder — skipping search", len(cached), entry.Username)})
			c, t, st, aErr := attemptDownload(cached)
			if aErr == nil {
				candidate, transfer, startedAt = c, t, st
			} else {
				s.logEvent(SlskdLogEvent{Phase: "peer", Level: "warn", Username: entry.Username, Message: fmt.Sprintf("album folder cache failed (%v) — falling back to search", aErr)})
				slskdForgetAlbumPeer(spotifyAlbumName, spotifyAlbumArtist, entry.Username)
			}
		}
	}

	if candidate == nil {
		// Search with a relaxation ladder: the precise query first, then
		// progressively looser fallbacks. A single over-specified token (often a
		// second artist) can make Soulseek's AND-match return 0 responses for a
		// well-seeded track; the looser tiers recover it without pulling in
		// unrelated files.
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

		c, t, st, aErr := attemptDownload(candidates)
		if aErr != nil {
			s.logEvent(SlskdLogEvent{Phase: "result", Level: "error", Message: fmt.Sprintf("all peer attempt(s) failed: %v", aErr)})
			return "", aErr
		}
		candidate, transfer, startedAt = c, t, st
	}

	// Remember the source folder so the album's remaining tracks skip the
	// search. Single-track downloads (spotifyTotalTracks <= 1) have no
	// siblings to benefit.
	if spotifyTotalTracks > 1 {
		s.rememberAlbumPeer(spotifyAlbumName, spotifyAlbumArtist, candidate.Response.Username, candidate.File.Filename)
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

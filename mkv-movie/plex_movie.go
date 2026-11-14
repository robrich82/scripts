package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
)

// ErrPlexMovieNotFound is returned by FindMovieByTMDBID when no matching movie
// exists in any Plex library section.
var ErrPlexMovieNotFound = errors.New("plex: movie not found")

// ErrPlexNoAudioStream is returned by SetMovieStreams when the movie has no
// audio stream in the requested language.
var ErrPlexNoAudioStream = errors.New("plex: no audio stream in that language")

// flexBool unmarshals JSON booleans (true/false) or integers (0/1) into a bool.
// Plex API versions differ: some return selected/forced as int, others as bool.
type flexBool bool

func (f *flexBool) UnmarshalJSON(data []byte) error {
	var b bool
	if err := json.Unmarshal(data, &b); err == nil {
		*f = flexBool(b)
		return nil
	}
	var i int
	if err := json.Unmarshal(data, &i); err == nil {
		*f = flexBool(i != 0)
		return nil
	}
	return fmt.Errorf("flexBool: cannot unmarshal %s", data)
}

// PlexMovieClient is the interface for Plex API calls needed by the movie
// rating pipeline.
type PlexMovieClient interface {
	// FindMovieByTMDBID searches all "movie" library sections for an item whose
	// Guid contains "tmdb://{tmdbID}". Returns the ratingKey on success, or
	// ErrPlexMovieNotFound if no match exists.
	FindMovieByTMDBID(tmdbID string) (string, error)
	// FindMovieByTitle searches all "movie" library sections for an item whose
	// title (normalized) matches title and whose year matches year.
	// Returns ErrPlexMovieNotFound if no match exists.
	FindMovieByTitle(title, year string) (string, error)
	// GetContentRating returns the current NZ content rating stored in Plex for
	// the item identified by ratingKey, or RatingNone if unset or unrecognised.
	GetContentRating(ratingKey string) (NZRating, error)
	// SetContentRating updates the contentRating field on a Plex movie item and
	// locks it to prevent automatic overwriting.
	SetContentRating(ratingKey string, rating NZRating) error
	// SetMovieStreams selects the audio stream matching the account's language
	// (ISO 639-1, e.g. "ja") and the first non-forced English subtitle stream on
	// the movie's first media Part. A single-account client uses langs.Default;
	// multiPlexMovieClient gives each account its own language. Returns whether
	// audio and/or subtitle were changed.
	SetMovieStreams(ratingKey string, langs AudioLangs) (audioSet, subtitleSet bool, err error)
	// RefreshLibraries triggers a metadata scan of all Plex movie library
	// sections so that newly processed files are picked up without manual
	// intervention.
	RefreshLibraries() error
}

// httpPlexMovieClient is the production PlexMovieClient that communicates with
// a real Plex Media Server over HTTP.
type httpPlexMovieClient struct {
	baseURL       string
	token         string
	httpClient    *http.Client
	movieKeyCache map[string]string          // tmdbID → Plex ratingKey
	sectionItems  map[string][]plexMovieItem // section key → its last listing
}

// plexMovieItem is a movie in a Plex section listing.
type plexMovieItem struct {
	RatingKey string          `json:"ratingKey"`
	Title     string          `json:"title"`
	Year      int             `json:"year"`
	Guid      json.RawMessage `json:"Guid"`
	GuidLower string          `json:"guid"`
}

// newPlexHTTPClient drops X-Plex-Token from a redirect that leaves the Plex
// server's host: Go only strips Authorization and Cookie on its own.
func newPlexHTTPClient() *http.Client {
	return &http.Client{
		Timeout: httpTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			if req.URL.Host != via[0].URL.Host {
				req.Header.Del("X-Plex-Token")
			}
			return nil
		},
	}
}

func newHTTPPlexMovieClient(baseURL, token string) *httpPlexMovieClient {
	return &httpPlexMovieClient{
		baseURL:       strings.TrimRight(baseURL, "/"),
		token:         token,
		httpClient:    newPlexHTTPClient(),
		movieKeyCache: make(map[string]string),
		sectionItems:  make(map[string][]plexMovieItem),
	}
}

// plexGet is plexRequest with GET.
func (c *httpPlexMovieClient) plexGet(path string) (*http.Response, error) {
	return c.plexRequest(http.MethodGet, path)
}

// plexRequest sends method to path (relative to baseURL) with an
// Accept: application/json header. The Plex token goes in a header, not the
// URL, so transport errors (which print the URL) can't leak it.
func (c *httpPlexMovieClient) plexRequest(method, path string) (*http.Response, error) {
	u := c.baseURL + path
	req, err := http.NewRequest(method, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Plex-Token", c.token)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	// Reject non-2xx so a 401/404/500 surfaces as an error instead of being
	// decoded into a zero-value struct that looks like an empty success.
	if resp.StatusCode/100 != 2 {
		resp.Body.Close()
		return nil, fmt.Errorf("plex: %s %s: unexpected status %s", method, path, resp.Status)
	}
	return resp, nil
}

// FindMovieByTMDBID implements PlexMovieClient.
func (c *httpPlexMovieClient) FindMovieByTMDBID(tmdbID string) (string, error) {
	if key, ok := c.movieKeyCache[tmdbID]; ok {
		return key, nil
	}
	wantGUID := "tmdb://" + tmdbID
	key, err := c.findMovie(func(item plexMovieItem) bool {
		return plexMovieItemHasTMDBID(item.Guid, item.GuidLower, wantGUID, tmdbID)
	})
	if err == nil {
		c.movieKeyCache[tmdbID] = key
	}
	return key, err
}

// findMovie returns the rating key of the first movie that match accepts, or
// ErrPlexMovieNotFound. It searches the section listings fetched earlier in
// this run, then fresh listings, as Plex may have added the movie since; a
// cached match in any section wins over a copy newly added to another.
func (c *httpPlexMovieClient) findMovie(match func(plexMovieItem) bool) (string, error) {
	sections, err := c.movieSectionKeys()
	if err != nil {
		return "", err
	}
	fetched := make(map[string]bool) // sections listed by this call
	for _, fresh := range []bool{false, true} {
		for _, section := range sections {
			if fresh && fetched[section] {
				continue
			}
			items, wasFetched, err := c.sectionListing(section, fresh)
			if err != nil {
				return "", err
			}
			fetched[section] = fetched[section] || wasFetched
			for _, item := range items {
				if match(item) {
					return item.RatingKey, nil
				}
			}
		}
	}
	return "", ErrPlexMovieNotFound
}

// movieSectionKeys returns the keys of Plex's movie library sections.
func (c *httpPlexMovieClient) movieSectionKeys() ([]string, error) {
	resp, err := c.plexGet("/library/sections")
	if err != nil {
		return nil, fmt.Errorf("plex: list sections: %w", err)
	}
	defer resp.Body.Close()

	var sectionsResp struct {
		MediaContainer struct {
			Directory []struct {
				Key  string `json:"key"`
				Type string `json:"type"`
			} `json:"Directory"`
		} `json:"MediaContainer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&sectionsResp); err != nil {
		return nil, fmt.Errorf("plex: decode sections: %w", err)
	}
	var keys []string
	for _, section := range sectionsResp.MediaContainer.Directory {
		if section.Type == "movie" {
			keys = append(keys, section.Key)
		}
	}
	return keys, nil
}

// sectionListing returns the movies in a Plex section: the listing fetched
// earlier in this run, unless fresh or there is none. fetched reports a
// request. Without includeGuids=1, Plex lists a modern-agent movie with only
// its plex:// guid, so a TMDB ID would never match.
func (c *httpPlexMovieClient) sectionListing(sectionKey string, fresh bool) (items []plexMovieItem, fetched bool, err error) {
	if items, ok := c.sectionItems[sectionKey]; ok && !fresh {
		return items, false, nil
	}
	resp, err := c.plexGet("/library/sections/" + sectionKey + "/all?type=1&includeGuids=1")
	if err != nil {
		return nil, false, fmt.Errorf("plex: list section %s: %w", sectionKey, err)
	}
	defer resp.Body.Close()

	var allResp struct {
		MediaContainer struct {
			Metadata []plexMovieItem `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&allResp); err != nil {
		return nil, false, fmt.Errorf("plex: decode section %s: %w", sectionKey, err)
	}
	c.sectionItems[sectionKey] = allResp.MediaContainer.Metadata
	return allResp.MediaContainer.Metadata, true, nil
}

// plexMovieItemHasTMDBID mirrors mkv-tvshow's plexItemHasTMDBID.
func plexMovieItemHasTMDBID(guidRaw json.RawMessage, guidLower, wantGUID, tmdbID string) bool {
	if len(guidRaw) > 0 && guidRaw[0] == '[' {
		var guids []struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(guidRaw, &guids) == nil {
			for _, g := range guids {
				if g.ID == wantGUID {
					return true
				}
			}
		}
		// A legacy-agent movie may come with an empty array; its guid field still counts.
		return plexMovieGuidMatchesTMDBID(guidLower, tmdbID)
	}
	if len(guidRaw) > 0 {
		var s string
		if json.Unmarshal(guidRaw, &s) == nil && plexMovieGuidMatchesTMDBID(s, tmdbID) {
			return true
		}
	}
	return plexMovieGuidMatchesTMDBID(guidLower, tmdbID)
}

func plexMovieGuidMatchesTMDBID(s, tmdbID string) bool {
	_, rest, ok := strings.Cut(s, "://"+tmdbID)
	return ok && (rest == "" || rest[0] < '0' || rest[0] > '9')
}

// FindMovieByTitle implements PlexMovieClient.
func (c *httpPlexMovieClient) FindMovieByTitle(title, year string) (string, error) {
	normTitle := normalizePlexTitle(title)
	wantYear := 0
	if year != "" {
		wantYear, _ = strconv.Atoi(year)
	}
	return c.findMovie(func(item plexMovieItem) bool {
		if normalizePlexTitle(item.Title) != normTitle {
			return false
		}
		return wantYear == 0 || abs(item.Year-wantYear) <= 1
	})
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// normalizePlexTitle returns a lowercase alphanumeric-only string for fuzzy title matching.
func normalizePlexTitle(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, c := range s {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			b.WriteRune(c)
		}
	}
	return b.String()
}

// GetContentRating implements PlexMovieClient.
// It returns the NZ content rating currently stored in Plex for ratingKey,
// stripping the "nz/" prefix before parsing. Returns RatingNone when unset
// or not a recognised NZ rating.
func (c *httpPlexMovieClient) GetContentRating(ratingKey string) (NZRating, error) {
	resp, err := c.plexGet("/library/metadata/" + ratingKey)
	if err != nil {
		return RatingNone, fmt.Errorf("plex: get metadata %s: %w", ratingKey, err)
	}
	defer resp.Body.Close()

	var metaResp struct {
		MediaContainer struct {
			Metadata []struct {
				ContentRating string `json:"contentRating"`
			} `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&metaResp); err != nil {
		return RatingNone, fmt.Errorf("plex: decode metadata %s: %w", ratingKey, err)
	}
	if len(metaResp.MediaContainer.Metadata) == 0 {
		return RatingNone, nil
	}
	raw := strings.TrimPrefix(metaResp.MediaContainer.Metadata[0].ContentRating, "nz/")
	rating, _ := ParseNZRating(raw)
	return rating, nil
}

// RefreshLibraries implements PlexMovieClient. It scans all "movie" library
// sections, triggering a metadata refresh so Plex picks up any new or changed
// files without requiring a manual library scan. It sends POST, the method
// the API defines; GET works today but is undocumented.
func (c *httpPlexMovieClient) RefreshLibraries() error {
	sections, err := c.movieSectionKeys()
	if err != nil {
		return err
	}
	for _, section := range sections {
		refreshResp, err := c.plexRequest(http.MethodPost, "/library/sections/"+section+"/refresh")
		if err != nil {
			return fmt.Errorf("plex: refresh section %s: %w", section, err)
		}
		refreshResp.Body.Close()
	}
	return nil
}

// SetMovieStreams implements PlexMovieClient. It fetches the movie's first
// media Part, selects the audio stream matching the language (e.g. "ja"), and
// selects the first non-forced English subtitle stream. Only calls the Plex API
// when the desired stream is not already selected. This client speaks for a
// single account, so it uses langs.Default and ignores per-alt overrides.
func (c *httpPlexMovieClient) SetMovieStreams(ratingKey string, langs AudioLangs) (audioSet, subtitleSet bool, err error) {
	audioLangTag := langs.Default
	resp, err := c.plexGet("/library/metadata/" + ratingKey)
	if err != nil {
		return false, false, fmt.Errorf("plex: get streams %s: %w", ratingKey, err)
	}
	defer resp.Body.Close()

	var meta struct {
		MediaContainer struct {
			Metadata []struct {
				Media []struct {
					Part []struct {
						ID     int `json:"id"`
						Stream []struct {
							ID           int      `json:"id"`
							StreamType   int      `json:"streamType"`
							Selected     flexBool `json:"selected"`
							Forced       flexBool `json:"forced"`
							LanguageTag  string   `json:"languageTag"`
							LanguageCode string   `json:"languageCode"`
						} `json:"Stream"`
					} `json:"Part"`
				} `json:"Media"`
			} `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return false, false, fmt.Errorf("plex: decode streams %s: %w", ratingKey, err)
	}
	if len(meta.MediaContainer.Metadata) == 0 ||
		len(meta.MediaContainer.Metadata[0].Media) == 0 ||
		len(meta.MediaContainer.Metadata[0].Media[0].Part) == 0 {
		return false, false, fmt.Errorf("plex: no media part for %s", ratingKey)
	}
	part := meta.MediaContainer.Metadata[0].Media[0].Part[0]

	// Select audio stream matching audioLangTag (skip if already selected).
	var audioID int
	audioOK := false
	for _, s := range part.Stream {
		if s.StreamType != 2 || !streamLangMatches(s.LanguageTag, s.LanguageCode, audioLangTag) {
			continue
		}
		if bool(s.Selected) {
			audioOK = true
			break
		}
		if audioID == 0 {
			audioID = s.ID
		}
	}
	if !audioOK {
		if audioID == 0 {
			return false, false, fmt.Errorf("%w: %s, key %s", ErrPlexNoAudioStream, audioLangTag, ratingKey)
		}
		if err := c.plexSetStream(part.ID, "audioStreamID", audioID); err != nil {
			return false, false, fmt.Errorf("plex: set audio stream: %w", err)
		}
		audioSet = true
	}

	// Select first non-forced English subtitle (skip if already selected).
	var subtitleID int
	subtitleOK := false
	for _, s := range part.Stream {
		if s.StreamType != 3 || !streamLangMatches(s.LanguageTag, s.LanguageCode, "en") || bool(s.Forced) {
			continue
		}
		if bool(s.Selected) {
			subtitleOK = true
			break
		}
		if subtitleID == 0 {
			subtitleID = s.ID
		}
	}
	if !subtitleOK && subtitleID != 0 {
		if err := c.plexSetStream(part.ID, "subtitleStreamID", subtitleID); err != nil {
			return audioSet, false, fmt.Errorf("plex: set subtitle stream: %w", err)
		}
		subtitleSet = true
	}

	return audioSet, subtitleSet, nil
}

// iso6392ToTag maps ISO 639-2 three-letter codes to ISO 639-1 two-letter BCP 47 tags.
var iso6392ToTag = map[string]string{
	"eng": "en", "jpn": "ja", "kor": "ko",
	"zho": "zh", "chi": "zh",
	"fre": "fr", "fra": "fr",
	"ger": "de", "deu": "de",
	"spa": "es", "ita": "it", "por": "pt",
	"rus": "ru", "ara": "ar",
}

// streamLangMatches reports whether a Plex stream matches the given ISO 639-1
// tag. It checks languageTag (BCP 47, possibly with a region suffix like "ja-JP")
// first, then falls back to languageCode (ISO 639-2) via iso6392ToTag.
func streamLangMatches(languageTag, languageCode, want string) bool {
	if languageTag != "" {
		return languageTag == want || strings.HasPrefix(languageTag, want+"-")
	}
	return iso6392ToTag[languageCode] == want
}

func (c *httpPlexMovieClient) plexSetStream(partID int, param string, streamID int) error {
	u := fmt.Sprintf("%s/library/parts/%d?%s=%d",
		c.baseURL, partID, param, streamID)
	req, err := http.NewRequest(http.MethodPut, u, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Plex-Token", c.token)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// multiAccountReporter is implemented by multiPlexMovieClient, the client that
// fans SetMovieStreams out to more than one Plex account. It lets the caller
// set and record streams per account and report any per-account failures.
type multiAccountReporter interface {
	appliedSignature(langs AudioLangs) string
	setStreamsPending(ratingKey string, langs AudioLangs, applied string) (audioSet, subtitleSet bool, record string, err error)
	altErrSuffix() string
}

// appliedLangSignature returns the value to cache as "already applied" for a
// movie: the language given to the primary account, plus one entry per
// configured alt account, e.g. "ja|alt=ja|alt2=en". Alt accounts are listed
// even when they simply inherit the default, so adding PLEX_ALT2_TOKEN
// invalidates the cache and the new account is actually written on the next
// run instead of being skipped because the language itself did not change.
func appliedLangSignature(client PlexMovieClient, langs AudioLangs) string {
	if r, ok := client.(multiAccountReporter); ok {
		return r.appliedSignature(langs)
	}
	return langs.Default
}

// Labels for the additional Plex accounts. Each is both the manual_data.json
// key prefix ("alt-language") and the name used when reporting errors.
const (
	altAccount  = "alt"
	alt2Account = "alt2"
)

// AudioLangs holds the preferred audio language for each Plex account. Default
// applies to the primary account and to any alt account with no override of its
// own; Alts is keyed by account label (altAccount, alt2Account).
type AudioLangs struct {
	Default string
	Alts    map[string]string
}

// forAccount returns the language to apply for the named alt account, falling
// back to Default when that account has no override.
func (a AudioLangs) forAccount(name string) string {
	if lang := a.Alts[name]; lang != "" {
		return lang
	}
	return a.Default
}

// appliedSignature implements multiAccountReporter. Every configured alt
// account is listed with the language it receives — which is Default unless it
// has an "alt-language" / "alt2-language" override of its own.
func (m *multiPlexMovieClient) appliedSignature(langs AudioLangs) string {
	var b strings.Builder
	b.WriteString(langs.Default)
	for _, a := range m.alts {
		b.WriteString("|")
		b.WriteString(a.name)
		b.WriteString("=")
		b.WriteString(langs.forAccount(a.name))
	}
	return b.String()
}

// namedPlexMovieClient pairs an additional Plex account's client with its label.
type namedPlexMovieClient struct {
	name   string
	client PlexMovieClient
}

// altPlexTokenEnv lists the optional additional Plex account tokens, in the
// order their labels are reported.
var altPlexTokenEnv = []struct{ name, env string }{
	{altAccount, "PLEX_ALT_TOKEN"},
	{alt2Account, "PLEX_ALT2_TOKEN"},
}

// altPlexMovieClients builds a client for each additional Plex account token
// present in the environment. Returns nil when none are set.
func altPlexMovieClients(plexURL string) []namedPlexMovieClient {
	var alts []namedPlexMovieClient
	for _, a := range altPlexTokenEnv {
		if token := os.Getenv(a.env); token != "" {
			alts = append(alts, namedPlexMovieClient{name: a.name, client: newHTTPPlexMovieClient(plexURL, token)})
		}
	}
	return alts
}

// multiPlexMovieClient dispatches SetMovieStreams to the primary and every alt
// account, routing all other calls to primary only. Used when PLEX_ALT_TOKEN
// and/or PLEX_ALT2_TOKEN are set so stream selection is applied for every Plex
// user.
type multiPlexMovieClient struct {
	primary PlexMovieClient
	alts    []namedPlexMovieClient
	altErrs []error // parallel to alts; nil entry means that account succeeded
}

// altErrSuffix formats the errors from the most recent SetMovieStreams call for
// display, e.g. "; alt: not in library". Returns "" when every alt account
// succeeded.
func (m *multiPlexMovieClient) altErrSuffix() string {
	var parts []string
	for i, err := range m.altErrs {
		if err != nil {
			parts = append(parts, m.alts[i].name+": "+plexMovieAltErrSummary(err))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "; " + strings.Join(parts, "; ")
}

// plexMovieAltErrSummary converts a Plex alt-token error to a short human-readable
// string, collapsing noisy HTTP 404 responses to "not in library".
func plexMovieAltErrSummary(err error) string {
	if strings.Contains(err.Error(), "status 404") {
		return "not in library"
	}
	return err.Error()
}

// newMultiPlexMovieClient returns a PlexMovieClient that calls SetMovieStreams
// on the primary and every alt. If there are no alts, primary is returned
// directly.
func newMultiPlexMovieClient(primary PlexMovieClient, alts ...namedPlexMovieClient) PlexMovieClient {
	if len(alts) == 0 {
		return primary
	}
	return &multiPlexMovieClient{primary: primary, alts: alts}
}

func (m *multiPlexMovieClient) FindMovieByTMDBID(tmdbID string) (string, error) {
	return m.primary.FindMovieByTMDBID(tmdbID)
}
func (m *multiPlexMovieClient) FindMovieByTitle(title, year string) (string, error) {
	return m.primary.FindMovieByTitle(title, year)
}
func (m *multiPlexMovieClient) GetContentRating(ratingKey string) (NZRating, error) {
	return m.primary.GetContentRating(ratingKey)
}
func (m *multiPlexMovieClient) SetContentRating(ratingKey string, rating NZRating) error {
	return m.primary.SetContentRating(ratingKey, rating)
}

// SetMovieStreams sets every account's streams: setStreamsPending with nothing
// applied yet.
func (m *multiPlexMovieClient) SetMovieStreams(ratingKey string, langs AudioLangs) (audioSet, subtitleSet bool, err error) {
	audioSet, subtitleSet, _, err = m.setStreamsPending(ratingKey, langs, "")
	return
}

// setStreamsPending implements multiAccountReporter. It sets the streams only
// for the accounts whose language in applied (a cached signature, or "") isn't
// the one langs gives them, so an account that already has its selection —
// possibly changed by hand in Plex since — is left alone. The primary goes
// first; only its failure is returned, and then nothing is recorded. Alt
// failures are kept for altErrSuffix. record is the signature to cache: applied
// plus each account that succeeded, without a failed alt, so the next run
// retries that alt alone. The flags report a change on any account.
func (m *multiPlexMovieClient) setStreamsPending(ratingKey string, langs AudioLangs, applied string) (audioSet, subtitleSet bool, record string, err error) {
	m.altErrs = make([]error, len(m.alts))
	done := parseLangSignature(applied)
	if lang, ok := done[""]; !ok || lang != langs.Default {
		if audioSet, subtitleSet, err = m.primary.SetMovieStreams(ratingKey, langs); err != nil {
			return false, false, "", err
		}
		done[""] = langs.Default
	}
	for i, a := range m.alts {
		lang := langs.forAccount(a.name)
		if got, ok := done[a.name]; ok && got == lang {
			continue
		}
		altAudio, altSubtitle, altErr := a.client.SetMovieStreams(ratingKey, AudioLangs{Default: lang})
		m.altErrs[i] = altErr
		if altErr != nil {
			delete(done, a.name)
			continue
		}
		done[a.name] = lang
		audioSet = audioSet || altAudio
		subtitleSet = subtitleSet || altSubtitle
	}
	return audioSet, subtitleSet, m.formatSignature(done), nil
}

// formatSignature writes an account → language map in appliedSignature's form,
// skipping alts it doesn't hold.
func (m *multiPlexMovieClient) formatSignature(done map[string]string) string {
	var b strings.Builder
	b.WriteString(done[""])
	for _, a := range m.alts {
		if lang, ok := done[a.name]; ok {
			b.WriteString("|" + a.name + "=" + lang)
		}
	}
	return b.String()
}

// parseLangSignature reads a cached signature ("ja|alt=ja|alt2=en") into an
// account → language map, with the primary account under "".
func parseLangSignature(sig string) map[string]string {
	done := map[string]string{}
	if sig == "" {
		return done
	}
	parts := strings.Split(sig, "|")
	done[""] = parts[0]
	for _, p := range parts[1:] {
		if name, lang, ok := strings.Cut(p, "="); ok {
			done[name] = lang
		}
	}
	return done
}
func (m *multiPlexMovieClient) RefreshLibraries() error { return m.primary.RefreshLibraries() }

// SetContentRating implements PlexMovieClient.
func (c *httpPlexMovieClient) SetContentRating(ratingKey string, rating NZRating) error {
	plexRating := "nz/" + string(rating)
	path := fmt.Sprintf("/library/metadata/%s?type=1&id=%s&contentRating.value=%s&contentRating.locked=1",
		ratingKey, ratingKey, url.QueryEscape(plexRating))

	req, err := http.NewRequest(http.MethodPut, c.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("plex: build put request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Plex-Token", c.token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("plex: set content rating: %w", err)
	}
	resp.Body.Close()

	if resp.StatusCode >= 300 {
		return fmt.Errorf("plex: set content rating got status %d", resp.StatusCode)
	}
	return nil
}

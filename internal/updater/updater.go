// Package updater handles self-update for the Windows build.
//
// Flow:
//  1. Check() hits https://api.github.com/repos/<owner>/<repo>/releases/latest,
//     compares the tag with the build-time Version, and returns an UpdateInfo.
//  2. Apply() downloads the new .exe next to the current binary (<exe>.new) and
//     hands off to the platform swap helper, which renames the running image
//     aside, drops the new bytes in place, and relaunches. Apply() returns; the
//     caller is responsible for quitting the Wails runtime so the swap can
//     complete.
//  3. The release notes are persisted to ~/.<app>/pending-release-notes.json
//     before the swap. On the next startup the new binary reads that file and
//     shows the notes once, then deletes it.
//
// This file is byte-identical across every app that embeds the updater. All
// per-app differences (owner/repo/asset/version/config dir) are injected via
// Config at construction time.
package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	pendingNotesFile = "pending-release-notes.json"

	attemptFile  = "update-attempt.json"
	maxAutoTries = 5
)

// UpdateInfo is the result of a Check(). Available=false means "no update";
// the other fields may still be populated (e.g. CurrentVersion for display).
type UpdateInfo struct {
	Available      bool   `json:"available"`
	CurrentVersion string `json:"currentVersion"`
	LatestVersion  string `json:"latestVersion"`
	ReleaseNotes   string `json:"releaseNotes"`
	DownloadURL    string `json:"downloadUrl"`
	PublishedAt    string `json:"publishedAt"`
}

// PendingNotes is the on-disk record of "show these release notes the next
// time the user runs version X".
type PendingNotes struct {
	Version string `json:"version"`
	Notes   string `json:"notes"`
}

// AttemptRecord is the on-disk loop guard for the silent startup path. It caps
// how many times we will auto-apply the SAME target version before giving up
// and falling back to the manual badge (see ShouldAutoApply).
type AttemptRecord struct {
	TargetVersion string    `json:"targetVersion"`
	FromVersion   string    `json:"fromVersion"`
	AttemptedAt   time.Time `json:"attemptedAt"`
	Count         int       `json:"count"`
}

// AutoUpdateResult is returned by the guarded startup path (App.AutoUpdate).
type AutoUpdateResult struct {
	Applying bool       `json:"applying"` // update applying; app will quit shortly
	Blocked  bool       `json:"blocked"`  // available but guard tripped → manual badge
	Info     UpdateInfo `json:"info"`
}

// Config carries every per-app difference. Two apps embedding this package
// differ ONLY in the Config they pass to New.
type Config struct {
	Owner, Repo, AssetName, CurrentVersion, ConfigDir string
}

type Updater struct {
	owner          string
	repo           string
	assetName      string
	currentVersion string
	configDir      string
	httpClient     *http.Client
}

// New builds an Updater from an app-specific Config.
func New(c Config) *Updater {
	return &Updater{
		owner:          c.Owner,
		repo:           c.Repo,
		assetName:      c.AssetName,
		currentVersion: c.CurrentVersion,
		configDir:      c.ConfigDir,
		httpClient:     &http.Client{Timeout: 15 * time.Second},
	}
}

func (u *Updater) CurrentVersion() string { return u.currentVersion }

type ghRelease struct {
	TagName     string    `json:"tag_name"`
	Body        string    `json:"body"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt string    `json:"published_at"`
	Assets      []ghAsset `json:"assets"`
}
type ghAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// Check asks GitHub for the latest release and decides whether an update is
// available. Dev builds (Version == "" or "dev") never advertise updates.
//
// It tries the REST API first (which gives release notes and the exact asset
// URL), but that endpoint is rate-limited to 60 req/hour per IP for
// unauthenticated callers — easily exhausted behind a shared/NAT office IP. So
// on any API failure (403 rate limit, network error) it falls back to the
// plain github.com /releases/latest redirect, which is NOT API-rate-limited.
// Set GITHUB_TOKEN / GH_TOKEN to raise the API limit to 5000 req/hour and keep
// release notes working.
func (u *Updater) Check(ctx context.Context) (UpdateInfo, error) {
	info := UpdateInfo{CurrentVersion: u.currentVersion}
	if u.currentVersion == "" || u.currentVersion == "dev" {
		return info, nil
	}

	// Preferred path: REST API (gives notes + exact asset URL).
	rel, apiErr := u.fetchLatestViaAPI(ctx)
	if apiErr == nil && rel.TagName != "" && !rel.Draft {
		info.LatestVersion = rel.TagName
		info.ReleaseNotes = rel.Body
		info.PublishedAt = rel.PublishedAt
		for _, a := range rel.Assets {
			if a.Name == u.assetName {
				info.DownloadURL = a.BrowserDownloadURL
				break
			}
		}
		info.Available = compareSemver(rel.TagName, u.currentVersion) > 0 && info.DownloadURL != ""
		return info, nil
	}
	// API returned 404 (no releases yet) with no error — nothing to update to.
	if apiErr == nil && rel.TagName == "" {
		return info, nil
	}

	// Fallback: resolve the latest tag from the github.com redirect (no API
	// rate limit). We lose release notes, but the update still works.
	tag, err := u.fetchLatestTagViaRedirect(ctx)
	if err != nil {
		if apiErr != nil {
			return info, apiErr // surface the more descriptive API error
		}
		return info, err
	}
	if tag == "" {
		return info, nil
	}
	info.LatestVersion = tag
	// Canonical asset URL for a specific tag; same file the API would point to.
	info.DownloadURL = fmt.Sprintf(
		"https://github.com/%s/%s/releases/download/%s/%s",
		u.owner, u.repo, tag, u.assetName,
	)
	info.Available = compareSemver(tag, u.currentVersion) > 0
	return info, nil
}

// fetchLatestViaAPI hits the GitHub REST API. It returns (zero release, nil)
// when there are no releases (404), and an error for any other non-200 status
// (including 403 rate limit) so the caller can fall back to the redirect.
func (u *Updater) fetchLatestViaAPI(ctx context.Context) (ghRelease, error) {
	var rel ghRelease
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", u.owner, u.repo)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return rel, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if tok := githubToken(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := u.httpClient.Do(req)
	if err != nil {
		return rel, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return rel, nil // no releases yet
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return rel, fmt.Errorf("github api %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return rel, err
	}
	return rel, nil
}

// fetchLatestTagViaRedirect resolves the latest release tag without the REST
// API. GitHub serves /releases/latest as a 302 to /releases/tag/<tag>; we read
// the tag out of the Location header without following the redirect. This path
// is handled by the regular github.com web server and is not subject to the
// 60 req/hour API rate limit.
func (u *Updater) fetchLatestTagViaRedirect(ctx context.Context) (string, error) {
	url := fmt.Sprintf("https://github.com/%s/%s/releases/latest", u.owner, u.repo)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", err
	}
	cli := &http.Client{
		Timeout: u.httpClient.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse // don't follow; we just want Location
		},
	}
	resp, err := cli.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	loc := resp.Header.Get("Location")
	if loc == "" {
		// A 200/404 with no redirect usually means there are no releases.
		if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNotFound {
			return "", nil
		}
		return "", fmt.Errorf("github redirect: unexpected status %d", resp.StatusCode)
	}
	idx := strings.LastIndex(loc, "/tag/")
	if idx == -1 {
		return "", nil
	}
	return loc[idx+len("/tag/"):], nil
}

// githubToken returns the first non-empty GitHub token from the environment,
// or "" if none is set. An authenticated request raises the API rate limit
// from 60 to 5000 req/hour.
func githubToken() string {
	for _, k := range []string{"GITHUB_TOKEN", "GH_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

// compareSemver returns 1 if a > b, -1 if a < b, 0 if equal. Strips a leading
// 'v' and compares dot-separated parts numerically (falls back to string
// compare for non-numeric segments like "rc1").
func compareSemver(a, b string) int {
	pa := strings.Split(strings.TrimPrefix(a, "v"), ".")
	pb := strings.Split(strings.TrimPrefix(b, "v"), ".")
	n := len(pa)
	if len(pb) > n {
		n = len(pb)
	}
	for i := 0; i < n; i++ {
		var xa, xb string
		if i < len(pa) {
			xa = pa[i]
		}
		if i < len(pb) {
			xb = pb[i]
		}
		na, ea := strconv.Atoi(xa)
		nb, eb := strconv.Atoi(xb)
		if ea == nil && eb == nil {
			if na != nb {
				if na > nb {
					return 1
				}
				return -1
			}
			continue
		}
		if xa != xb {
			if xa > xb {
				return 1
			}
			return -1
		}
	}
	return 0
}

// Apply downloads the asset, stashes the release notes for the next launch,
// and hands off to the platform-specific swap helper (see apply_windows.go /
// apply_other.go). The caller must quit the process shortly after this returns
// so the helper can replace the binary.
func (u *Updater) Apply(ctx context.Context, info UpdateInfo) error {
	if !info.Available || info.DownloadURL == "" {
		return errors.New("no update available")
	}
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate exe: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exePath); err == nil {
		exePath = resolved
	}
	newPath := exePath + ".new"

	if err := u.download(ctx, info.DownloadURL, newPath); err != nil {
		return fmt.Errorf("download: %w", err)
	}

	// Stash notes for the post-update launch. Non-fatal — proceed even if the
	// write fails; the worst case is the user doesn't get a notes popup.
	_ = u.SavePendingNotes(PendingNotes{Version: info.LatestVersion, Notes: info.ReleaseNotes})

	return applyPlatform(exePath, newPath)
}

func (u *Updater) download(ctx context.Context, url, dst string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	// Big download — don't reuse the short-timeout client.
	cli := &http.Client{Timeout: 10 * time.Minute}
	resp, err := cli.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download status %d", resp.StatusCode)
	}
	tmp := dst + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	// Replace any stale .new from a previous attempt.
	_ = os.Remove(dst)
	return os.Rename(tmp, dst)
}

func (u *Updater) pendingPath() string {
	return filepath.Join(u.configDir, pendingNotesFile)
}

func (u *Updater) SavePendingNotes(p PendingNotes) error {
	if u.configDir == "" {
		return errors.New("no config dir")
	}
	if err := os.MkdirAll(u.configDir, 0755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(u.pendingPath(), b, 0644)
}

// LoadPendingNotes returns (notes, true, nil) iff a notes file exists for the
// CURRENT binary version. Notes left over for a different version are ignored
// (the update either didn't apply yet, or already shipped).
func (u *Updater) LoadPendingNotes() (PendingNotes, bool, error) {
	b, err := os.ReadFile(u.pendingPath())
	if errors.Is(err, os.ErrNotExist) {
		return PendingNotes{}, false, nil
	}
	if err != nil {
		return PendingNotes{}, false, err
	}
	var p PendingNotes
	if err := json.Unmarshal(b, &p); err != nil {
		return PendingNotes{}, false, err
	}
	if p.Version != u.currentVersion {
		return PendingNotes{}, false, nil
	}
	return p, true, nil
}

func (u *Updater) ClearPendingNotes() error {
	err := os.Remove(u.pendingPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// --- Loop guard for the silent startup path -----------------------------

func (u *Updater) attemptPath() string { return filepath.Join(u.configDir, attemptFile) }

func (u *Updater) loadAttempt() AttemptRecord {
	var r AttemptRecord
	b, err := os.ReadFile(u.attemptPath())
	if err != nil {
		return AttemptRecord{}
	}
	if json.Unmarshal(b, &r) != nil {
		return AttemptRecord{}
	}
	return r
}

func (u *Updater) saveAttempt(r AttemptRecord) {
	if u.configDir == "" {
		return
	}
	_ = os.MkdirAll(u.configDir, 0755)
	if b, err := json.MarshalIndent(r, "", "  "); err == nil {
		_ = os.WriteFile(u.attemptPath(), b, 0644)
	}
}

// ShouldAutoApply gates the SILENT startup path only. It records the attempt
// when it green-lights, so a crash mid-apply still counts toward the cap.
func (u *Updater) ShouldAutoApply(info UpdateInfo) bool {
	if !info.Available {
		return false
	}
	if u.currentVersion == "" || u.currentVersion == "dev" {
		return false
	}
	r := u.loadAttempt()
	// If we've reached/passed the last target, the update took → reset.
	if r.TargetVersion != "" && compareSemver(u.currentVersion, r.TargetVersion) >= 0 {
		r = AttemptRecord{}
	}
	sameTarget := r.TargetVersion == info.LatestVersion
	if sameTarget && r.Count >= maxAutoTries {
		return false // 5 non-converging tries exhausted → STOP (show manual badge)
	}
	next := AttemptRecord{TargetVersion: info.LatestVersion, FromVersion: u.currentVersion, AttemptedAt: time.Now().UTC(), Count: 1}
	if sameTarget {
		next.Count = r.Count + 1
	}
	u.saveAttempt(next)
	return true
}

// CleanupLeftovers removes stray files a previous swap may have left next to
// the binary (e.g. the renamed-aside old image). Safe to call on every start.
func (u *Updater) CleanupLeftovers(exePath string) {
	for _, suf := range []string{".old", ".new", ".new.part", "~"} {
		_ = os.Remove(exePath + suf)
	}
}

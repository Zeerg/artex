package selfupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Repo It is the release source. Hardcoded instead of as a configuration item: A configurable update source gives any person who can change the configuration a free pass.
// Remote code execution channel, this port is not allowed for a penetration testing platform.
const Repo = "Zeerg/artex"

// GitHubTokenEnv Only read by backend processes; not passed through frontend or configuration API.
const GitHubTokenEnv = "ARTEX_UPDATE_GITHUB_TOKEN"

// latestURL It is from GitHub's"Latest Official Version"Interface. It automatically skips prerelease and draft.
const latestURL = "https://api.github.com/repos/" + Repo + "/releases/latest"

// allowedHosts Restrict the domains accessible in the upgrade chain. Pair this with the following checkRedirect,
// Any hop redirected to a host outside the whitelist will fail directly.——This prevents DNS pollution / man-in-the-middle
// The first gate when replacing binary files, the second is SHA256SUMS comparison.
var allowedHosts = map[string]bool{
	"api.github.com": true,
	"github.com": true,
	"objects.githubusercontent.com": true, // release Actual landing object storage for assets
	"release-assets.githubusercontent.com": true,
	"raw.githubusercontent.com": true,
}

// Release Fields we care about from GitHub Release.
type Release struct {
	TagName string `json:"tag_name"`
	Name string `json:"name"`
	Body string `json:"body"`
	Draft bool `json:"draft"`
	Prerelease bool `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
	HTMLURL string `json:"html_url"`
	Assets []Asset `json:"assets"`
}

// Asset It's a file hanging on a Release.
type Asset struct {
	Name string `json:"name"`
	URL string `json:"browser_download_url"`
	APIURL string `json:"url"`
	Size int64 `json:"size"`
}

// Private repository attachments need to be downloaded with authentication using the Release Asset API.
func (a Asset) downloadURL() string {
	if a.APIURL!= "" {
		return a.APIURL
	}
	return a.URL
}

// setUpdateAuth Send token only to fixed release repository HTTPS API. Attachments redirected to
// GitHub Use signed URLs for object storage and do not forward the Authorization header.
func setUpdateAuth(req *http.Request) {
	req.Header.Del("Authorization")
	u:= req.URL
	if u.Scheme!= "https" ||!strings.EqualFold(u.Hostname(), "api.github.com") ||
		(u.Port()!= "" && u.Port()!= "443") || u.User!= nil ||
		!strings.HasPrefix(strings.ToLower(u.EscapedPath()), strings.ToLower("/repos/"+Repo+"/releases/")) {
		return
	}
	if token:= strings.TrimSpace(os.Getenv(GitHubTokenEnv)); token!= "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}

// NewClient Construct an HTTP client that only recognizes the GitHub domain. If proxy is empty, connect directly.
//
// Intentionally not reusing the default Transport: upgrade chain must force TLS and certificate verification, cannot be bypassed elsewhere
// 'Settings like InsecureSkipVerify affect',
func NewClient(proxy string) *http.Client {
	tr:= &http.Transport{
		ForceAttemptHTTP2: true,
		TLSHandshakeTimeout: 15 * time.Second,
	}
	if p:= strings.TrimSpace(proxy); p!= "" {
		if pu, err:= url.Parse(p); err == nil {
			tr.Proxy = http.ProxyURL(pu)
		}
	}
	return &http.Client{
		Transport: tr,
		Timeout: 30 * time.Minute, // Download entire package, cannot be stuck by request-level timeout
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("Too many redirects")
			}
			if err:= checkURL(req.URL); err!= nil {
				return err
			}
			setUpdateAuth(req)
			return nil
		},
	}
}

// checkURL Force HTTPS + Domain whitelist.
func checkURL(u *url.URL) error {
	if u.Scheme!= "https" {
		return fmt.Errorf("Refuse non-HTTPS addresses.: %s", u.Scheme+"://"+u.Host)
	}
	if!allowedHosts[strings.ToLower(u.Hostname())] {
		return fmt.Errorf("Refuse non-GitHub domain: %s", u.Hostname())
	}
	return nil
}

// FetchLatest Query the latest formal version.
func FetchLatest(ctx context.Context, c *http.Client) (*Release, error) {
	req, err:= http.NewRequestWithContext(ctx, http.MethodGet, latestURL, nil)
	if err!= nil {
		return nil, err
	}
	if err:= checkURL(req.URL); err!= nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "artex-selfupdate")
	setUpdateAuth(req)

	resp, err:= c.Do(req)
	if err!= nil {
		return nil, fmt.Errorf("Failed to access GitHub (can configure global proxy in system settings).: %w", err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, fmt.Errorf("GitHub Authentication failed, please check the backend environment variables %s", GitHubTokenEnv)
	case resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("GitHub access denied: verify the token has Contents read permission and is within API limits")
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("GitHub Rate Limit reached, please try again later")
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("Repository %s Unaccessible or not yet published; private repositories should be set up on the backend. %s ((requiring Contents read permission).", Repo, GitHubTokenEnv)
	case resp.StatusCode!= http.StatusOK:
		return nil, fmt.Errorf("GitHub Support %d", resp.StatusCode)
	}

	var rel Release
	if err:= json.NewDecoder(resp.Body).Decode(&rel); err!= nil {
		return nil, fmt.Errorf("Failed to parse Release: %w", err)
	}
	if strings.TrimSpace(rel.TagName) == "" {
		return nil, fmt.Errorf("Release Missing tag")
	}
	return &rel, nil
}

// AssetName Return the release package name corresponding to the current platform, similar to build.sh The package_binary remains consistent:
// artex-<Role>-<os>-<arch>.zip (Version number without v prefix).
func AssetName(tag, goos, goarch string) string {
	return fmt.Sprintf("artex-%s-%s-%s.zip", strings.TrimPrefix(tag, "v"), goos, goarch)
}

// FindAsset Find assets by name in a Release.
func (r *Release) FindAsset(name string) (Asset, bool) {
	for _, a:= range r.Assets {
		if strings.EqualFold(a.Name, name) {
			return a, true
		}
	}
	return Asset{}, false
}

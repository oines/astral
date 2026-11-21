// Package releasefeed resolves immutable, checksum-verified official releases.
package releasefeed

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/oines/astral/internal/githubrelease"
)

var versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

type Binary struct {
	Version     string  `json:"version"`
	Name        string  `json:"name"`
	SHA256      string  `json:"sha256"`
	GitHubProxy *string `json:"github_proxy,omitempty"`
}

func (b Binary) ProxyOverride() githubrelease.ProxyOverride {
	if b.GitHubProxy == nil {
		return githubrelease.ProxyOverride{}
	}
	return githubrelease.ProxyOverride{Set: true, Prefix: *b.GitHubProxy}
}

func Compare(a, b string) (int, error) {
	a, b = strings.TrimPrefix(a, "v"), strings.TrimPrefix(b, "v")
	if !versionPattern.MatchString(a) || !versionPattern.MatchString(b) {
		return 0, errors.New("release version must have the form major.minor.patch")
	}
	x, y := strings.Split(a, "."), strings.Split(b, ".")
	for i := range x {
		xv, e := strconv.ParseUint(x[i], 10, 64)
		if e != nil {
			return 0, e
		}
		yv, e := strconv.ParseUint(y[i], 10, 64)
		if e != nil {
			return 0, e
		}
		if xv < yv {
			return -1, nil
		}
		if xv > yv {
			return 1, nil
		}
	}
	return 0, nil
}

func AssetName(platform, arch string) (string, error) {
	if (platform != "darwin" && platform != "windows" && platform != "linux") || (arch != "arm64" && arch != "amd64") {
		return "", errors.New("unsupported Daemon platform or architecture")
	}
	name := "astral-" + platform + "-" + arch
	if platform == "windows" {
		name += ".exe"
	}
	return name, nil
}

func DaemonAssetName(platform, arch string) (string, error) {
	if (platform != "darwin" && platform != "windows" && platform != "linux") || (arch != "arm64" && arch != "amd64") {
		return "", errors.New("unsupported Daemon platform or architecture")
	}
	name := "astral-daemon-" + platform + "-" + arch
	if platform == "windows" {
		name += ".exe"
	}
	return name, nil
}

func clientWithProxy(override githubrelease.ProxyOverride) *http.Client {
	return &http.Client{Timeout: 5 * time.Minute, CheckRedirect: githubrelease.CheckRedirectWithProxy(override)}
}

func getWithProxy(ctx context.Context, c *http.Client, url string, override githubrelease.ProxyOverride) (io.ReadCloser, error) {
	address, err := githubrelease.AddressWithProxy(url, override)
	if err != nil {
		return nil, err
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	r.Header.Set("User-Agent", "Astral-Updater")
	response, err := c.Do(r)
	if err != nil {
		return nil, githubrelease.RequestError(err, url)
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, fmt.Errorf("official release unavailable (HTTP %d)", response.StatusCode)
	}
	return response.Body, nil
}

func base(version string) string {
	return "https://github.com/oines/astral/releases/download/v" + version
}

// LatestWithProxy pins the release tag and checksum. Legacy Daemons require
// the unified executable, including when the release also has standalone assets.
func LatestWithProxy(ctx context.Context, platform, arch string, override githubrelease.ProxyOverride, standalone bool) (Binary, error) {
	c := clientWithProxy(override)
	defer c.CloseIdleConnections()
	return latestWithProxy(ctx, platform, arch, c, override, standalone)
}

func latestWithProxy(ctx context.Context, platform, arch string, c *http.Client, override githubrelease.ProxyOverride, standalone bool) (Binary, error) {
	daemonName, err := DaemonAssetName(platform, arch)
	if err != nil {
		return Binary{}, err
	}
	fullName, err := AssetName(platform, arch)
	if err != nil {
		return Binary{}, err
	}
	body, err := getWithProxy(ctx, c, "https://api.github.com/repos/oines/astral/releases/latest", override)
	if err != nil {
		return Binary{}, err
	}
	var release struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	err = json.NewDecoder(io.LimitReader(body, 1<<20)).Decode(&release)
	body.Close()
	version := strings.TrimPrefix(release.Tag, "v")
	if err != nil || release.Draft || release.Prerelease || !versionPattern.MatchString(version) || release.Tag != "v"+version {
		return Binary{}, errors.New("official latest release has an invalid version")
	}
	body, err = getWithProxy(ctx, c, base(version)+"/checksums.txt", override)
	if err != nil {
		return Binary{}, err
	}
	raw, err := io.ReadAll(io.LimitReader(body, 65537))
	body.Close()
	if err != nil {
		return Binary{}, err
	}
	if len(raw) > 65536 {
		return Binary{}, errors.New("release checksum list is too large")
	}
	var daemonSHA, fullSHA string
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		target := strings.TrimPrefix(fields[1], "*")
		switch target {
		case daemonName:
			if daemonSHA != "" {
				return Binary{}, errors.New("duplicate release checksum")
			}
			daemonSHA = fields[0]
		case fullName:
			if fullSHA != "" {
				return Binary{}, errors.New("duplicate release checksum")
			}
			fullSHA = fields[0]
		}
	}
	value := Binary{Version: version, Name: daemonName, SHA256: daemonSHA}
	if !standalone || value.SHA256 == "" {
		value.Name, value.SHA256 = fullName, fullSHA
	}
	if err = value.Validate(platform, arch); err != nil {
		return Binary{}, err
	}
	return value, nil
}

// Verify rechecks a staged descriptor against its immutable official release.
// override must come from trusted configuration or an authenticated Server.
// Neither the bytes nor the verification source may be chosen by a local handoff.
func (b Binary) Verify(ctx context.Context, override githubrelease.ProxyOverride) error {
	c := clientWithProxy(override)
	defer c.CloseIdleConnections()
	return b.verifyWithProxy(ctx, c, override)
}

func (b Binary) verifyWithProxy(ctx context.Context, c *http.Client, override githubrelease.ProxyOverride) error {
	if !versionPattern.MatchString(b.Version) || strings.ContainsAny(b.Name, "/\\") {
		return errors.New("invalid release descriptor")
	}
	body, err := getWithProxy(ctx, c, base(b.Version)+"/checksums.txt", override)
	if err != nil {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(body, 65537))
	body.Close()
	if err != nil {
		return err
	}
	if len(raw) > 65536 {
		return errors.New("release checksum list is too large")
	}
	matches := 0
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == b.Name {
			matches++
			if fields[0] != b.SHA256 {
				return errors.New("staged descriptor does not match the official release")
			}
		}
	}
	if matches != 1 {
		return errors.New("official release has no unique matching executable")
	}
	return nil
}

func (b Binary) Validate(platform, arch string) error {
	fullName, err := AssetName(platform, arch)
	if err != nil {
		return err
	}
	daemonName, err := DaemonAssetName(platform, arch)
	if err != nil {
		return err
	}
	if b.GitHubProxy != nil {
		if _, err := githubrelease.NormalizeProxy(*b.GitHubProxy); err != nil {
			return err
		}
	}
	raw, err := hex.DecodeString(b.SHA256)
	if !versionPattern.MatchString(b.Version) || (b.Name != daemonName && b.Name != fullName) || err != nil || len(raw) != sha256.Size || strings.ToLower(b.SHA256) != b.SHA256 {
		return errors.New("invalid official release descriptor")
	}
	return nil
}

func (b Binary) Download(ctx context.Context, target string) error {
	return b.DownloadWithProxy(ctx, target, b.ProxyOverride())
}

func (b Binary) DownloadWithProxy(ctx context.Context, target string, override githubrelease.ProxyOverride) error {
	c := clientWithProxy(override)
	defer c.CloseIdleConnections()
	return b.downloadWithProxy(ctx, target, c, override)
}

func (b Binary) downloadWithProxy(ctx context.Context, target string, c *http.Client, override githubrelease.ProxyOverride) error {
	// The URL is derived only from a validated release tag and asset name.
	if !versionPattern.MatchString(b.Version) || strings.ContainsAny(b.Name, "/\\") {
		return errors.New("invalid release download")
	}
	body, err := getWithProxy(ctx, c, base(b.Version)+"/"+b.Name, override)
	if err != nil {
		return err
	}
	defer body.Close()
	file, err := os.CreateTemp(filepath.Dir(target), ".astral-release-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	hash := sha256.New()
	if err = file.Chmod(0700); err == nil {
		_, err = io.Copy(io.MultiWriter(file, hash), io.LimitReader(body, (256<<20)+1))
	}
	info, statErr := file.Stat()
	err = errors.Join(err, statErr, file.Sync(), file.Close())
	if err != nil {
		return err
	}
	if info.Size() > 256<<20 || hex.EncodeToString(hash.Sum(nil)) != b.SHA256 {
		return errors.New("release executable checksum mismatch")
	}
	return os.Rename(file.Name(), target)
}

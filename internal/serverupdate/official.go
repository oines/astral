package serverupdate

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
	"strings"
	"time"

	"github.com/oines/astral/internal/releasefeed"
)

type releaseTarget struct {
	Version      string
	InstallerSHA string
}

func officialClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) >= 8 || r.URL.Scheme != "https" || r.URL.User != nil {
			return errors.New("untrusted official update redirect")
		}
		switch r.URL.Hostname() {
		case "github.com", "api.github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com":
			return nil
		}
		return errors.New("unexpected official update host")
	}}
}
func officialBytes(ctx context.Context, client *http.Client, address string, limit int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "Astral-Server-Updater")
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("official Server release unavailable (HTTP %d)", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, errors.New("official update metadata exceeds its size limit")
	}
	return raw, nil
}
func releaseBase(version string) (string, error) {
	if _, err := releasefeed.Compare(version, version); err != nil || strings.HasPrefix(version, "v") {
		return "", errors.New("invalid official Server release version")
	}
	return "https://github.com/oines/astral/releases/download/v" + version, nil
}
func checksum(raw []byte, name string) (string, error) {
	value := ""
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[1] != name {
			continue
		}
		decoded, e := hex.DecodeString(fields[0])
		if value != "" || e != nil || len(decoded) != sha256.Size || strings.ToLower(fields[0]) != fields[0] {
			return "", errors.New("invalid or duplicate official installer checksum")
		}
		value = fields[0]
	}
	if value == "" {
		return "", errors.New("official release has no installer checksum")
	}
	return value, nil
}
func officialLatest(ctx context.Context) (releaseTarget, error) {
	client := officialClient()
	defer client.CloseIdleConnections()
	return resolveLatest(ctx, client)
}
func resolveLatest(ctx context.Context, client *http.Client) (releaseTarget, error) {
	raw, err := officialBytes(ctx, client, "https://api.github.com/repos/oines/astral/releases/latest", 1<<20)
	if err != nil {
		return releaseTarget{}, err
	}
	var release struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if json.Unmarshal(raw, &release) != nil || release.Draft || release.Prerelease || !strings.HasPrefix(release.Tag, "v") {
		return releaseTarget{}, errors.New("official Latest is not a stable published release")
	}
	version := strings.TrimPrefix(release.Tag, "v")
	base, err := releaseBase(version)
	if err != nil {
		return releaseTarget{}, err
	}
	sums, err := officialBytes(ctx, client, base+"/SHA256SUMS", 1<<20)
	if err != nil {
		return releaseTarget{}, err
	}
	installerSHA, err := checksum(sums, "install-server.sh")
	if err != nil {
		return releaseTarget{}, err
	}
	manifestSHA, err := checksum(sums, "release-manifest.json")
	if err != nil {
		return releaseTarget{}, err
	}
	raw, err = officialBytes(ctx, client, base+"/release-manifest.json", 1<<20)
	if err != nil {
		return releaseTarget{}, err
	}
	digest := sha256.Sum256(raw)
	if hex.EncodeToString(digest[:]) != manifestSHA {
		return releaseTarget{}, errors.New("official manifest checksum mismatch")
	}
	var manifest struct {
		Schema  int    `json:"schema_version"`
		Purpose string `json:"purpose"`
		Ready   bool   `json:"public_release_ready"`
		Version string `json:"version"`
		Tag     string `json:"tag"`
		Assets  []struct {
			Name string `json:"name"`
			SHA  string `json:"sha256"`
			Size int64  `json:"size"`
		} `json:"assets"`
	}
	if json.Unmarshal(raw, &manifest) != nil || manifest.Schema != 1 || manifest.Purpose != "release" || !manifest.Ready || manifest.Version != version || manifest.Tag != release.Tag {
		return releaseTarget{}, errors.New("official manifest is not a matching ready public release")
	}
	matches := 0
	for _, asset := range manifest.Assets {
		if asset.Name == "install-server.sh" {
			matches++
			if asset.SHA != installerSHA || asset.Size <= 0 || asset.Size > 2<<20 {
				return releaseTarget{}, errors.New("official installer inventory mismatch")
			}
		}
	}
	if matches != 1 {
		return releaseTarget{}, errors.New("official release must contain exactly one installer")
	}
	return releaseTarget{Version: version, InstallerSHA: installerSHA}, nil
}
func fetchInstaller(ctx context.Context, target releaseTarget, path string) error {
	client := officialClient()
	defer client.CloseIdleConnections()
	return fetchInstallerWithClient(ctx, target, path, client)
}
func fetchInstallerWithClient(ctx context.Context, target releaseTarget, path string, client *http.Client) error {
	base, err := releaseBase(target.Version)
	if err != nil {
		return err
	}
	// Recheck only the immutable selected tag, never Latest during the operation.
	sums, err := officialBytes(ctx, client, base+"/SHA256SUMS", 1<<20)
	if err != nil {
		return err
	}
	expected, err := checksum(sums, "install-server.sh")
	if err != nil {
		return err
	}
	if expected != target.InstallerSHA {
		return errors.New("staged Server installer does not match its official release")
	}
	raw, err := officialBytes(ctx, client, base+"/install-server.sh", 2<<20)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(raw)
	if hex.EncodeToString(digest[:]) != expected {
		return errors.New("official Server installer checksum mismatch")
	}
	return os.WriteFile(path, raw, 0600)
}

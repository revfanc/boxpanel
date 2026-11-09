// Package singbox installs and inspects the managed sing-box binary and services.
package singbox

import (
	"fmt"
	"regexp"
	"strings"
)

// PinnedVersion is the latest stable release older than seven days, verified
// against https://api.github.com/repos/SagerNet/sing-box/releases/latest.
const PinnedVersion = "1.14.4"
const PinnedPublishedAt = "2026-10-24T01:04:25Z"

const releaseBase = "https://github.com/SagerNet/sing-box/releases/download/"
const releaseAPI = "https://api.github.com/repos/SagerNet/sing-box/releases/tags/"

var stableVersion = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

func NormalizeVersion(version string) (string, error) {
	version = strings.TrimPrefix(version, "v")
	if len(version) > 40 || !stableVersion.MatchString(version) {
		return "", fmt.Errorf("invalid sing-box version: use a stable version such as %s", PinnedVersion)
	}
	return version, nil
}

func ArchiveName(version, platform, arch string) (string, error) {
	version, err := NormalizeVersion(version)
	if err != nil {
		return "", err
	}
	if platform != "linux" && platform != "darwin" {
		return "", fmt.Errorf("unsupported sing-box OS: use Linux or macOS")
	}
	switch arch {
	case "amd64", "x86_64":
		arch = "amd64"
	case "arm64", "aarch64":
		arch = "arm64"
	default:
		return "", fmt.Errorf("unsupported sing-box architecture: use amd64 or arm64")
	}
	return fmt.Sprintf("sing-box-%s-%s-%s.tar.gz", version, platform, arch), nil
}

func DownloadURL(version, platform, arch string) (string, error) {
	name, err := ArchiveName(version, platform, arch)
	if err != nil {
		return "", err
	}
	return releaseBase + "v" + strings.TrimPrefix(version, "v") + "/" + name, nil
}

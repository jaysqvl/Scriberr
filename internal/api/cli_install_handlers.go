package api

import (
	"net/http"
	"os"
	"path/filepath"

	"github.com/gin-gonic/gin"
)

const ArchAMD64 = "amd64"

// DownloadCLIBinary serves the requested CLI binary
// GET /api/cli/download
func (h *Handler) DownloadCLIBinary(c *gin.Context) {
	osName := c.Query("os")
	arch := c.Query("arch")

	if osName == "" || arch == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "os and arch query parameters are required"})
		return
	}

	// Map to filename
	// Supported: linux/amd64, darwin/amd64, darwin/arm64, windows/amd64
	var filename string
	switch osName {
	case "linux":
		if arch == ArchAMD64 {
			filename = "scriberr-linux-amd64"
		}
	case "darwin":
		if arch == ArchAMD64 {
			filename = "scriberr-darwin-amd64"
		} else if arch == "arm64" {
			filename = "scriberr-darwin-arm64"
		}
	case "windows":
		if arch == ArchAMD64 {
			filename = "scriberr-windows-amd64.exe"
		}
	}

	if filename == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Unsupported OS or architecture"})
		return
	}

	// Path to binaries
	// In Docker: /app/bin/cli
	// Local dev: ./bin/cli
	baseDir := "bin/cli"
	if _, err := os.Stat("/app/bin/cli"); err == nil {
		baseDir = "/app/bin/cli"
	}

	filePath := filepath.Join(baseDir, filename)
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Binary not found"})
		return
	}

	c.FileAttachment(filePath, filename)
}

const installScript = `#!/bin/bash

set -euo pipefail

SERVER_URL="${1:-${SCRIBERR_SERVER_URL:-http://localhost:8080}}"
INSTALL_DIR="/usr/local/bin"
BINARY_NAME="scriberr"

case "$SERVER_URL" in
    http://*|https://*) ;;
    *)
        echo "Server URL must begin with http:// or https://"
        exit 1
        ;;
esac

TMP_BINARY="$(mktemp "${TMPDIR:-/tmp}/scriberr.XXXXXX")"
trap 'rm -f "$TMP_BINARY"' EXIT

# Detect OS and Arch
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"

if [ "$ARCH" == "x86_64" ]; then
    ARCH="amd64"
elif [ "$ARCH" == "aarch64" ] || [ "$ARCH" == "arm64" ]; then
    ARCH="arm64"
else
    echo "Unsupported architecture: $ARCH"
    exit 1
fi

echo "Detected OS: $OS, Arch: $ARCH"

# Construct download URL
DOWNLOAD_URL="$SERVER_URL/api/v1/cli/download?os=$OS&arch=$ARCH"

echo "Downloading CLI from $DOWNLOAD_URL..."
curl --fail --silent --show-error --location --output "$TMP_BINARY" -- "$DOWNLOAD_URL"

chmod 0755 "$TMP_BINARY"

# Handle macOS security protections
if [ "$OS" == "darwin" ]; then
    echo "Applying macOS security fixes..."
    
    # 1. Remove quarantine attribute (Gatekeeper)
    echo "Removing quarantine attribute..."
    xattr -d com.apple.quarantine "$TMP_BINARY" 2>/dev/null || echo "  (No quarantine attribute found or failed to remove)"

    # 2. Ad-hoc sign the binary (Required for arm64)
    echo "Signing binary..."
    codesign -s - -f "$TMP_BINARY" || echo "  (Code signing failed, but might not be strictly necessary if already signed)"
fi

echo "Installing to $INSTALL_DIR..."
if [ -w "$INSTALL_DIR" ]; then
    install -m 0755 "$TMP_BINARY" "$INSTALL_DIR/$BINARY_NAME"
else
    sudo install -m 0755 "$TMP_BINARY" "$INSTALL_DIR/$BINARY_NAME"
fi

echo "Successfully installed $BINARY_NAME to $INSTALL_DIR/$BINARY_NAME"

echo "Please run '$BINARY_NAME login --server $SERVER_URL' to authenticate."

echo "Installation complete!"
`

// GetInstallScript serves the installation script
// GET /api/cli/install
func (h *Handler) GetInstallScript(c *gin.Context) {
	c.Header("Content-Type", "text/x-shellscript")
	c.Header("Cache-Control", "public, max-age=300")
	c.String(http.StatusOK, installScript)
}

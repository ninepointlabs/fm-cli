package images

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	termimg "github.com/blacktop/go-termimg"
)

// TerminalCapability represents what graphics the terminal supports
type TerminalCapability int

const (
	CapNone TerminalCapability = iota
	CapSixel
	CapKitty
	CapITerm2
)

// DetectCapability checks what image protocol the terminal supports
func DetectCapability() TerminalCapability {
	// Check for Kitty
	if os.Getenv("KITTY_WINDOW_ID") != "" {
		return CapKitty
	}

	// Check for iTerm2
	if strings.Contains(os.Getenv("TERM_PROGRAM"), "iTerm") {
		return CapITerm2
	}

	// Check for SIXEL support via terminfo or known terminals
	term := os.Getenv("TERM")
	if strings.Contains(term, "sixel") ||
		strings.Contains(term, "mlterm") ||
		strings.Contains(term, "yaft") ||
		os.Getenv("SIXEL_SUPPORT") == "1" {
		return CapSixel
	}

	// Check for foot terminal (supports sixel)
	if strings.Contains(term, "foot") {
		return CapSixel
	}

	// Check for WezTerm (supports various protocols)
	if os.Getenv("WEZTERM_PANE") != "" {
		return CapSixel
	}

	// Check for Konsole (supports sixel in recent versions)
	if os.Getenv("KONSOLE_VERSION") != "" {
		return CapSixel
	}

	return CapNone
}

// ImageInfo represents an extracted image from an email
type ImageInfo struct {
	URL     string
	CID     string // Content-ID for embedded images
	AltText string
}

// ExtractImagesFromHTML extracts image URLs from HTML content
func ExtractImagesFromHTML(html string) []ImageInfo {
	var images []ImageInfo

	// Match img tags
	imgRegex := regexp.MustCompile(`<img[^>]+src=["']([^"']+)["'][^>]*>`)
	altRegex := regexp.MustCompile(`alt=["']([^"']*)["']`)

	matches := imgRegex.FindAllStringSubmatch(html, -1)
	for _, match := range matches {
		if len(match) >= 2 {
			img := ImageInfo{URL: match[1]}

			// Try to get alt text
			altMatch := altRegex.FindStringSubmatch(match[0])
			if len(altMatch) >= 2 {
				img.AltText = altMatch[1]
			}

			// Check if it's a CID reference
			if strings.HasPrefix(img.URL, "cid:") {
				img.CID = strings.TrimPrefix(img.URL, "cid:")
			}

			images = append(images, img)
		}
	}

	return images
}

// DownloadImage fetches an image from a URL
func DownloadImage(url string) ([]byte, error) {
	// Skip data URIs and CID references for now
	if strings.HasPrefix(url, "data:") || strings.HasPrefix(url, "cid:") {
		return nil, fmt.Errorf("unsupported image source: %s", url[:min(20, len(url))])
	}

	// Only https, only images, and only up to a size a terminal can use.
	// Loading an image confirms the address to the sender, so the caller
	// asks first; this keeps the fetch from doing anything beyond that.
	if !strings.HasPrefix(strings.ToLower(url), "https://") {
		return nil, fmt.Errorf("only https images are loaded")
	}
	const maxImageBytes = 8 << 20
	client := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return fmt.Errorf("too many redirects")
			}
			if req.URL.Scheme != "https" {
				return fmt.Errorf("redirect to a non-https address")
			}
			return nil
		},
	}

	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to download image: %s", resp.Status)
	}
	if ct := strings.ToLower(resp.Header.Get("Content-Type")); !strings.HasPrefix(ct, "image/") {
		return nil, fmt.Errorf("not an image: %s", ct)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxImageBytes {
		return nil, fmt.Errorf("image larger than %d MB", maxImageBytes>>20)
	}
	return data, nil
}

// RenderImage renders an image to the terminal using the best available protocol
func RenderImage(imageData []byte, maxWidth, maxHeight int) (string, error) {
	cap := DetectCapability()

	if cap == CapNone {
		return "", fmt.Errorf("terminal does not support inline images")
	}

	// Create a temp file for the image
	tmpFile, err := os.CreateTemp("", "fm-cli-img-*.png")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.Write(imageData); err != nil {
		tmpFile.Close()
		return "", err
	}
	tmpFile.Close()

	// Use termimg to render
	img, err := termimg.Open(tmpFile.Name())
	if err != nil {
		return "", err
	}

	// Set dimensions
	if maxWidth > 0 {
		img = img.Width(maxWidth)
	}
	if maxHeight > 0 {
		img = img.Height(maxHeight)
	}

	// Capture output
	var buf bytes.Buffer
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	switch cap {
	case CapKitty:
		img.Protocol(termimg.Kitty).Print()
	case CapSixel:
		img.Protocol(termimg.Sixel).Print()
	case CapITerm2:
		img.Protocol(termimg.ITerm2).Print()
	}

	w.Close()
	os.Stdout = oldStdout
	io.Copy(&buf, r)

	return buf.String(), nil
}

// RenderImageFromURL downloads and renders an image
func RenderImageFromURL(url string, maxWidth, maxHeight int) (string, error) {
	data, err := DownloadImage(url)
	if err != nil {
		return "", err
	}
	return RenderImage(data, maxWidth, maxHeight)
}

// OpenInBrowser opens a URL or file in the default browser
func OpenInBrowser(url string) error {
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	default: // Linux and others
		cmd = exec.Command("xdg-open", url)
	}

	return cmd.Start()
}

// browserCSP keeps an email's HTML from running scripts or loading anything
// but images and inline styles once it is opened in the browser.
const browserCSP = `<meta http-equiv="Content-Security-Policy" content="default-src 'none'; img-src https: data:; style-src 'unsafe-inline'; font-src data:">`

// OpenHTMLInBrowser writes the email's HTML to one private file under the
// user's cache directory — replaced on every use, removed again after ten
// minutes — with a Content-Security-Policy that disables scripts, and opens
// it in the browser.
func OpenHTMLInBrowser(html string) error {
	cache, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(cache, "fm-cli")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, "preview.html")
	document := injectCSP(html)
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		return err
	}
	time.AfterFunc(10*time.Minute, func() { _ = os.Remove(path) })
	return OpenInBrowser("file://" + path)
}

var headTag = regexp.MustCompile(`(?i)<head[^>]*>`)

func injectCSP(html string) string {
	if loc := headTag.FindStringIndex(html); loc != nil {
		return html[:loc[1]] + browserCSP + html[loc[1]:]
	}
	return "<!doctype html><html><head>" + browserCSP + "</head><body>" + html + "</body></html>"
}

// HasGraphicsSupport returns true if the terminal supports any image protocol
func HasGraphicsSupport() bool {
	return DetectCapability() != CapNone
}

// GetCapabilityName returns a human-readable name for the capability
func GetCapabilityName() string {
	switch DetectCapability() {
	case CapKitty:
		return "Kitty"
	case CapSixel:
		return "Sixel"
	case CapITerm2:
		return "iTerm2"
	default:
		return "None"
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

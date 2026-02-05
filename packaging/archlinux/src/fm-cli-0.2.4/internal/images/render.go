package images

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
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

	client := &http.Client{
		Timeout: 10 * time.Second,
	}
	
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to download image: %s", resp.Status)
	}
	
	return io.ReadAll(resp.Body)
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

// OpenHTMLInBrowser saves HTML content to a temp file and opens it in browser
func OpenHTMLInBrowser(html string) error {
	tmpFile, err := os.CreateTemp("", "fm-cli-email-*.html")
	if err != nil {
		return err
	}
	
	if _, err := tmpFile.WriteString(html); err != nil {
		tmpFile.Close()
		os.Remove(tmpFile.Name())
		return err
	}
	tmpFile.Close()
	
	// Open in browser (file will be cleaned up later or by OS)
	return OpenInBrowser("file://" + tmpFile.Name())
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

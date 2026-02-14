package cdp

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/devicelab-dev/maestro-runner/pkg/core"
	"github.com/devicelab-dev/maestro-runner/pkg/flow"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

const (
	defaultFindTimeoutMs  = 17000
	optionalFindTimeoutMs = 7000
	defaultViewportW      = 1280
	defaultViewportH      = 800
)

// Config holds browser driver configuration.
type Config struct {
	Headless  bool
	URL       string // Initial URL to navigate to
	ChromeBin string // Path to Chrome binary (empty = auto-download)
	Browser   string // "chrome", "chromium", or path to binary (default: chromium)
	ViewportW int
	ViewportH int
}

// Driver implements core.Driver for desktop browser testing using Rod + CDP.
type Driver struct {
	browser *rod.Browser
	page    *rod.Page
	config  Config

	findTimeoutMs int
	clipboard     string
	viewportW     int
	viewportH     int

	// Dialog handling
	dialogCh chan *proto.PageJavascriptDialogOpening
	stopCh   chan struct{}

	// Selector validation dedup
	warnedFields map[string]bool

	closeOnce sync.Once
	closeErr  error
}

// New creates a new browser Driver.
func New(cfg Config) (*Driver, error) {
	if cfg.ViewportW == 0 {
		cfg.ViewportW = defaultViewportW
	}
	if cfg.ViewportH == 0 {
		cfg.ViewportH = defaultViewportH
	}

	// Cache downloaded browsers in ~/.maestro-runner/browsers/
	homeDir, _ := os.UserHomeDir()
	if homeDir != "" {
		launcher.DefaultBrowserDir = filepath.Join(homeDir, ".maestro-runner", "browsers")
	}

	// Resolve which browser binary to use.
	// Priority: ChromeBin (direct path) > Browser flag > default (chromium/download)
	chromeBin := resolveBrowserBin(cfg)

	// Use a clean profile with password manager disabled
	l := launcher.New().Headless(cfg.Headless).
		Set("no-first-run").
		Set("disable-default-apps").
		Set("disable-popup-blocking").
		Set("disable-translate").
		Set("disable-background-timer-throttling").
		Set("disable-component-update").
		Set("password-store", "basic")
	if chromeBin != "" {
		l = l.Bin(chromeBin)
	} else {
		// Using Rod's bundled Chromium — check if download is needed
		browserDir := launcher.DefaultBrowserDir
		if needsDownload(browserDir) {
			log.Printf("[browser] Downloading Chromium (first time only, subsequent runs will be faster)...")
		}
	}

	// Write Chrome preferences to disable password manager and breach detection
	if dataDir := l.Get("user-data-dir"); dataDir != "" {
		writeChromePref(dataDir)
	}

	controlURL, err := l.Launch()
	if err != nil {
		return nil, fmt.Errorf("failed to launch browser: %w", err)
	}

	// Connect with NoDefaultDevice so we control the viewport
	browser := rod.New().ControlURL(controlURL).NoDefaultDevice()
	if err := browser.Connect(); err != nil {
		return nil, fmt.Errorf("failed to connect to browser: %w", err)
	}

	// Create a new page
	page, err := browser.Page(proto.TargetCreateTarget{})
	if err != nil {
		return nil, fmt.Errorf("failed to create page: %w", err)
	}

	// Set viewport
	err = page.SetViewport(&proto.EmulationSetDeviceMetricsOverride{
		Width:  cfg.ViewportW,
		Height: cfg.ViewportH,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to set viewport: %w", err)
	}

	// Inject JS helper (persists across navigations)
	_, err = page.EvalOnNewDocument(jsHelperCode)
	if err != nil {
		return nil, fmt.Errorf("failed to inject JS helper: %w", err)
	}

	d := &Driver{
		browser:       browser,
		page:          page,
		config:        cfg,
		findTimeoutMs: defaultFindTimeoutMs,
		viewportW:     cfg.ViewportW,
		viewportH:     cfg.ViewportH,
		dialogCh:      make(chan *proto.PageJavascriptDialogOpening, 10),
		stopCh:        make(chan struct{}),
		warnedFields:  make(map[string]bool),
	}

	// Start background dialog handler
	d.startDialogHandler()

	// Navigate to initial URL if provided
	if cfg.URL != "" {
		if err := page.Navigate(cfg.URL); err != nil {
			return nil, fmt.Errorf("failed to navigate to %s: %w", cfg.URL, err)
		}
		page.MustWaitLoad()
	}

	return d, nil
}

// startDialogHandler starts a background goroutine to capture dialog events.
// Uses Rod's EachEvent pattern — the goroutine blocks until the browser closes.
func (d *Driver) startDialogHandler() {
	go d.page.EachEvent(func(e *proto.PageJavascriptDialogOpening) bool {
		select {
		case d.dialogCh <- e:
		default:
			// Channel full — drop oldest
			select {
			case <-d.dialogCh:
			default:
			}
			d.dialogCh <- e
		}
		return false // keep listening
	})()
}

// writeChromePref writes a Chrome Preferences file that disables password manager
// and breach detection popups. Must be called before Launch().
func writeChromePref(userDataDir string) {
	defaultDir := filepath.Join(userDataDir, "Default")
	if err := os.MkdirAll(defaultDir, 0o755); err != nil {
		return
	}
	prefs := `{
  "credentials_enable_service": false,
  "profile": {
    "password_manager_enabled": false,
    "password_manager_leak_detection": false
  },
  "password_manager": {
    "leak_detection": false
  }
}`
	_ = os.WriteFile(filepath.Join(defaultDir, "Preferences"), []byte(prefs), 0o644)
}

// detectChrome returns the path to an installed Chrome/Chromium binary, or empty string.
func detectChrome() string {
	var paths []string
	switch runtime.GOOS {
	case "darwin":
		paths = []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Google Chrome Canary.app/Contents/MacOS/Google Chrome Canary",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
		}
	case "linux":
		paths = []string{
			"/usr/bin/google-chrome",
			"/usr/bin/google-chrome-stable",
			"/usr/bin/chromium",
			"/usr/bin/chromium-browser",
			"/snap/bin/chromium",
		}
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// resolveBrowserBin determines which browser binary to use based on config.
// Returns empty string to let Rod download/use its bundled Chromium.
func resolveBrowserBin(cfg Config) string {
	// Explicit ChromeBin takes highest priority (programmatic use)
	if cfg.ChromeBin != "" {
		return cfg.ChromeBin
	}

	browser := strings.ToLower(strings.TrimSpace(cfg.Browser))
	switch browser {
	case "chrome":
		// User explicitly requested installed Chrome
		if path := detectChrome(); path != "" {
			log.Printf("[browser] Using installed Chrome: %s", path)
			return path
		}
		log.Printf("[browser] Chrome not found, falling back to Chromium")
		return "" // fall back to Rod download
	case "", "chromium":
		// Default: use Rod's bundled Chromium (download if needed)
		return ""
	default:
		// Treat as a custom path to a browser binary
		if _, err := os.Stat(browser); err == nil {
			log.Printf("[browser] Using custom browser: %s", browser)
			return browser
		}
		log.Printf("[browser] Browser binary not found at %s, falling back to Chromium", browser)
		return ""
	}
}

// needsDownload checks if the browser cache directory is empty or missing.
func needsDownload(browserDir string) bool {
	entries, err := os.ReadDir(browserDir)
	if err != nil {
		return true // directory doesn't exist
	}
	return len(entries) == 0
}

// Close shuts down the browser. Safe to call multiple times.
func (d *Driver) Close() error {
	d.closeOnce.Do(func() {
		close(d.stopCh)
		if d.browser != nil {
			d.closeErr = d.browser.Close()
		}
	})
	return d.closeErr
}

// Execute runs a single step and returns the result.
func (d *Driver) Execute(step flow.Step) *core.CommandResult {
	start := time.Now()

	var result *core.CommandResult
	switch s := step.(type) {
	// Tap commands
	case *flow.TapOnStep:
		result = d.tapOn(s)
	case *flow.DoubleTapOnStep:
		result = d.doubleTapOn(s)
	case *flow.LongPressOnStep:
		result = d.longPressOn(s)
	case *flow.TapOnPointStep:
		result = d.tapOnPoint(s)

	// Assert commands
	case *flow.AssertVisibleStep:
		result = d.assertVisible(s)
	case *flow.AssertNotVisibleStep:
		result = d.assertNotVisible(s)

	// Input commands
	case *flow.InputTextStep:
		result = d.inputText(s)
	case *flow.EraseTextStep:
		result = d.eraseText(s)
	case *flow.HideKeyboardStep:
		result = d.hideKeyboard(s)
	case *flow.InputRandomStep:
		result = d.inputRandom(s)

	// Scroll/Swipe commands
	case *flow.ScrollStep:
		result = d.scroll(s)
	case *flow.ScrollUntilVisibleStep:
		result = d.scrollUntilVisible(s)
	case *flow.SwipeStep:
		result = d.swipe(s)

	// Navigation commands
	case *flow.BackStep:
		result = d.back(s)
	case *flow.PressKeyStep:
		result = d.pressKey(s)

	// App lifecycle (URL-based for browser)
	case *flow.LaunchAppStep:
		result = d.launchApp(s)
	case *flow.StopAppStep:
		result = d.stopApp(s)
	case *flow.KillAppStep:
		result = d.killApp(s)
	case *flow.ClearStateStep:
		result = d.clearState(s)

	// Clipboard
	case *flow.CopyTextFromStep:
		result = d.copyTextFrom(s)
	case *flow.PasteTextStep:
		result = d.pasteText(s)
	case *flow.SetClipboardStep:
		result = d.setClipboard(s)

	// Device control
	case *flow.SetOrientationStep:
		result = d.setOrientation(s)
	case *flow.OpenLinkStep:
		result = d.openLink(s)
	case *flow.OpenBrowserStep:
		result = d.openBrowser(s)
	case *flow.SetLocationStep:
		result = d.setLocation(s)

	// Wait commands
	case *flow.WaitUntilStep:
		result = d.waitUntil(s)
	case *flow.WaitForAnimationToEndStep:
		result = d.waitForAnimationToEnd(s)

	// Media
	case *flow.TakeScreenshotStep:
		result = d.takeScreenshot(s)

	// Alert handling
	case *flow.AcceptAlertStep:
		result = d.acceptAlert(s)
	case *flow.DismissAlertStep:
		result = d.dismissAlert(s)

	// Unsupported — mobile-only or not applicable to web
	case *flow.SetAirplaneModeStep, *flow.ToggleAirplaneModeStep:
		result = unsupportedResult("airplane mode is not supported on web platform")
	case *flow.TravelStep:
		result = unsupportedResult("travel is not supported on web platform")
	case *flow.AddMediaStep:
		result = unsupportedResult("addMedia is not supported on web platform")
	case *flow.StartRecordingStep:
		result = unsupportedResult("startRecording is not supported on web platform")
	case *flow.StopRecordingStep:
		result = unsupportedResult("stopRecording is not supported on web platform")
	case *flow.ClearKeychainStep:
		result = unsupportedResult("clearKeychain is not supported on web platform")
	case *flow.SetPermissionsStep:
		result = unsupportedResult("setPermissions is not supported on web platform")
	case *flow.AssertNoDefectsWithAIStep:
		result = unsupportedResult("assertNoDefectsWithAI is not supported on web platform")
	case *flow.AssertWithAIStep:
		result = unsupportedResult("assertWithAI is not supported on web platform")
	case *flow.ExtractTextWithAIStep:
		result = unsupportedResult("extractTextWithAI is not supported on web platform")

	default:
		result = &core.CommandResult{
			Success: false,
			Error:   fmt.Errorf("unknown step type: %T", step),
			Message: fmt.Sprintf("Step type '%T' is not supported on web platform", step),
		}
	}

	result.Duration = time.Since(start)
	return result
}

// Screenshot captures the current page as PNG.
func (d *Driver) Screenshot() ([]byte, error) {
	return d.page.Screenshot(true, nil)
}

// Hierarchy returns the full accessibility tree as JSON.
func (d *Driver) Hierarchy() ([]byte, error) {
	result, err := proto.AccessibilityGetFullAXTree{}.Call(d.page)
	if err != nil {
		return nil, fmt.Errorf("failed to get AX tree: %w", err)
	}
	return json.Marshal(result.Nodes)
}

// GetState returns the current browser state.
func (d *Driver) GetState() *core.StateSnapshot {
	orientation := "portrait"
	if d.viewportW > d.viewportH {
		orientation = "landscape"
	}
	return &core.StateSnapshot{
		Orientation:   orientation,
		ClipboardText: d.clipboard,
	}
}

// GetPlatformInfo returns platform information.
func (d *Driver) GetPlatformInfo() *core.PlatformInfo {
	return &core.PlatformInfo{
		Platform:     "web",
		DeviceName:   "Chrome",
		IsSimulator:  true,
		ScreenWidth:  d.viewportW,
		ScreenHeight: d.viewportH,
	}
}

// SetFindTimeout sets the default timeout for finding elements.
func (d *Driver) SetFindTimeout(ms int) {
	if ms > 0 {
		d.findTimeoutMs = ms
	}
}

// SetWaitForIdleTimeout is a no-op for browser since Rod handles waits differently.
func (d *Driver) SetWaitForIdleTimeout(ms int) error {
	return nil
}

// successResult creates a success result.
func successResult(msg string, elem *core.ElementInfo) *core.CommandResult {
	return &core.CommandResult{
		Success: true,
		Message: msg,
		Element: elem,
	}
}

// errorResult creates an error result.
func errorResult(err error, msg string) *core.CommandResult {
	return &core.CommandResult{
		Success: false,
		Error:   err,
		Message: msg,
	}
}

// unsupportedResult creates an error result for unsupported commands.
func unsupportedResult(msg string) *core.CommandResult {
	return &core.CommandResult{
		Success: false,
		Error:   fmt.Errorf("%s", msg),
		Message: msg,
	}
}

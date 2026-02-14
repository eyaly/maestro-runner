package cdp

import (
	"encoding/json"
	"fmt"
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
}

// New creates a new browser Driver.
func New(cfg Config) (*Driver, error) {
	if cfg.ViewportW == 0 {
		cfg.ViewportW = defaultViewportW
	}
	if cfg.ViewportH == 0 {
		cfg.ViewportH = defaultViewportH
	}

	// Launch Chrome
	l := launcher.New().Headless(cfg.Headless)
	if cfg.ChromeBin != "" {
		l = l.Bin(cfg.ChromeBin)
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

// Close shuts down the browser.
func (d *Driver) Close() error {
	close(d.stopCh)
	if d.browser != nil {
		return d.browser.Close()
	}
	return nil
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

	// Unsupported
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

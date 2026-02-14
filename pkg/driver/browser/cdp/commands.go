package cdp

import (
	"crypto/rand"
	"fmt"
	"log"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/devicelab-dev/maestro-runner/pkg/core"
	"github.com/devicelab-dev/maestro-runner/pkg/flow"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
)

// tapOn taps on an element. Rod's Click() handles scroll+stable+interactable+enabled.
func (d *Driver) tapOn(step *flow.TapOnStep) *core.CommandResult {
	elem, info, err := d.findElement(step.Selector, isOptional(step.Selector.Optional), step.TimeoutMs)
	if err != nil {
		return errorResult(err, fmt.Sprintf("Failed to find element %s", step.Selector.DescribeQuoted()))
	}

	if err := elem.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return errorResult(err, "Failed to tap on element")
	}

	return successResult(fmt.Sprintf("Tapped on %s", step.Selector.DescribeQuoted()), info)
}

// doubleTapOn double-clicks an element.
func (d *Driver) doubleTapOn(step *flow.DoubleTapOnStep) *core.CommandResult {
	elem, info, err := d.findElement(step.Selector, isOptional(step.Selector.Optional), step.TimeoutMs)
	if err != nil {
		return errorResult(err, fmt.Sprintf("Failed to find element %s", step.Selector.DescribeQuoted()))
	}

	if err := elem.Click(proto.InputMouseButtonLeft, 2); err != nil {
		return errorResult(err, "Failed to double tap on element")
	}

	return successResult(fmt.Sprintf("Double tapped on %s", step.Selector.DescribeQuoted()), info)
}

// longPressOn performs a long press (mouse down, hold 1s, mouse up).
func (d *Driver) longPressOn(step *flow.LongPressOnStep) *core.CommandResult {
	elem, info, err := d.findElement(step.Selector, isOptional(step.Selector.Optional), step.TimeoutMs)
	if err != nil {
		return errorResult(err, fmt.Sprintf("Failed to find element %s", step.Selector.DescribeQuoted()))
	}

	// Scroll into view and wait for interactable
	pt, err := elem.WaitInteractable()
	if err != nil {
		return errorResult(err, "Element not interactable for long press")
	}

	mouse := d.page.Mouse
	if err := mouse.MoveTo(*pt); err != nil {
		return errorResult(err, "Failed to move mouse")
	}
	if err := mouse.Down(proto.InputMouseButtonLeft, 1); err != nil {
		return errorResult(err, "Failed to mouse down")
	}
	time.Sleep(1 * time.Second)
	if err := mouse.Up(proto.InputMouseButtonLeft, 1); err != nil {
		return errorResult(err, "Failed to mouse up")
	}

	return successResult(fmt.Sprintf("Long pressed on %s", step.Selector.DescribeQuoted()), info)
}

// tapOnPoint taps at specific coordinates.
func (d *Driver) tapOnPoint(step *flow.TapOnPointStep) *core.CommandResult {
	x, y := step.X, step.Y

	// Handle percentage-based point
	if step.Point != "" {
		px, py, err := parsePercentageCoords(step.Point)
		if err != nil {
			return errorResult(err, "Failed to parse point coordinates")
		}
		x = int(px * float64(d.viewportW))
		y = int(py * float64(d.viewportH))
	}

	mouse := d.page.Mouse
	pt := proto.NewPoint(float64(x), float64(y))
	if err := mouse.MoveTo(pt); err != nil {
		return errorResult(err, "Failed to move mouse")
	}
	if err := mouse.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return errorResult(err, "Failed to click at point")
	}

	return successResult(fmt.Sprintf("Tapped at (%d, %d)", x, y), nil)
}

// assertVisible asserts that an element is visible.
func (d *Driver) assertVisible(step *flow.AssertVisibleStep) *core.CommandResult {
	_, info, err := d.findElement(step.Selector, isOptional(step.Selector.Optional), step.TimeoutMs)
	if err != nil {
		return errorResult(err, fmt.Sprintf("Element %s is not visible", step.Selector.DescribeQuoted()))
	}

	if !info.Visible {
		return errorResult(
			fmt.Errorf("element exists but is not visible"),
			fmt.Sprintf("Element %s exists but is not visible", step.Selector.DescribeQuoted()),
		)
	}

	return successResult(fmt.Sprintf("Element %s is visible", step.Selector.DescribeQuoted()), info)
}

// assertNotVisible asserts that an element is NOT visible.
// Uses RAF-based polling in the browser for fast resolution (~16ms) instead of
// CDP round-trips with 200ms polling.
func (d *Driver) assertNotVisible(step *flow.AssertNotVisibleStep) *core.CommandResult {
	timeoutMs := step.TimeoutMs
	if timeoutMs == 0 {
		timeoutMs = 5000
	}

	selectorType, selectorValue := jsSelectorTypeValue(step.Selector)
	desc := step.Selector.DescribeQuoted()

	// Use the JS RAF-based polling: runs inside the browser, no CDP round-trips.
	// Resolves within ~16ms of element disappearing.
	// ByPromise() tells Rod to await the JS Promise before returning.
	result, err := d.page.Timeout(time.Duration(timeoutMs+1000) * time.Millisecond).Evaluate(
		rod.Eval(`(type, value, timeout) => window.__maestro.waitForNotVisible(type, value, timeout)`,
			selectorType, selectorValue, timeoutMs).ByPromise(),
	)
	if err != nil {
		// JS evaluation failed (e.g. page navigated) — element is gone
		return successResult(fmt.Sprintf("Element %s is not visible", desc), nil)
	}

	if result.Value.Bool() {
		return successResult(fmt.Sprintf("Element %s is not visible", desc), nil)
	}

	return errorResult(
		fmt.Errorf("element is still visible after %dms", timeoutMs),
		fmt.Sprintf("Element %s is still visible", desc),
	)
}

// jsSelectorTypeValue extracts the selector type and value for use with the
// browser-side __maestro JS helper functions.
func jsSelectorTypeValue(sel flow.Selector) (string, string) {
	switch {
	case sel.CSS != "":
		return "css", sel.CSS
	case sel.TestID != "":
		return "testId", sel.TestID
	case sel.Name != "":
		return "name", sel.Name
	case sel.Placeholder != "":
		return "placeholder", sel.Placeholder
	case sel.Href != "":
		return "href", sel.Href
	case sel.Alt != "":
		return "alt", sel.Alt
	case sel.Title != "":
		return "title", sel.Title
	case sel.Role != "":
		return "role", sel.Role
	case sel.ID != "":
		return "id", sel.ID
	case sel.TextRegex != "":
		return "textRegex", sel.TextRegex
	case sel.TextContains != "":
		return "textContains", sel.TextContains
	case sel.Text != "":
		if looksLikeRegex(sel.Text) {
			return "textRegex", sel.Text
		}
		return "text", sel.Text
	default:
		return "text", ""
	}
}

// inputText types text into an element. Rod's Input() handles focus+waitEnabled+waitWritable+events.
func (d *Driver) inputText(step *flow.InputTextStep) *core.CommandResult {
	if !step.Selector.IsEmpty() {
		elem, _, err := d.findElement(step.Selector, isOptional(step.Selector.Optional), step.TimeoutMs)
		if err != nil {
			return errorResult(err, fmt.Sprintf("Failed to find element %s", step.Selector.DescribeQuoted()))
		}
		if err := elem.Input(step.Text); err != nil {
			return errorResult(err, "Failed to input text")
		}
	} else {
		// Type into the currently focused element via keyboard
		if err := d.page.Keyboard.Type([]input.Key(convertToKeys(step.Text))...); err != nil {
			// Fallback: use InsertText for non-typeable characters
			if err := d.page.InsertText(step.Text); err != nil {
				return errorResult(err, "Failed to input text")
			}
		}
	}

	return successResult(fmt.Sprintf("Entered text: %s", step.Text), nil)
}

// eraseText erases characters. Sends Ctrl+A then Backspace, or N backspaces.
func (d *Driver) eraseText(step *flow.EraseTextStep) *core.CommandResult {
	chars := step.Characters
	if chars == 0 {
		chars = 50
	}

	kb := d.page.Keyboard
	for i := 0; i < chars; i++ {
		if err := kb.Type(input.Backspace); err != nil {
			return errorResult(err, "Failed to erase text")
		}
	}

	return successResult(fmt.Sprintf("Erased %d characters", chars), nil)
}

// hideKeyboard is a no-op on web (no virtual keyboard).
func (d *Driver) hideKeyboard(step *flow.HideKeyboardStep) *core.CommandResult {
	return successResult("hideKeyboard is a no-op on web platform", nil)
}

// inputRandom generates and inputs random text.
func (d *Driver) inputRandom(step *flow.InputRandomStep) *core.CommandResult {
	length := step.Length
	if length == 0 {
		length = 10
	}

	var text string
	switch strings.ToUpper(step.DataType) {
	case "EMAIL":
		text = randomEmail()
	case "NUMBER":
		text = randomNumber(length)
	case "PERSON_NAME":
		text = randomPersonName()
	default: // TEXT
		text = randomString(length)
	}

	if err := d.page.InsertText(text); err != nil {
		return errorResult(err, "Failed to input random text")
	}

	result := successResult(fmt.Sprintf("Entered random text: %s", text), nil)
	result.Data = text
	return result
}

// viewportCenter returns the center point of the viewport.
func (d *Driver) viewportCenter() proto.Point {
	return proto.NewPoint(float64(d.viewportW)/2, float64(d.viewportH)/2)
}

// scroll scrolls the page in the given direction.
func (d *Driver) scroll(step *flow.ScrollStep) *core.CommandResult {
	dir := strings.ToLower(step.Direction)
	if dir == "" {
		dir = "down"
	}

	var dx, dy float64
	switch dir {
	case "down":
		dy = 300
	case "up":
		dy = -300
	case "left":
		dx = -300
	case "right":
		dx = 300
	}

	mouse := d.page.Mouse
	if err := mouse.MoveTo(d.viewportCenter()); err != nil {
		return errorResult(err, "Failed to move mouse for scroll")
	}
	if err := mouse.Scroll(dx, dy, 0); err != nil {
		return errorResult(err, "Failed to scroll")
	}

	return successResult(fmt.Sprintf("Scrolled %s", dir), nil)
}

// scrollUntilVisible scrolls until an element is visible.
func (d *Driver) scrollUntilVisible(step *flow.ScrollUntilVisibleStep) *core.CommandResult {
	dir := strings.ToLower(step.Direction)
	if dir == "" {
		dir = "down"
	}
	maxScrolls := 10

	var dy float64
	switch dir {
	case "down":
		dy = 300
	case "up":
		dy = -300
	}

	center := d.viewportCenter()
	for i := 0; i < maxScrolls; i++ {
		_, info, err := d.findElementOnce(step.Element)
		if err == nil && info != nil && info.Visible {
			return successResult(
				fmt.Sprintf("Element visible after %d scrolls", i),
				info,
			)
		}

		mouse := d.page.Mouse
		if err := mouse.MoveTo(center); err != nil {
			log.Printf("[browser] scrollUntilVisible: MoveTo failed: %v", err)
		}
		if err := mouse.Scroll(0, dy, 0); err != nil {
			return errorResult(err, "Failed to scroll")
		}
		time.Sleep(300 * time.Millisecond)
	}

	return errorResult(
		fmt.Errorf("element not visible after %d scrolls", maxScrolls),
		fmt.Sprintf("Element %s not visible after scrolling", step.Element.DescribeQuoted()),
	)
}

// swipe performs a swipe gesture using mouse drag.
func (d *Driver) swipe(step *flow.SwipeStep) *core.CommandResult {
	dir := strings.ToLower(step.Direction)
	center := d.viewportCenter()
	cx, cy := center.X, center.Y

	var startX, startY, endX, endY float64
	switch dir {
	case "up":
		startX, startY = cx, cy*1.4
		endX, endY = cx, cy*0.6
	case "down":
		startX, startY = cx, cy*0.6
		endX, endY = cx, cy*1.4
	case "left":
		startX, startY = cx*1.4, cy
		endX, endY = cx*0.6, cy
	case "right":
		startX, startY = cx*0.6, cy
		endX, endY = cx*1.4, cy
	default:
		return errorResult(fmt.Errorf("unsupported swipe direction: %s", dir), "Invalid swipe direction")
	}

	mouse := d.page.Mouse
	startPt := proto.NewPoint(startX, startY)
	if err := mouse.MoveTo(startPt); err != nil {
		return errorResult(err, "Failed to move mouse for swipe")
	}
	if err := mouse.Down(proto.InputMouseButtonLeft, 1); err != nil {
		return errorResult(err, "Failed to mouse down for swipe")
	}
	endPt := proto.NewPoint(endX, endY)
	if err := mouse.MoveTo(endPt); err != nil {
		return errorResult(err, "Failed to drag for swipe")
	}
	if err := mouse.Up(proto.InputMouseButtonLeft, 1); err != nil {
		return errorResult(err, "Failed to mouse up for swipe")
	}

	return successResult(fmt.Sprintf("Swiped %s", dir), nil)
}

// back navigates back in browser history.
func (d *Driver) back(step *flow.BackStep) *core.CommandResult {
	if err := d.page.NavigateBack(); err != nil {
		return errorResult(err, "Failed to navigate back")
	}
	d.page.MustWaitLoad()
	return successResult("Navigated back", nil)
}

// pressKey presses a keyboard key.
func (d *Driver) pressKey(step *flow.PressKeyStep) *core.CommandResult {
	key := mapKey(step.Key)
	if key == 0 {
		return errorResult(fmt.Errorf("unknown key: %s", step.Key), "Unknown key")
	}

	if err := d.page.Keyboard.Type(key); err != nil {
		return errorResult(err, fmt.Sprintf("Failed to press key: %s", step.Key))
	}

	return successResult(fmt.Sprintf("Pressed key: %s", step.Key), nil)
}

// launchApp navigates to the app URL.
func (d *Driver) launchApp(step *flow.LaunchAppStep) *core.CommandResult {
	url := step.AppID
	if url == "" {
		return errorResult(fmt.Errorf("no URL specified"), "No URL specified for launchApp")
	}

	if step.ClearState {
		d.clearAllState()
	}

	if err := d.page.Navigate(url); err != nil {
		return errorResult(err, fmt.Sprintf("Failed to navigate to %s", url))
	}
	d.page.MustWaitLoad()

	return successResult(fmt.Sprintf("Navigated to %s", url), nil)
}

// stopApp navigates to about:blank.
func (d *Driver) stopApp(step *flow.StopAppStep) *core.CommandResult {
	return d.navigateBlank()
}

// killApp navigates to about:blank.
func (d *Driver) killApp(step *flow.KillAppStep) *core.CommandResult {
	return d.navigateBlank()
}

// navigateBlank navigates to about:blank (shared by stopApp/killApp).
func (d *Driver) navigateBlank() *core.CommandResult {
	if err := d.page.Navigate("about:blank"); err != nil {
		return errorResult(err, "Failed to navigate to about:blank")
	}
	return successResult("Navigated to about:blank", nil)
}

// clearState clears cookies, storage, and cache.
func (d *Driver) clearState(step *flow.ClearStateStep) *core.CommandResult {
	d.clearAllState()
	return successResult("Cleared browser state", nil)
}

// clearAllState clears cookies, local/session storage, and cache.
func (d *Driver) clearAllState() {
	if err := d.page.SetCookies(nil); err != nil {
		log.Printf("[browser] clearState: failed to clear cookies: %v", err)
	}

	d.page.MustEval(`() => {
		try { localStorage.clear(); } catch(e) {}
		try { sessionStorage.clear(); } catch(e) {}
	}`)

	if err := (proto.NetworkClearBrowserCache{}).Call(d.page); err != nil {
		log.Printf("[browser] clearState: failed to clear cache: %v", err)
	}
}

// copyTextFrom copies text from an element.
func (d *Driver) copyTextFrom(step *flow.CopyTextFromStep) *core.CommandResult {
	elem, info, err := d.findElement(step.Selector, isOptional(step.Selector.Optional), step.TimeoutMs)
	if err != nil {
		return errorResult(err, fmt.Sprintf("Failed to find element %s", step.Selector.DescribeQuoted()))
	}

	text, err := elem.Text()
	if err != nil {
		return errorResult(err, "Failed to get text from element")
	}

	d.clipboard = text

	result := successResult(fmt.Sprintf("Copied text: %s", text), info)
	result.Data = text
	return result
}

// pasteText pastes clipboard text into the focused element.
func (d *Driver) pasteText(step *flow.PasteTextStep) *core.CommandResult {
	if d.clipboard == "" {
		return errorResult(fmt.Errorf("clipboard is empty"), "Clipboard is empty")
	}

	if err := d.page.InsertText(d.clipboard); err != nil {
		return errorResult(err, "Failed to paste text")
	}

	return successResult(fmt.Sprintf("Pasted text: %s", d.clipboard), nil)
}

// setClipboard stores text in the driver's clipboard.
func (d *Driver) setClipboard(step *flow.SetClipboardStep) *core.CommandResult {
	d.clipboard = step.Text
	return successResult(fmt.Sprintf("Set clipboard: %s", step.Text), nil)
}

// setOrientation changes viewport dimensions to simulate orientation.
func (d *Driver) setOrientation(step *flow.SetOrientationStep) *core.CommandResult {
	switch strings.ToUpper(step.Orientation) {
	case "LANDSCAPE", "LANDSCAPE_LEFT", "LANDSCAPE_RIGHT":
		if d.viewportW < d.viewportH {
			d.viewportW, d.viewportH = d.viewportH, d.viewportW
		}
	default: // PORTRAIT
		if d.viewportW > d.viewportH {
			d.viewportW, d.viewportH = d.viewportH, d.viewportW
		}
	}

	err := d.page.SetViewport(&proto.EmulationSetDeviceMetricsOverride{
		Width:  d.viewportW,
		Height: d.viewportH,
	})
	if err != nil {
		return errorResult(err, "Failed to set orientation")
	}

	return successResult(fmt.Sprintf("Set orientation: %s", step.Orientation), nil)
}

// openLink navigates to a URL.
func (d *Driver) openLink(step *flow.OpenLinkStep) *core.CommandResult {
	return d.navigateToURL(step.Link)
}

// openBrowser navigates to a URL.
func (d *Driver) openBrowser(step *flow.OpenBrowserStep) *core.CommandResult {
	return d.navigateToURL(step.URL)
}

// navigateToURL navigates to a URL and waits for load (shared by openLink/openBrowser).
func (d *Driver) navigateToURL(url string) *core.CommandResult {
	if err := d.page.Navigate(url); err != nil {
		return errorResult(err, fmt.Sprintf("Failed to open %s", url))
	}
	d.page.MustWaitLoad()
	return successResult(fmt.Sprintf("Opened %s", url), nil)
}

// setLocation sets geolocation via Emulation CDP domain.
func (d *Driver) setLocation(step *flow.SetLocationStep) *core.CommandResult {
	lat, err := strconv.ParseFloat(step.Latitude, 64)
	if err != nil {
		return errorResult(err, "Invalid latitude")
	}
	lng, err := strconv.ParseFloat(step.Longitude, 64)
	if err != nil {
		return errorResult(err, "Invalid longitude")
	}

	// Grant geolocation permission
	if err := (proto.BrowserGrantPermissions{
		Permissions: []proto.BrowserPermissionType{proto.BrowserPermissionTypeGeolocation},
	}).Call(d.browser); err != nil {
		log.Printf("[browser] setLocation: failed to grant geolocation permission: %v", err)
	}

	accuracy := 100.0
	err = proto.EmulationSetGeolocationOverride{
		Latitude:  &lat,
		Longitude: &lng,
		Accuracy:  &accuracy,
	}.Call(d.page)
	if err != nil {
		return errorResult(err, "Failed to set geolocation")
	}

	return successResult(fmt.Sprintf("Set location: %s, %s", step.Latitude, step.Longitude), nil)
}

// waitUntil waits for an element to become visible or not visible.
func (d *Driver) waitUntil(step *flow.WaitUntilStep) *core.CommandResult {
	timeoutMs := step.TimeoutMs
	if timeoutMs == 0 {
		timeoutMs = 30000
	}
	deadline := time.Now().Add(time.Duration(timeoutMs) * time.Millisecond)

	for time.Now().Before(deadline) {
		if step.Visible != nil {
			_, info, err := d.findElementOnce(*step.Visible)
			if err == nil && info != nil && info.Visible {
				return successResult("Element is now visible", info)
			}
		}
		if step.NotVisible != nil {
			_, info, err := d.findElementOnce(*step.NotVisible)
			if err != nil || info == nil || !info.Visible {
				return successResult("Element is no longer visible", nil)
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	return errorResult(
		fmt.Errorf("wait condition not met within %dms", timeoutMs),
		fmt.Sprintf("Wait condition not met within %ds", timeoutMs/1000),
	)
}

// waitForAnimationToEnd waits for the DOM to stabilize.
func (d *Driver) waitForAnimationToEnd(step *flow.WaitForAnimationToEndStep) *core.CommandResult {
	p := d.page.Timeout(10 * time.Second)
	if err := p.WaitDOMStable(300*time.Millisecond, 0); err != nil {
		return errorResult(err, "DOM did not stabilize")
	}
	return successResult("Animation ended (DOM stable)", nil)
}

// takeScreenshot captures a full-page screenshot.
func (d *Driver) takeScreenshot(step *flow.TakeScreenshotStep) *core.CommandResult {
	data, err := d.page.Screenshot(true, nil)
	if err != nil {
		return errorResult(err, "Failed to take screenshot")
	}

	result := successResult("Screenshot taken", nil)
	result.Data = data
	return result
}

// acceptAlert accepts the currently showing dialog.
func (d *Driver) acceptAlert(step *flow.AcceptAlertStep) *core.CommandResult {
	return d.handleDialog(true)
}

// dismissAlert dismisses the currently showing dialog.
func (d *Driver) dismissAlert(step *flow.DismissAlertStep) *core.CommandResult {
	return d.handleDialog(false)
}

// handleDialog accepts or dismisses the current JS dialog (shared by acceptAlert/dismissAlert).
func (d *Driver) handleDialog(accept bool) *core.CommandResult {
	err := proto.PageHandleJavaScriptDialog{Accept: accept}.Call(d.page)
	if err != nil {
		action := "accept"
		if !accept {
			action = "dismiss"
		}
		return errorResult(err, fmt.Sprintf("No alert to %s", action))
	}
	if accept {
		return successResult("Accepted alert", nil)
	}
	return successResult("Dismissed alert", nil)
}

// --- Helper functions ---

// isOptional returns true if the Optional pointer is set and true.
func isOptional(opt *bool) bool {
	return opt != nil && *opt
}

// parsePercentageCoords parses "x%, y%" format coordinates.
func parsePercentageCoords(point string) (float64, float64, error) {
	parts := strings.Split(point, ",")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid point format: %s", point)
	}

	xStr := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(parts[0]), "%"))
	yStr := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(parts[1]), "%"))

	x, err := strconv.ParseFloat(xStr, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid x coordinate: %s", parts[0])
	}
	y, err := strconv.ParseFloat(yStr, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid y coordinate: %s", parts[1])
	}

	return x / 100, y / 100, nil
}

// mapKey maps a key name to a Rod input key.
func mapKey(name string) input.Key {
	switch strings.ToLower(name) {
	case "enter":
		return input.Enter
	case "back", "backspace", "delete":
		return input.Backspace
	case "tab":
		return input.Tab
	case "space":
		return input.Space
	case "escape", "esc":
		return input.Escape
	case "home":
		return input.Home
	case "end":
		return input.End
	case "dpad_up", "arrow_up", "up":
		return input.ArrowUp
	case "dpad_down", "arrow_down", "down":
		return input.ArrowDown
	case "dpad_left", "arrow_left", "left":
		return input.ArrowLeft
	case "dpad_right", "arrow_right", "right":
		return input.ArrowRight
	case "page_up":
		return input.PageUp
	case "page_down":
		return input.PageDown
	default:
		return 0
	}
}

// convertToKeys converts a string to input.Key slice for typing.
func convertToKeys(text string) []input.Key {
	var keys []input.Key
	for _, ch := range text {
		keys = append(keys, input.Key(ch))
	}
	return keys
}

// --- Random text generators ---

const alphanumeric = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// cryptoRandIntn returns a cryptographically secure random int in [0, max).
func cryptoRandIntn(max int) int {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(max)))
	if err != nil {
		log.Printf("[browser] crypto/rand failed, using fallback: %v", err)
		return 0
	}
	return int(n.Int64())
}

func randomString(length int) string {
	b := make([]byte, length)
	for i := range b {
		b[i] = alphanumeric[cryptoRandIntn(len(alphanumeric))]
	}
	return string(b)
}

func randomNumber(length int) string {
	b := make([]byte, length)
	for i := range b {
		b[i] = '0' + byte(cryptoRandIntn(10))
	}
	return string(b)
}

func randomEmail() string {
	return randomString(8) + "@" + randomString(6) + ".com"
}

var firstNames = []string{"Alice", "Bob", "Charlie", "Diana", "Eve", "Frank", "Grace", "Henry"}
var lastNames = []string{"Smith", "Johnson", "Brown", "Taylor", "Wilson", "Davis", "Clark", "Lewis"}

func randomPersonName() string {
	return firstNames[cryptoRandIntn(len(firstNames))] + " " + lastNames[cryptoRandIntn(len(lastNames))]
}

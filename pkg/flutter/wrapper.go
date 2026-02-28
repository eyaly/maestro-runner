package flutter

import (
	"fmt"
	"strings"
	"time"

	"github.com/devicelab-dev/maestro-runner/pkg/core"
	"github.com/devicelab-dev/maestro-runner/pkg/flow"
	"github.com/devicelab-dev/maestro-runner/pkg/logger"
)

// FlutterDriver wraps an inner core.Driver and falls back to the Flutter VM Service
// when the inner driver cannot find an element. Supports lazy connection — the
// client may be nil initially and will be discovered on first fallback attempt.
//
// Fallback chain: inner driver (UIAutomator2) → semantics tree → widget tree.
type FlutterDriver struct {
	inner     core.Driver
	client    *VMServiceClient
	dev       DeviceExecutor
	appID     string
	attempted bool // true after first discovery attempt (avoids retrying every step)
}

// Wrap creates a FlutterDriver that wraps the given inner driver.
// client may be nil — connection will be established lazily on first fallback.
func Wrap(inner core.Driver, client *VMServiceClient, dev DeviceExecutor, appID string) core.Driver {
	return &FlutterDriver{
		inner:  inner,
		client: client,
		dev:    dev,
		appID:  appID,
	}
}

// Execute runs the step on the inner driver first. If the inner driver returns an
// element-not-found error for an element-finding step, it falls back to the Flutter
// VM Service: first the semantics tree, then the widget tree.
func (d *FlutterDriver) Execute(step flow.Step) *core.CommandResult {
	// After launchApp, the app restarts — invalidate connection so we re-discover
	if _, ok := step.(*flow.LaunchAppStep); ok {
		if d.client != nil {
			d.client.Close()
			d.client = nil
		}
		d.attempted = false
	}

	result := d.inner.Execute(step)
	if result.Success {
		return result
	}

	if !isElementFindingStep(step) {
		return result
	}

	if !isElementNotFoundError(result) {
		return result
	}

	sel := extractSelector(step)
	if sel == nil || sel.IsEmpty() {
		return result
	}

	// Only attempt Flutter fallback for text/ID selectors
	if sel.Text == "" && sel.ID == "" {
		return result
	}

	logger.Info("Flutter fallback: attempting for %s (text=%q, id=%q)", step.Type(), sel.Text, sel.ID)

	flutterResult := d.findViaFlutter(step, sel)
	if flutterResult != nil {
		return flutterResult
	}

	logger.Info("Flutter fallback: element not found in Flutter VM service")
	return result
}

// findViaFlutter attempts to find and interact with an element via the Flutter VM Service.
// It tries the semantics tree first, then falls back to the widget tree for cross-referencing.
func (d *FlutterDriver) findViaFlutter(step flow.Step, sel *flow.Selector) *core.CommandResult {
	dump, err := d.getSemanticsTreeWithReconnect()
	if err != nil {
		logger.Debug("Flutter fallback: failed to get semantics tree: %v", err)
		return nil
	}

	root, pixelRatio, err := ParseSemanticsTree(dump)
	if err != nil {
		logger.Debug("Flutter fallback: failed to parse semantics tree: %v", err)
		return nil
	}

	// Step 1: Search semantics tree directly
	nodes := searchSemanticsTree(root, sel)
	isSuffix := false

	// Step 2: If not found in semantics tree, try widget tree cross-reference
	if len(nodes) == 0 {
		var wtMatch *WidgetTreeMatch
		nodes, wtMatch = d.searchViaWidgetTree(root, sel)
		if wtMatch != nil {
			isSuffix = wtMatch.IsSuffix
		}
	}

	if len(nodes) == 0 {
		logger.Debug("Flutter fallback: no matching node (text=%q, id=%q)", sel.Text, sel.ID)
		return nil
	}

	node := nodes[0]
	bounds := node.Rect.ToBounds(pixelRatio)
	cx, cy := bounds.Center()

	// For suffix icons merged into a TextField, the suffix's a11y node only
	// appears when the TextField is focused. Tap the center first to focus,
	// then tap the suffix icon position (24dp icon at the right edge).
	if isSuffix {
		rightEdge := int(node.Rect.Right * pixelRatio)
		cx = rightEdge - int(12*pixelRatio) // center of 24dp icon at right edge

		// Pre-focus: tap center of the TextField so the suffix becomes tappable
		switch step.(type) {
		case *flow.TapOnStep, *flow.DoubleTapOnStep, *flow.LongPressOnStep:
			centerX, centerY := bounds.Center()
			focusTap := &flow.TapOnPointStep{X: centerX, Y: centerY}
			focusTap.StepType = flow.StepTapOnPoint
			d.inner.Execute(focusTap)
			time.Sleep(500 * time.Millisecond)
		}

		logger.Info("Flutter fallback: suffix icon — targeting right edge (node #%d at %d,%d)", node.ID, cx, cy)
	} else {
		logger.Info("Flutter fallback: found element (node #%d at %d,%d)", node.ID, cx, cy)
	}

	return d.executeWithCoordinates(step, node, cx, cy, bounds)
}

// searchSemanticsTree searches the parsed semantics tree for matching nodes.
func searchSemanticsTree(root *SemanticsNode, sel *flow.Selector) []*SemanticsNode {
	var nodes []*SemanticsNode
	if sel.ID != "" {
		nodes = FindByIdentifier(root, sel.ID)
		if len(nodes) == 0 {
			nodes = FindByLabel(root, sel.ID)
		}
	}
	if len(nodes) == 0 && sel.Text != "" {
		nodes = FindByIdentifier(root, sel.Text)
		if len(nodes) == 0 {
			nodes = FindByLabel(root, sel.Text)
		}
		if len(nodes) == 0 {
			nodes = FindByHint(root, sel.Text)
		}
	}
	return nodes
}

// searchViaWidgetTree gets the widget tree and cross-references findings with
// the semantics tree to get coordinates. Returns matching nodes and the widget tree match info.
func (d *FlutterDriver) searchViaWidgetTree(root *SemanticsNode, sel *flow.Selector) ([]*SemanticsNode, *WidgetTreeMatch) {
	if d.client == nil {
		return nil, nil
	}

	widgetDump, err := d.client.GetWidgetTree()
	if err != nil {
		logger.Debug("Flutter fallback: failed to get widget tree: %v", err)
		return nil, nil
	}

	logger.Info("Flutter fallback: searching widget tree (%d bytes)", len(widgetDump))

	// Search widget tree for text (hintText) or identifier
	var match *WidgetTreeMatch
	if sel.Text != "" {
		match = SearchWidgetTreeForText(widgetDump, sel.Text)
	}
	if match == nil && sel.ID != "" {
		match = SearchWidgetTreeForID(widgetDump, sel.ID)
	}

	if match == nil {
		logger.Debug("Flutter fallback: text/id not found in widget tree either")
		return nil, nil
	}

	logger.Info("Flutter fallback: found in widget tree (type=%s, label=%q, nearbyText=%q, suffix=%v)",
		match.MatchType, match.LabelText, match.NearbyText, match.IsSuffix)

	// Cross-reference with semantics tree for coordinates
	return match.CrossReferenceWithSemantics(root), match
}

// executeWithCoordinates dispatches the step using the element's coordinates.
func (d *FlutterDriver) executeWithCoordinates(step flow.Step, node *SemanticsNode, cx, cy int, bounds core.Bounds) *core.CommandResult {
	elemInfo := &core.ElementInfo{
		Text:   node.Label,
		Bounds: bounds,
		ID:     node.Identifier,
	}

	switch s := step.(type) {
	case *flow.TapOnStep:
		tapStep := &flow.TapOnPointStep{
			X:         cx,
			Y:         cy,
			LongPress: s.LongPress,
			Repeat:    s.Repeat,
		}
		tapStep.StepType = flow.StepTapOnPoint
		return d.inner.Execute(tapStep)

	case *flow.DoubleTapOnStep:
		tapStep := &flow.TapOnPointStep{
			X:      cx,
			Y:      cy,
			Repeat: 2,
		}
		tapStep.StepType = flow.StepTapOnPoint
		return d.inner.Execute(tapStep)

	case *flow.LongPressOnStep:
		tapStep := &flow.TapOnPointStep{
			X:         cx,
			Y:         cy,
			LongPress: true,
		}
		tapStep.StepType = flow.StepTapOnPoint
		return d.inner.Execute(tapStep)

	case *flow.AssertVisibleStep:
		_ = s
		return core.SuccessResult(
			fmt.Sprintf("Element found via Flutter VM service (node #%d)", node.ID),
			elemInfo,
		)

	case *flow.InputTextStep:
		// Tap to focus the element first
		tapStep := &flow.TapOnPointStep{X: cx, Y: cy}
		tapStep.StepType = flow.StepTapOnPoint
		tapResult := d.inner.Execute(tapStep)
		if !tapResult.Success {
			return tapResult
		}
		return d.inner.Execute(step)

	case *flow.CopyTextFromStep:
		_ = s
		tapStep := &flow.TapOnPointStep{X: cx, Y: cy}
		tapStep.StepType = flow.StepTapOnPoint
		tapResult := d.inner.Execute(tapStep)
		if !tapResult.Success {
			return tapResult
		}
		return d.inner.Execute(step)
	}

	return nil
}

// ensureConnected establishes or re-establishes the VM Service connection.
func (d *FlutterDriver) ensureConnected() error {
	if d.client != nil {
		return nil
	}

	wsURL, err := DiscoverVMService(d.dev, d.appID)
	if err != nil {
		return fmt.Errorf("discovery failed: %w", err)
	}
	if wsURL == "" {
		return fmt.Errorf("no Flutter VM service found")
	}

	client, err := Connect(wsURL)
	if err != nil {
		return fmt.Errorf("connect failed: %w", err)
	}

	d.client = client
	logger.Info("Flutter VM service connected (lazy)")
	return nil
}

// getSemanticsTreeWithReconnect gets the semantics tree, attempting reconnection once on failure.
func (d *FlutterDriver) getSemanticsTreeWithReconnect() (string, error) {
	// Lazy connect on first use
	if d.client == nil {
		if d.attempted {
			return "", fmt.Errorf("Flutter VM service not available (already attempted)")
		}
		d.attempted = true
		if err := d.ensureConnected(); err != nil {
			logger.Debug("Flutter fallback: lazy connect failed: %v", err)
			return "", err
		}
	}

	dump, err := d.client.GetSemanticsTree()
	if err == nil {
		return dump, nil
	}

	// Attempt reconnection (app may have restarted via launchApp clearState)
	logger.Debug("Flutter fallback: reconnecting VM service after error: %v", err)
	d.client.Close()
	d.client = nil
	d.attempted = false // Allow re-discovery after app restart

	if err := d.ensureConnected(); err != nil {
		return "", fmt.Errorf("reconnect failed: %w", err)
	}

	return d.client.GetSemanticsTree()
}

// isElementFindingStep returns true for step types that involve finding an element.
func isElementFindingStep(step flow.Step) bool {
	switch step.(type) {
	case *flow.TapOnStep, *flow.DoubleTapOnStep, *flow.LongPressOnStep,
		*flow.AssertVisibleStep, *flow.InputTextStep, *flow.CopyTextFromStep:
		return true
	}
	return false
}

// isElementNotFoundError checks if a command result indicates an element-not-found error.
func isElementNotFoundError(result *core.CommandResult) bool {
	if result.Error == nil && result.Message == "" {
		return false
	}

	check := func(s string) bool {
		s = strings.ToLower(s)
		return strings.Contains(s, "not found") ||
			strings.Contains(s, "not visible") ||
			strings.Contains(s, "no such element") ||
			strings.Contains(s, "could not be located")
	}

	if result.Error != nil && check(result.Error.Error()) {
		return true
	}
	return result.Message != "" && check(result.Message)
}

// extractSelector gets the selector from element-finding steps.
func extractSelector(step flow.Step) *flow.Selector {
	switch s := step.(type) {
	case *flow.TapOnStep:
		return &s.Selector
	case *flow.DoubleTapOnStep:
		return &s.Selector
	case *flow.LongPressOnStep:
		return &s.Selector
	case *flow.AssertVisibleStep:
		return &s.Selector
	case *flow.InputTextStep:
		return &s.Selector
	case *flow.CopyTextFromStep:
		return &s.Selector
	}
	return nil
}

// Close closes the VM Service client if connected.
func (d *FlutterDriver) Close() {
	if d.client != nil {
		d.client.Close()
		d.client = nil
	}
}

// --- Pass-through methods ---

func (d *FlutterDriver) Screenshot() ([]byte, error)       { return d.inner.Screenshot() }
func (d *FlutterDriver) Hierarchy() ([]byte, error)         { return d.inner.Hierarchy() }
func (d *FlutterDriver) GetState() *core.StateSnapshot      { return d.inner.GetState() }
func (d *FlutterDriver) GetPlatformInfo() *core.PlatformInfo { return d.inner.GetPlatformInfo() }
func (d *FlutterDriver) SetFindTimeout(ms int)              { d.inner.SetFindTimeout(ms) }
func (d *FlutterDriver) SetWaitForIdleTimeout(ms int) error {
	return d.inner.SetWaitForIdleTimeout(ms)
}

// --- Optional interface forwarding ---

func (d *FlutterDriver) CDPState() *core.CDPInfo {
	if p, ok := d.inner.(core.CDPStateProvider); ok {
		return p.CDPState()
	}
	return nil
}

func (d *FlutterDriver) ForceStop(appID string) error {
	if m, ok := d.inner.(core.AppLifecycleManager); ok {
		return m.ForceStop(appID)
	}
	return fmt.Errorf("inner driver does not support ForceStop")
}

func (d *FlutterDriver) ClearAppData(appID string) error {
	if m, ok := d.inner.(core.AppLifecycleManager); ok {
		return m.ClearAppData(appID)
	}
	return fmt.Errorf("inner driver does not support ClearAppData")
}

func (d *FlutterDriver) DetectWebView() (*core.WebViewInfo, error) {
	if w, ok := d.inner.(core.WebViewDetector); ok {
		return w.DetectWebView()
	}
	return nil, fmt.Errorf("inner driver does not support DetectWebView")
}

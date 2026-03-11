package flutter

import (
	"fmt"
	"testing"

	"github.com/devicelab-dev/maestro-runner/pkg/core"
	"github.com/devicelab-dev/maestro-runner/pkg/flow"
)

// mockDriver implements core.Driver for testing.
type mockDriver struct {
	executeFunc func(step flow.Step) *core.CommandResult
	lastStep    flow.Step
}

func (m *mockDriver) Execute(step flow.Step) *core.CommandResult {
	m.lastStep = step
	if m.executeFunc != nil {
		return m.executeFunc(step)
	}
	return core.SuccessResult("ok", nil)
}

func (m *mockDriver) Screenshot() ([]byte, error)           { return nil, nil }
func (m *mockDriver) Hierarchy() ([]byte, error)             { return nil, nil }
func (m *mockDriver) GetState() *core.StateSnapshot          { return &core.StateSnapshot{} }
func (m *mockDriver) GetPlatformInfo() *core.PlatformInfo    { return &core.PlatformInfo{} }
func (m *mockDriver) SetFindTimeout(ms int)                  {}
func (m *mockDriver) SetWaitForIdleTimeout(ms int) error     { return nil }

func TestFlutterDriver_PassThrough_Success(t *testing.T) {
	inner := &mockDriver{
		executeFunc: func(step flow.Step) *core.CommandResult {
			return core.SuccessResult("tapped", &core.ElementInfo{Text: "Login"})
		},
	}

	fd := &FlutterDriver{inner: inner}

	step := &flow.TapOnStep{}
	step.Selector.Text = "Login"
	step.StepType = flow.StepTapOn

	result := fd.Execute(step)
	if !result.Success {
		t.Errorf("expected success, got error: %v", result.Error)
	}
	if result.Message != "tapped" {
		t.Errorf("message = %q, want %q", result.Message, "tapped")
	}
}

func TestFlutterDriver_NonElementStep_NoFallback(t *testing.T) {
	inner := &mockDriver{
		executeFunc: func(step flow.Step) *core.CommandResult {
			return core.ErrorResult(fmt.Errorf("some error"), "")
		},
	}

	fd := &FlutterDriver{inner: inner}

	// BackStep is not an element-finding step
	step := &flow.BackStep{}
	step.StepType = flow.StepBack

	result := fd.Execute(step)
	if result.Success {
		t.Error("expected failure to pass through")
	}
}

func TestFlutterDriver_NonElementError_NoFallback(t *testing.T) {
	inner := &mockDriver{
		executeFunc: func(step flow.Step) *core.CommandResult {
			return core.ErrorResult(fmt.Errorf("network timeout"), "")
		},
	}

	fd := &FlutterDriver{inner: inner}

	step := &flow.TapOnStep{}
	step.Selector.Text = "Login"
	step.StepType = flow.StepTapOn

	result := fd.Execute(step)
	if result.Success {
		t.Error("expected failure for non-element error")
	}
	// Should NOT fallback for network errors
	if result.Error.Error() != "network timeout" {
		t.Errorf("error = %q, want %q", result.Error.Error(), "network timeout")
	}
}

func TestFlutterDriver_EmptySelector_NoFallback(t *testing.T) {
	inner := &mockDriver{
		executeFunc: func(step flow.Step) *core.CommandResult {
			return core.ErrorResult(fmt.Errorf("element not found"), "")
		},
	}

	fd := &FlutterDriver{inner: inner}

	// Empty selector
	step := &flow.TapOnStep{}
	step.StepType = flow.StepTapOn

	result := fd.Execute(step)
	if result.Success {
		t.Error("expected failure for empty selector")
	}
}

func TestFlutterDriver_TapOnFallback(t *testing.T) {
	var tappedStep flow.Step

	inner := &mockDriver{
		executeFunc: func(step flow.Step) *core.CommandResult {
			// Inner driver can't find by selector (accessibility bridge limitation)
			// but can tap by coordinates
			if _, ok := step.(*flow.TapOnPointStep); ok {
				tappedStep = step
				return core.SuccessResult("tapped at point", nil)
			}
			return core.ErrorResult(fmt.Errorf("element not found: text=\"Login\""), "")
		},
	}

	semanticsDump := `SemanticsNode#0
 Rect.fromLTRB(0.0, 0.0, 411.4, 890.3)
 scaled by 1.0x
 ├─SemanticsNode#1
   Rect.fromLTRB(100.0, 200.0, 300.0, 250.0)
   label: "Login"
   identifier: "login_button"
`

	wsURL, cleanup := startMockVMService(t, semanticsDump)
	defer cleanup()

	client, err := Connect(wsURL)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer client.Close()

	fd := &FlutterDriver{inner: inner, client: client}

	step := &flow.TapOnStep{}
	step.Selector.Text = "Login"
	step.StepType = flow.StepTapOn

	result := fd.Execute(step)
	if !result.Success {
		t.Fatalf("expected success, got: %v", result.Error)
	}

	// Verify it tapped at the center of the element's bounds
	pointStep, ok := tappedStep.(*flow.TapOnPointStep)
	if !ok {
		t.Fatalf("expected TapOnPointStep, got %T", tappedStep)
	}
	// Center of Rect(100, 200, 300, 250) with pixelRatio 1.0 = (200, 225)
	if pointStep.X != 200 || pointStep.Y != 225 {
		t.Errorf("tap point = (%d, %d), want (200, 225)", pointStep.X, pointStep.Y)
	}
}

func TestFlutterDriver_AssertVisibleFallback(t *testing.T) {
	inner := &mockDriver{
		executeFunc: func(step flow.Step) *core.CommandResult {
			return core.ErrorResult(fmt.Errorf("Element not visible: text=\"Welcome\""), "")
		},
	}

	semanticsDump := `SemanticsNode#0
 Rect.fromLTRB(0.0, 0.0, 400.0, 800.0)
 scaled by 2.0x
 ├─SemanticsNode#1
   Rect.fromLTRB(10.0, 20.0, 200.0, 60.0)
   label: "Welcome"
`

	wsURL, cleanup := startMockVMService(t, semanticsDump)
	defer cleanup()

	client, err := Connect(wsURL)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer client.Close()

	fd := &FlutterDriver{inner: inner, client: client}

	step := &flow.AssertVisibleStep{}
	step.Selector.Text = "Welcome"
	step.StepType = flow.StepAssertVisible

	result := fd.Execute(step)
	if !result.Success {
		t.Errorf("expected success from Flutter fallback, got: %v", result.Error)
	}
	if result.Element == nil {
		t.Fatal("expected ElementInfo")
	}
	if result.Element.Text != "Welcome" {
		t.Errorf("element text = %q, want %q", result.Element.Text, "Welcome")
	}
}

func TestFlutterDriver_DoubleTapFallback(t *testing.T) {
	var tappedStep flow.Step

	inner := &mockDriver{
		executeFunc: func(step flow.Step) *core.CommandResult {
			if _, ok := step.(*flow.TapOnPointStep); ok {
				tappedStep = step
				return core.SuccessResult("double tapped", nil)
			}
			return core.ErrorResult(fmt.Errorf("element not found"), "")
		},
	}

	semanticsDump := `SemanticsNode#0
 Rect.fromLTRB(0.0, 0.0, 400.0, 800.0)
 scaled by 1.0x
 ├─SemanticsNode#1
   Rect.fromLTRB(50.0, 100.0, 150.0, 140.0)
   label: "Item"
`

	wsURL, cleanup := startMockVMService(t, semanticsDump)
	defer cleanup()

	client, err := Connect(wsURL)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer client.Close()

	fd := &FlutterDriver{inner: inner, client: client}

	step := &flow.DoubleTapOnStep{}
	step.Selector.Text = "Item"
	step.StepType = flow.StepDoubleTapOn

	result := fd.Execute(step)
	if !result.Success {
		t.Fatalf("expected success, got: %v", result.Error)
	}

	pointStep, ok := tappedStep.(*flow.TapOnPointStep)
	if !ok {
		t.Fatalf("expected TapOnPointStep, got %T", tappedStep)
	}
	if pointStep.Repeat != 2 {
		t.Errorf("repeat = %d, want 2", pointStep.Repeat)
	}
}

func TestFlutterDriver_LongPressFallback(t *testing.T) {
	var tappedStep flow.Step

	inner := &mockDriver{
		executeFunc: func(step flow.Step) *core.CommandResult {
			if _, ok := step.(*flow.TapOnPointStep); ok {
				tappedStep = step
				return core.SuccessResult("long pressed", nil)
			}
			return core.ErrorResult(fmt.Errorf("element not found"), "")
		},
	}

	semanticsDump := `SemanticsNode#0
 Rect.fromLTRB(0.0, 0.0, 400.0, 800.0)
 scaled by 1.0x
 ├─SemanticsNode#1
   Rect.fromLTRB(50.0, 100.0, 150.0, 140.0)
   label: "Item"
`

	wsURL, cleanup := startMockVMService(t, semanticsDump)
	defer cleanup()

	client, err := Connect(wsURL)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer client.Close()

	fd := &FlutterDriver{inner: inner, client: client}

	step := &flow.LongPressOnStep{}
	step.Selector.Text = "Item"
	step.StepType = flow.StepLongPressOn

	result := fd.Execute(step)
	if !result.Success {
		t.Fatalf("expected success, got: %v", result.Error)
	}

	pointStep, ok := tappedStep.(*flow.TapOnPointStep)
	if !ok {
		t.Fatalf("expected TapOnPointStep, got %T", tappedStep)
	}
	if !pointStep.LongPress {
		t.Error("expected LongPress = true")
	}
}

func TestFlutterDriver_FindByID(t *testing.T) {
	var tappedStep flow.Step
	inner := &mockDriver{
		executeFunc: func(step flow.Step) *core.CommandResult {
			if _, ok := step.(*flow.TapOnPointStep); ok {
				tappedStep = step
				return core.SuccessResult("tapped", nil)
			}
			return core.ErrorResult(fmt.Errorf("element not found"), "")
		},
	}

	semanticsDump := `SemanticsNode#0
 Rect.fromLTRB(0.0, 0.0, 400.0, 800.0)
 scaled by 1.0x
 ├─SemanticsNode#1
   Rect.fromLTRB(10.0, 20.0, 110.0, 70.0)
   identifier: "submit_btn"
   label: "Submit"
`

	wsURL, cleanup := startMockVMService(t, semanticsDump)
	defer cleanup()

	client, err := Connect(wsURL)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer client.Close()

	fd := &FlutterDriver{inner: inner, client: client}

	step := &flow.TapOnStep{}
	step.Selector.ID = "submit_btn"
	step.StepType = flow.StepTapOn

	result := fd.Execute(step)
	if !result.Success {
		t.Fatalf("expected success finding by ID, got: %v", result.Error)
	}

	// Verify it tapped at the center of Rect(10, 20, 110, 70) = (60, 45)
	pointStep, ok := tappedStep.(*flow.TapOnPointStep)
	if !ok {
		t.Fatalf("expected TapOnPointStep, got %T", tappedStep)
	}
	if pointStep.X != 60 || pointStep.Y != 45 {
		t.Errorf("tap point = (%d, %d), want (60, 45)", pointStep.X, pointStep.Y)
	}
}

func TestFlutterDriver_PassThrough_Screenshot(t *testing.T) {
	inner := &mockDriver{}
	fd := &FlutterDriver{inner: inner}

	data, err := fd.Screenshot()
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if data != nil {
		t.Errorf("expected nil data")
	}
}

func TestFlutterDriver_PassThrough_GetPlatformInfo(t *testing.T) {
	inner := &mockDriver{}
	fd := &FlutterDriver{inner: inner}

	info := fd.GetPlatformInfo()
	if info == nil {
		t.Error("expected non-nil PlatformInfo")
	}
}

func TestFlutterDriver_WidgetTreeFallback_HintText(t *testing.T) {
	// Inner driver can't find by hintText "Enter your email"
	inner := &mockDriver{
		executeFunc: func(step flow.Step) *core.CommandResult {
			return core.ErrorResult(fmt.Errorf("element not found: text=\"Enter your email\""), "")
		},
	}

	// Semantics tree: has TextField with label "Email" but NOT "Enter your email"
	semanticsDump := `SemanticsNode#0
 Rect.fromLTRB(0.0, 0.0, 400.0, 800.0)
 scaled by 2.8x
 ├─SemanticsNode#1
   Rect.fromLTRB(24.0, 213.5, 368.7, 269.5)
   flags: isTextField, hasEnabledState, isEnabled, isFocusable
   label: "Email"
`
	// Widget tree: has the hintText with associated labelText
	widgetTreeDump := `TextField(decoration: InputDecoration(labelText: "Email", hintText: "Enter your email"))
`

	wsURL, cleanup := startMockVMServiceFull(t, semanticsDump, widgetTreeDump)
	defer cleanup()

	client, err := Connect(wsURL)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer client.Close()

	fd := &FlutterDriver{inner: inner, client: client}

	step := &flow.AssertVisibleStep{}
	step.Selector.Text = "Enter your email"
	step.StepType = flow.StepAssertVisible

	result := fd.Execute(step)
	if !result.Success {
		t.Fatalf("expected success via widget tree fallback, got: %v", result.Error)
	}
	if result.Element == nil {
		t.Fatal("expected ElementInfo")
	}
	// Should find the "Email" TextField node via cross-reference
	if result.Element.Text != "Email" {
		t.Errorf("element text = %q, want %q", result.Element.Text, "Email")
	}
}

func TestFlutterDriver_WidgetTreeFallback_Identifier(t *testing.T) {
	// Inner driver can't find by ID "card_subtitle"
	inner := &mockDriver{
		executeFunc: func(step flow.Step) *core.CommandResult {
			return core.ErrorResult(fmt.Errorf("element not found: id=\"card_subtitle\""), "")
		},
	}

	// Semantics tree: has merged label containing "Card Subtitle"
	semanticsDump := `SemanticsNode#0
 Rect.fromLTRB(0.0, 0.0, 400.0, 800.0)
 scaled by 2.8x
 ├─SemanticsNode#1
   Rect.fromLTRB(0.0, 0.0, 360.7, 212.0)
   identifier: "card_title"
   label:
     "Card Title
     Card Subtitle
     A longer description."
`
	// Widget tree: has the individual identifier with Text child
	widgetTreeDump := `Semantics(identifier: "card_subtitle", container: false)
 └Text("Card Subtitle", textAlign: start)
`

	wsURL, cleanup := startMockVMServiceFull(t, semanticsDump, widgetTreeDump)
	defer cleanup()

	client, err := Connect(wsURL)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer client.Close()

	fd := &FlutterDriver{inner: inner, client: client}

	step := &flow.AssertVisibleStep{}
	step.Selector.ID = "card_subtitle"
	step.StepType = flow.StepAssertVisible

	result := fd.Execute(step)
	if !result.Success {
		t.Fatalf("expected success via widget tree fallback, got: %v", result.Error)
	}
	// Should cross-reference "Card Subtitle" text with the merged semantics node
	if result.Element == nil {
		t.Fatal("expected ElementInfo")
	}
}

func TestFlutterDriver_WidgetTreeFallback_NoMatch(t *testing.T) {
	// Inner driver can't find element
	inner := &mockDriver{
		executeFunc: func(step flow.Step) *core.CommandResult {
			return core.ErrorResult(fmt.Errorf("element not found"), "")
		},
	}

	semanticsDump := `SemanticsNode#0
 Rect.fromLTRB(0.0, 0.0, 400.0, 800.0)
 scaled by 1.0x
`
	widgetTreeDump := `MyApp()`

	wsURL, cleanup := startMockVMServiceFull(t, semanticsDump, widgetTreeDump)
	defer cleanup()

	client, err := Connect(wsURL)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer client.Close()

	fd := &FlutterDriver{inner: inner, client: client, findTimeoutMs: 2000}

	step := &flow.AssertVisibleStep{}
	step.Selector.Text = "totally missing element"
	step.StepType = flow.StepAssertVisible

	result := fd.Execute(step)
	if result.Success {
		t.Error("expected failure when element not found anywhere")
	}
}

func TestFlutterDriver_Inner(t *testing.T) {
	inner := &mockDriver{}
	fd := &FlutterDriver{inner: inner}

	got := fd.Inner()
	if got != inner {
		t.Error("Inner() should return the underlying driver")
	}
}

func TestFlutterDriver_Inner_Unwrap(t *testing.T) {
	inner := &mockDriver{}
	fd := &FlutterDriver{inner: inner}

	unwrapped := core.Unwrap(fd)
	if unwrapped != inner {
		t.Error("core.Unwrap on FlutterDriver should return the inner driver")
	}
}

func TestIsElementFindingStep(t *testing.T) {
	tests := []struct {
		step flow.Step
		want bool
	}{
		{&flow.TapOnStep{}, true},
		{&flow.DoubleTapOnStep{}, true},
		{&flow.LongPressOnStep{}, true},
		{&flow.AssertVisibleStep{}, true},
		{&flow.InputTextStep{}, true},
		{&flow.CopyTextFromStep{}, true},
		{&flow.BackStep{}, false},
		{&flow.SwipeStep{}, false},
		{&flow.LaunchAppStep{}, false},
		{&flow.TapOnPointStep{}, false},
	}

	for _, tt := range tests {
		got := isElementFindingStep(tt.step)
		if got != tt.want {
			t.Errorf("isElementFindingStep(%T) = %v, want %v", tt.step, got, tt.want)
		}
	}
}

func TestIsElementNotFoundError(t *testing.T) {
	tests := []struct {
		name   string
		result *core.CommandResult
		want   bool
	}{
		{
			name:   "element not found error",
			result: core.ErrorResult(fmt.Errorf("element not found: text=\"Login\""), ""),
			want:   true,
		},
		{
			name:   "not found in message",
			result: &core.CommandResult{Message: "Element not found: text=\"Login\""},
			want:   true,
		},
		{
			name:   "not visible error",
			result: core.ErrorResult(fmt.Errorf("Element not visible"), ""),
			want:   true,
		},
		{
			name:   "no such element error",
			result: core.ErrorResult(fmt.Errorf("context deadline exceeded: no such element: An element could not be located"), ""),
			want:   true,
		},
		{
			name:   "could not be located in message",
			result: &core.CommandResult{Message: "An element could not be located on the page"},
			want:   true,
		},
		{
			name:   "not visible in message, different error",
			result: &core.CommandResult{Error: fmt.Errorf("context deadline exceeded"), Message: "Element not visible: timeout"},
			want:   true,
		},
		{
			name:   "network error - no fallback",
			result: core.ErrorResult(fmt.Errorf("network timeout"), ""),
			want:   false,
		},
		{
			name:   "nil error and empty message",
			result: &core.CommandResult{},
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isElementNotFoundError(tt.result)
			if got != tt.want {
				t.Errorf("isElementNotFoundError = %v, want %v", got, tt.want)
			}
		})
	}
}

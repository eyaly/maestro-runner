package devicelab

import (
	"fmt"
	"testing"
	"time"

	"github.com/devicelab-dev/maestro-runner/pkg/core"
	"github.com/devicelab-dev/maestro-runner/pkg/flow"
	"github.com/devicelab-dev/maestro-runner/pkg/uiautomator2"
)

// mockDeviceLabClient is a minimal mock for scrollUntilVisible tests.
type mockDeviceLabClient struct {
	sourceFunc     func() (string, error)
	scrollCalls    int
	scrollErr      error
	findClickCalls int
}

func (m *mockDeviceLabClient) FindElement(strategy, selector string) (*uiautomator2.Element, error) {
	return nil, fmt.Errorf("element not found")
}
func (m *mockDeviceLabClient) FindAndClick(strategy, selector string) (*uiautomator2.Element, error) {
	m.findClickCalls++
	return nil, nil
}
func (m *mockDeviceLabClient) ActiveElement() (*uiautomator2.Element, error) { return nil, nil }
func (m *mockDeviceLabClient) SetImplicitWait(timeout time.Duration) error   { return nil }
func (m *mockDeviceLabClient) Click(x, y int) error                          { return nil }
func (m *mockDeviceLabClient) DoubleClick(x, y int) error                    { return nil }
func (m *mockDeviceLabClient) DoubleClickElement(elementID string) error     { return nil }
func (m *mockDeviceLabClient) LongClick(x, y, durationMs int) error          { return nil }
func (m *mockDeviceLabClient) LongClickElement(elementID string, durationMs int) error {
	return nil
}
func (m *mockDeviceLabClient) ScrollInArea(area uiautomator2.RectModel, direction string, percent float64, speed int) error {
	m.scrollCalls++
	return m.scrollErr
}
func (m *mockDeviceLabClient) SwipeInArea(area uiautomator2.RectModel, direction string, percent float64, speed int) error {
	return nil
}
func (m *mockDeviceLabClient) Back() error                       { return nil }
func (m *mockDeviceLabClient) HideKeyboard() error               { return nil }
func (m *mockDeviceLabClient) PressKeyCode(keyCode int) error    { return nil }
func (m *mockDeviceLabClient) SendKeyActions(text string) error  { return nil }
func (m *mockDeviceLabClient) Screenshot() ([]byte, error)       { return nil, nil }
func (m *mockDeviceLabClient) Source() (string, error)           { return m.sourceFunc() }
func (m *mockDeviceLabClient) GetOrientation() (string, error)   { return "PORTRAIT", nil }
func (m *mockDeviceLabClient) SetOrientation(string) error       { return nil }
func (m *mockDeviceLabClient) GetClipboard() (string, error)     { return "", nil }
func (m *mockDeviceLabClient) SetClipboard(string) error         { return nil }
func (m *mockDeviceLabClient) GetDeviceInfo() (*uiautomator2.DeviceInfo, error) {
	return &uiautomator2.DeviceInfo{RealDisplaySize: "1080x2400"}, nil
}
func (m *mockDeviceLabClient) LaunchApp(string, map[string]interface{}) error { return nil }
func (m *mockDeviceLabClient) ForceStop(string) error                         { return nil }
func (m *mockDeviceLabClient) ClearAppData(string) error                      { return nil }
func (m *mockDeviceLabClient) GrantPermissions(string, []string) error        { return nil }
func (m *mockDeviceLabClient) SetAppiumSettings(map[string]interface{}) error { return nil }
func (m *mockDeviceLabClient) WaitForSettle(int, int) (bool, error)          { return true, nil }

// Compile-time check
var _ DeviceLabClient = (*mockDeviceLabClient)(nil)

func TestScrollUntilVisibleRespectsMaxScrolls(t *testing.T) {
	client := &mockDeviceLabClient{
		sourceFunc: func() (string, error) {
			return `<?xml version="1.0" encoding="UTF-8"?>
<hierarchy rotation="0">
  <android.widget.FrameLayout bounds="[0,0][1080,2400]">
    <android.widget.TextView text="Other" bounds="[100,100][300,150]"/>
  </android.widget.FrameLayout>
</hierarchy>`, nil
		},
	}

	driver := New(client, &core.PlatformInfo{ScreenWidth: 1080, ScreenHeight: 2400}, nil)

	step := &flow.ScrollUntilVisibleStep{
		Element:    flow.Selector{Text: "NonExistent"},
		Direction:  "down",
		MaxScrolls: 3,
		BaseStep:   flow.BaseStep{TimeoutMs: 30000},
	}

	result := driver.scrollUntilVisible(step)

	if result.Success {
		t.Error("Expected failure when element not found")
	}
	if client.scrollCalls != 3 {
		t.Errorf("Expected exactly 3 scrolls (maxScrolls=3), got %d", client.scrollCalls)
	}
}

func TestScrollUntilVisibleRespectsTimeout(t *testing.T) {
	client := &mockDeviceLabClient{
		sourceFunc: func() (string, error) {
			return `<?xml version="1.0" encoding="UTF-8"?>
<hierarchy rotation="0">
  <android.widget.FrameLayout bounds="[0,0][1080,2400]">
    <android.widget.TextView text="Other" bounds="[100,100][300,150]"/>
  </android.widget.FrameLayout>
</hierarchy>`, nil
		},
	}

	driver := New(client, &core.PlatformInfo{ScreenWidth: 1080, ScreenHeight: 2400}, nil)

	step := &flow.ScrollUntilVisibleStep{
		Element:   flow.Selector{Text: "NonExistent"},
		Direction: "down",
		BaseStep:  flow.BaseStep{TimeoutMs: 500}, // very short timeout
	}

	result := driver.scrollUntilVisible(step)

	if result.Success {
		t.Error("Expected failure when element not found")
	}
	// With 500ms timeout, should get far fewer than default 20 scrolls
	if client.scrollCalls >= 20 {
		t.Errorf("Expected timeout to limit scrolls (got %d, default max is 20)", client.scrollCalls)
	}
}

func TestScrollUntilVisibleDefaultMaxScrolls(t *testing.T) {
	client := &mockDeviceLabClient{
		sourceFunc: func() (string, error) {
			return `<?xml version="1.0" encoding="UTF-8"?>
<hierarchy rotation="0">
  <android.widget.FrameLayout bounds="[0,0][1080,2400]">
    <android.widget.TextView text="Other" bounds="[100,100][300,150]"/>
  </android.widget.FrameLayout>
</hierarchy>`, nil
		},
	}

	driver := New(client, &core.PlatformInfo{ScreenWidth: 1080, ScreenHeight: 2400}, nil)

	step := &flow.ScrollUntilVisibleStep{
		Element:   flow.Selector{Text: "NonExistent"},
		Direction: "down",
		BaseStep:  flow.BaseStep{TimeoutMs: 60000}, // long timeout
		// MaxScrolls not set — defaults to 20
	}

	result := driver.scrollUntilVisible(step)

	if result.Success {
		t.Error("Expected failure when element not found")
	}
	if client.scrollCalls != 20 {
		t.Errorf("Expected default 20 scrolls, got %d", client.scrollCalls)
	}
}

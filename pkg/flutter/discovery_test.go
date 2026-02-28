package flutter

import (
	"fmt"
	"testing"
)

type mockDevice struct {
	shellOutput   string
	shellErr      error
	forwardCalled bool
	forwardLocal  int
	forwardRemote int
	forwardErr    error
}

func (m *mockDevice) Shell(cmd string) (string, error) {
	return m.shellOutput, m.shellErr
}

func (m *mockDevice) Forward(localPort, remotePort int) error {
	m.forwardCalled = true
	m.forwardLocal = localPort
	m.forwardRemote = remotePort
	return m.forwardErr
}

func TestDiscoverVMService(t *testing.T) {
	dev := &mockDevice{
		shellOutput: `--------- beginning of main
I/flutter ( 1234): Observatory listening on http://127.0.0.1:12345/abc123/
D/flutter ( 1234): Some debug log
I/flutter ( 1234): The Dart VM service is listening on http://127.0.0.1:54321/xYz789Token/
`,
	}

	wsURL, err := DiscoverVMService(dev, "com.example.app")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if wsURL != "ws://127.0.0.1:54321/xYz789Token/ws" {
		t.Errorf("wsURL = %q, want %q", wsURL, "ws://127.0.0.1:54321/xYz789Token/ws")
	}
	if !dev.forwardCalled {
		t.Error("Forward was not called")
	}
	if dev.forwardLocal != 54321 || dev.forwardRemote != 54321 {
		t.Errorf("Forward(%d, %d), want (54321, 54321)", dev.forwardLocal, dev.forwardRemote)
	}
}

func TestDiscoverVMService_MultipleRestarts(t *testing.T) {
	dev := &mockDevice{
		shellOutput: `I/flutter ( 1000): The Dart VM service is listening on http://127.0.0.1:11111/oldToken/
I/flutter ( 2000): The Dart VM service is listening on http://127.0.0.1:22222/newToken/
`,
	}

	wsURL, err := DiscoverVMService(dev, "com.example.app")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should use the most recent (last) URL
	if wsURL != "ws://127.0.0.1:22222/newToken/ws" {
		t.Errorf("wsURL = %q, want most recent URL", wsURL)
	}
}

func TestDiscoverVMService_NotFlutterApp(t *testing.T) {
	dev := &mockDevice{
		shellOutput: `--------- beginning of main
D/SomeTag ( 1234): No flutter here
`,
	}

	wsURL, err := DiscoverVMService(dev, "com.example.nativeapp")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if wsURL != "" {
		t.Errorf("wsURL = %q, want empty for non-Flutter app", wsURL)
	}
	if dev.forwardCalled {
		t.Error("Forward should not be called for non-Flutter app")
	}
}

func TestDiscoverVMService_EmptyLogcat(t *testing.T) {
	dev := &mockDevice{shellOutput: ""}

	wsURL, err := DiscoverVMService(dev, "com.example.app")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if wsURL != "" {
		t.Errorf("wsURL = %q, want empty", wsURL)
	}
}

func TestDiscoverVMService_ShellError(t *testing.T) {
	dev := &mockDevice{shellErr: fmt.Errorf("adb error")}

	_, err := DiscoverVMService(dev, "com.example.app")
	if err == nil {
		t.Error("expected error when shell fails")
	}
}

func TestDiscoverVMService_ForwardError(t *testing.T) {
	dev := &mockDevice{
		shellOutput: `I/flutter ( 1234): The Dart VM service is listening on http://127.0.0.1:54321/abc123/
`,
		forwardErr: fmt.Errorf("forward failed"),
	}

	_, err := DiscoverVMService(dev, "com.example.app")
	if err == nil {
		t.Error("expected error when forward fails")
	}
}

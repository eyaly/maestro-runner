package flutter

import (
	"fmt"
	"regexp"
	"strings"
)

// DeviceExecutor abstracts device operations needed for Flutter VM Service discovery.
// device.AndroidDevice satisfies this interface.
type DeviceExecutor interface {
	Shell(cmd string) (string, error)
	Forward(localPort, remotePort int) error
}

var reVMService = regexp.MustCompile(`The Dart VM service is listening on http://127\.0\.0\.1:(\d+)/([^\s/]+)/`)

// DiscoverVMService finds the Flutter VM Service URL from device logcat.
// Returns the WebSocket URL (ws://127.0.0.1:<port>/<token>/ws) and sets up adb port forwarding.
// Returns ("", nil) if no Flutter VM Service is found (not a Flutter app).
func DiscoverVMService(dev DeviceExecutor, appID string) (wsURL string, err error) {
	// Read Flutter logcat entries
	out, err := dev.Shell("logcat -d -s flutter")
	if err != nil {
		return "", fmt.Errorf("read logcat: %w", err)
	}

	// Find the most recent VM service URL (app may have restarted)
	var lastPort, lastToken string
	for _, line := range strings.Split(out, "\n") {
		m := reVMService.FindStringSubmatch(line)
		if m != nil {
			lastPort = m[1]
			lastToken = m[2]
		}
	}

	if lastPort == "" {
		return "", nil // Not a Flutter app
	}

	// Parse port
	port := 0
	for _, ch := range lastPort {
		port = port*10 + int(ch-'0')
	}

	// Set up adb port forwarding so host can connect
	if err := dev.Forward(port, port); err != nil {
		return "", fmt.Errorf("adb forward port %d: %w", port, err)
	}

	return fmt.Sprintf("ws://127.0.0.1:%d/%s/ws", port, lastToken), nil
}

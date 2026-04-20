package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/devicelab-dev/maestro-runner/pkg/logger"
)

func init() {
	Register(newSauceLabs)
}

func newSauceLabs(appiumURL string) Provider {
	if !strings.Contains(strings.ToLower(appiumURL), "saucelabs") {
		return nil
	}
	return &sauceLabs{}
}

type sauceLabs struct{}

func (s *sauceLabs) Name() string { return "Sauce Labs" }

func (s *sauceLabs) ExtractMeta(sessionID string, caps map[string]interface{}, meta map[string]string) {
	if capsDeviceNameIndicatesEmuSim(caps) {
		meta["type"] = "vms"
		meta["jobID"] = sessionID
	} else {
		meta["type"] = "rdc"
		meta["jobID"] = jobUUIDFromSessionCaps(caps)
	}
}

// OnRunStart is a no-op — Sauce Labs jobs are created server-side by the
// Appium session, so there's nothing to signal at run start.
func (s *sauceLabs) OnRunStart(meta map[string]string, totalFlows int) error {
	return nil
}

// OnFlowStart is a no-op — Sauce's dashboard renders test cases from the
// final result payload, so there's no per-flow API to ping at flow start.
func (s *sauceLabs) OnFlowStart(meta map[string]string, flowIdx, totalFlows int, name, file string) error {
	return nil
}

// OnFlowEnd is a no-op — Sauce's job result is updated once at run end via
// ReportResult. There's no per-test-case live update API.
func (s *sauceLabs) OnFlowEnd(meta map[string]string, result *FlowResult) error {
	return nil
}

func (s *sauceLabs) ReportResult(appiumURL string, meta map[string]string, result *TestResult) error {
	jobID := strings.TrimSpace(meta["jobID"])
	if jobID == "" {
		return fmt.Errorf("no job ID available")
	}

	apiBase, err := apiBaseFromAppiumURL(appiumURL)
	if err != nil {
		return err
	}

	username, accessKey, err := credentialsFromAppiumURL(appiumURL)
	if err != nil {
		return err
	}

	var endpoint string
	switch meta["type"] {
	case "rdc":
		endpoint = strings.TrimSuffix(apiBase, "/") + "/v1/rdc/jobs/" + url.PathEscape(jobID)
	case "vms":
		endpoint = strings.TrimSuffix(apiBase, "/") + "/rest/v1/" + url.PathEscape(username) + "/jobs/" + url.PathEscape(jobID)
	default:
		return fmt.Errorf("unknown Sauce Labs job type: %s", meta["type"])
	}

	return updateJob(endpoint, username, accessKey, result.Passed)
}

// apiBaseFromAppiumURL returns the Sauce Labs REST API base URL.
// Region is inferred from the Appium hub URL.
func apiBaseFromAppiumURL(appiumURL string) (string, error) {
	raw := strings.TrimSpace(appiumURL)
	if raw == "" {
		return "", fmt.Errorf("empty appium url")
	}
	if _, err := url.Parse(raw); err != nil {
		return "", fmt.Errorf("parse appium url: %w", err)
	}
	lower := strings.ToLower(raw)
	switch {
	case strings.Contains(lower, "eu-central-1"):
		return "https://api.eu-central-1.saucelabs.com", nil
	case strings.Contains(lower, "us-east-4"):
		return "https://api.us-east-4.saucelabs.com", nil
	default:
		return "https://api.us-west-1.saucelabs.com", nil
	}
}

// credentialsFromAppiumURL extracts Sauce Labs credentials from the URL userinfo
// or falls back to SAUCE_USERNAME and SAUCE_ACCESS_KEY environment variables.
func credentialsFromAppiumURL(appiumURL string) (username, accessKey string, err error) {
	u, err := url.Parse(strings.TrimSpace(appiumURL))
	if err != nil {
		return "", "", fmt.Errorf("parse appium url: %w", err)
	}
	if u.User != nil {
		username = strings.TrimSpace(u.User.Username())
		if pw, ok := u.User.Password(); ok {
			accessKey = strings.TrimSpace(pw)
		}
	}
	if username != "" && accessKey != "" {
		return username, accessKey, nil
	}
	username = strings.TrimSpace(os.Getenv("SAUCE_USERNAME"))
	accessKey = strings.TrimSpace(os.Getenv("SAUCE_ACCESS_KEY"))
	if username == "" || accessKey == "" {
		return "", "", fmt.Errorf("sauce credentials missing: use https://USERNAME:ACCESS_KEY@... in --appium-url or set SAUCE_USERNAME and SAUCE_ACCESS_KEY")
	}
	return username, accessKey, nil
}

// capsDeviceNameIndicatesEmuSim returns true when any capability key containing
// "deviceName" has a value containing "Emulator" or "Simulator".
func capsDeviceNameIndicatesEmuSim(caps map[string]interface{}) bool {
	if caps == nil {
		return false
	}
	var walk func(map[string]interface{}, int) bool
	walk = func(m map[string]interface{}, depth int) bool {
		if m == nil || depth > 4 {
			return false
		}
		for k, v := range m {
			if strings.Contains(strings.ToLower(k), "devicename") {
				if s, ok := v.(string); ok {
					lower := strings.ToLower(s)
					if strings.Contains(lower, "emulator") || strings.Contains(lower, "simulator") {
						return true
					}
				}
			}
			if sub, ok := v.(map[string]interface{}); ok {
				if walk(sub, depth+1) {
					return true
				}
			}
		}
		return false
	}
	return walk(caps, 0)
}

// jobUUIDFromSessionCaps reads the Sauce Labs RDC job UUID from session capabilities.
func jobUUIDFromSessionCaps(caps map[string]interface{}) string {
	if caps == nil {
		return ""
	}
	for _, key := range []string{"appium:jobUuid", "jobUuid"} {
		if s, ok := caps[key].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// updateJob sends a PUT request with {"passed": bool} to the given endpoint.
func updateJob(endpoint, username, accessKey string, passed bool) error {
	payload, err := json.Marshal(map[string]bool{"passed": passed})
	if err != nil {
		return fmt.Errorf("marshal body: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.SetBasicAuth(username, accessKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("http put: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			logger.Debug("sauce labs: close response body: %v", err)
		}
	}()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("sauce labs api %s: status %d, body: %s", endpoint, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

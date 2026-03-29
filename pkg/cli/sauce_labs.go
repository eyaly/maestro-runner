package cli

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

// sauceLabsAPIBaseFromAppiumURL returns the Sauce Labs REST API base URL for a given Appium hub URL.
// Used for RDC and other regional API calls (emulators/simulators may share the same regional hosts).
// Region is inferred from substrings in the full URL.
//
// Rules when the URL contains "saucelabs":
//   - "eu-central-1" -> https://api.eu-central-1.saucelabs.com
//   - "us-east-4"    -> https://api.us-east-4.saucelabs.com
//   - else           -> https://api.us-west-1.saucelabs.com
//
// Real Device job updates: https://docs.saucelabs.com/dev/api/rdc/#update-a-job
func sauceLabsAPIBaseFromAppiumURL(appiumURL string) (string, error) {
	raw := strings.TrimSpace(appiumURL)
	if raw == "" {
		return "", fmt.Errorf("empty appium url")
	}
	if _, err := url.Parse(raw); err != nil {
		return "", fmt.Errorf("parse appium url: %w", err)
	}
	lower := strings.ToLower(raw)
	if !strings.Contains(lower, "saucelabs") {
		return "", fmt.Errorf("not a Sauce Labs appium url")
	}
	switch {
	case strings.Contains(lower, "eu-central-1"):
		return "https://api.eu-central-1.saucelabs.com", nil
	case strings.Contains(lower, "us-east-4"):
		return "https://api.us-east-4.saucelabs.com", nil
	default:
		return "https://api.us-west-1.saucelabs.com", nil
	}
}

// sauceCredentialsFromAppiumURL returns the Sauce username and access key for REST API basic auth.
//
// Primary source is the Appium hub URL userinfo (same credentials Appium uses), for example:
//
//	https://<SAUCE_USERNAME>:<SAUCE_ACCESS_KEY>@ondemand.eu-central-1.saucelabs.com:443/wd/hub
//
// net/url decodes percent-encoding in the userinfo (needed if the access key contains reserved characters).
// If either field is missing from the URL, falls back to SAUCE_USERNAME and SAUCE_ACCESS_KEY.
func sauceCredentialsFromAppiumURL(appiumURL string) (username, accessKey string, err error) {
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
		return "", "", fmt.Errorf("Sauce credentials missing: use https://USERNAME:ACCESS_KEY@... in --appium-url or set SAUCE_USERNAME and SAUCE_ACCESS_KEY")
	}
	return username, accessKey, nil
}

// updateSauceLabsRDCJobPassed calls PUT /v1/rdc/jobs/{job_id} with {"passed": true|false}.
// job_id is the value from capability appium:jobUuid for applicable Appium sessions on Sauce Labs.
// See https://docs.saucelabs.com/dev/api/rdc/#update-a-job
func updateSauceLabsRDCJobPassed(appiumURL, jobUUID string, passed bool) error {
	jobUUID = strings.TrimSpace(jobUUID)
	if jobUUID == "" {
		return fmt.Errorf("empty job id")
	}
	base, err := sauceLabsAPIBaseFromAppiumURL(appiumURL)
	if err != nil {
		return err
	}
	user, key, err := sauceCredentialsFromAppiumURL(appiumURL)
	if err != nil {
		return err
	}
	endpoint := strings.TrimSuffix(base, "/") + "/v1/rdc/jobs/" + url.PathEscape(jobUUID)
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
	req.SetBasicAuth(user, key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 31 * time.Second}
	resp, err := client.Do(req)
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
		return fmt.Errorf("sauce labs rdc api %s: status %d, body: %s", endpoint, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

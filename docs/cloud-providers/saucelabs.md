# Sauce Labs (Appium)

Use Appium driver mode with a Sauce Labs URL and provider capabilities.

## Run command

```bash
maestro-runner \
  --driver appium \
  --appium-url "https://$SAUCE_USERNAME:$SAUCE_ACCESS_KEY@ondemand.us-west-1.saucelabs.com:443/wd/hub" \
  --caps provider-caps.json \
  test flows/
```

- Default example uses `us-west-1`. Replace the Sauce Labs endpoints with your region as needed (for example `eu-central-1`, `us-east-4`).
- The Appium URL should include Sauce credentials (`$SAUCE_USERNAME` and `$SAUCE_ACCESS_KEY`) or be provided via environment variables.

## Example capabilities

Example `provider-caps.json` for Android real device:

```json
{
  "platformName": "Android",
  "appium:automationName": "UiAutomator2",
  "appium:deviceName": "Samsung.*",
  "appium:platformVersion": "^1[5-6].*",
  "appium:app": "storage:filename=mda-2.2.0-25.apk",
  "sauce:options": {
    "build": "Maestro Android Run",
    "appiumVersion": "latest"
  }
}
```

Example `provider-caps.json` for iOS real device:

```json
{
  "platformName": "iOS",
  "appium:automationName": "XCUITest",
  "appium:deviceName": "iPhone.*",
  "appium:platformVersion": "^(18|26).*",
  "appium:app": "storage:filename=SauceLabs-Demo-App.ipa",
  "sauce:options": {
    "build": "Maestro iOS Run",
    "appiumVersion": "latest",
    "resigningEnabled": true
  }
}
```

Example `provider-caps.json` for Android emulator:

```json
{
  "platformName": "Android",
  "appium:automationName": "UiAutomator2",
  "appium:deviceName": "Google Pixel 9 Emulator",
  "appium:platformVersion": "16.0",
  "appium:app": "storage:filename=mda-2.2.0-25.apk",
  "sauce:options": {
    "build": "Maestro Android Emulator Run",
    "appiumVersion": "2.11.0"
  }
}
```

Example `provider-caps.json` for iOS simulator:

```json
{
  "platformName": "iOS",
  "appium:automationName": "XCUITest",
  "appium:deviceName": "iPhone Simulator",
  "appium:platformVersion": "17.0",
  "appium:app": "storage:filename=SauceLabs-Demo-App.Simulator.zip",
  "sauce:options": {
    "build": "Maestro iOS Simulator Run",
    "appiumVersion": "2.11.3"
  }
}
```

## References

- [Run Maestro Flows on Any Cloud Provider](https://devicelab.dev/blog/run-maestro-flows-any-cloud)
- [Sauce Labs: Mobile Appium capabilities](https://docs.saucelabs.com/dev/test-configuration-options/#mobile-appium-capabilities)

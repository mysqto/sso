package sso

import (
	"encoding/json"
	"fmt"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/devices"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/mysqto/log"
	"os"
	"path"
	"sso/totp"
	"strings"
	"time"
)

var (
	supportedFormats = map[string]proto.PageCaptureScreenshotFormat{
		"jpeg": proto.PageCaptureScreenshotFormatJpeg,
		"jpg":  proto.PageCaptureScreenshotFormatJpeg,
		"png":  proto.PageCaptureScreenshotFormatPng,
		"webp": proto.PageCaptureScreenshotFormatWebp,
	}
	screenshotDir = "screenshots"
	htmlDir       = "html"
	runs          = 0
)

func run(runFile string) {
	// check if we have a run file, if yes read runs from it
	if _, err := os.Stat(runFile); err == nil {
		data, err := os.ReadFile(runFile)
		if err == nil {
			_, _ = fmt.Sscanf(string(data), "%d", &runs)
		}
	}
	runs++
	_ = os.WriteFile(runFile, []byte(fmt.Sprintf("%d", runs)), 0644)
}

func screenshotFormat(format string) proto.PageCaptureScreenshotFormat {
	if f, ok := supportedFormats[format]; ok {
		return f
	}
	log.Warnf("unsupported format %s, using PNG instead", format)
	return proto.PageCaptureScreenshotFormatPng

}

// cleanup all files in the directory
func cleanupDir(dir string) {
	files, err := os.ReadDir(dir)
	if err != nil {
		log.Warnf("failed to read directory %s: %v", dir, err)
		return
	}
	for _, file := range files {
		err = os.RemoveAll(path.Join(dir, file.Name()))
		if err != nil {
			log.Warnf("failed to remove file %s: %v", file.Name(), err)
		}
	}
}

func savePage(page *rod.Page, file string) {
	if _, err := os.Stat(htmlDir); os.IsNotExist(err) {
		err = os.Mkdir(htmlDir, 0755)
		if err != nil {
			log.Warnf("failed to create directory %s: %v", htmlDir, err)
			return
		}
	}

	content, err := page.HTML()
	if err != nil {
		log.Warnf("failed to get page content: %v", err)
		return
	}

	filePath := path.Join(htmlDir, file)

	err = os.WriteFile(filePath, []byte(content), 0644)
	if err != nil {
		log.Warnf("failed to write page content: %v", err)
	}
}

func screenshot(page *rod.Page, file string) *rod.Page {
	if _, err := os.Stat(screenshotDir); os.IsNotExist(err) {
		err = os.Mkdir(screenshotDir, 0755)
		if err != nil {
			log.Warnf("failed to create directory %s: %v", screenshotDir, err)
			return page
		}
	}

	ext := strings.ToLower(path.Ext(file))

	if len(ext) == 0 {
		ext = "png"
	} else if ext != ".png" {
		log.Warnf("invalid extension %s, using .png instead", ext)
		ext = ".png"
	}

	ext = ext[1:]

	buf, err := page.Screenshot(true, &proto.PageCaptureScreenshot{
		Format: screenshotFormat(ext),
	})

	if err != nil {
		log.Warnf("failed to take screenshot: %v", err)
		return page
	}

	fileName := strings.TrimSuffix(file, "."+ext)
	filePath := path.Join(screenshotDir,
		fmt.Sprintf("%06d_%s_%s.%s", runs,
			fileName,
			time.Now().Format("20060102150405"), ext))

	err = os.WriteFile(filePath, buf, 0644)
	if err != nil {
		log.Warnf("failed to write screenshot: %v", err)
		return page
	}
	log.Debugf("screenshot saved to %s", filePath)

	// Save HTML alongside the screenshot under the same timestamped basename
	// so failures can be inspected page-by-page without re-running the flow.
	htmlPath := path.Join(htmlDir,
		fmt.Sprintf("%06d_%s_%s.html", runs,
			fileName,
			time.Now().Format("20060102150405")))
	if _, statErr := os.Stat(htmlDir); os.IsNotExist(statErr) {
		if mkErr := os.Mkdir(htmlDir, 0755); mkErr != nil {
			log.Warnf("failed to create directory %s: %v", htmlDir, mkErr)
			return page
		}
	}
	if html, herr := page.HTML(); herr == nil {
		if werr := os.WriteFile(htmlPath, []byte(html), 0644); werr != nil {
			log.Warnf("failed to write HTML: %v", werr)
		} else {
			log.Debugf("HTML saved to %s", htmlPath)
		}
	} else {
		log.Warnf("failed to get HTML: %v", herr)
	}

	return page
}

func browser(args Browser) (browser *rod.Browser, cleanup func()) {
	userDir := args.GetProfileLocation()
	switch args.Mode {
	case "local":
		log.Debugf("running SSO flow, round %d", runs)
		// Default: let rod auto-detect Chrome via launcher's standard search
		// (macOS: /Applications/..., Linux: /usr/bin/google-chrome, etc.).
		// Override via --bin / CHROME_BIN if rod can't find it or you need
		// a specific version (e.g. Chromium, Brave, beta channel).
		lc := launcher.
			New().
			Set("no-default-browser-check").
			Set("no-first-run").
			Set("disable-sync").
			Headless(false).
			UserDataDir(userDir)
		if args.Bin != "" {
			log.Debugf("using explicit chrome binary: %s", args.Bin)
			lc = lc.Bin(args.Bin)
		}

		// User explicitly chose --profile path; preserve its contents
		// (cookies, trusted-device tokens, etc.) so subsequent runs can
		// reuse the session and skip 2FA. lc.Cleanup would rm -rf the
		// userDataDir on exit — bind a no-op instead.
		cleanup = func() {}
		browser = rod.New().ControlURL(lc.MustLaunch()).Timeout(args.Timeout).MustConnect()
	case "rod-managed":
		lc := launcher.
			MustNewManaged(args.RemoteURL).
			Headless(false).
			Set("auto-open-devtools-for-tabs").
			Delete("disable-background-networking").
			Delete("disable-background-timer-throttling").
			Delete("disable-backgrounding-occluded-windows").
			Delete("disable-breakpad").
			Delete("disable-client-side-phishing-detection").
			Delete("disable-component-extensions-with-background-pages").
			Delete("disable-default-apps").
			Delete("disable-dev-shm-usage").
			Delete("disable-extensions").
			Delete("disable-features").
			Delete("disable-hang-monitor").
			Delete("disable-http2").
			Delete("disable-ipc-flooding-protection").
			Delete("disable-popup-blocking").
			Delete("disable-prompt-on-repost").
			Delete("disable-renderer-backgrounding").
			Delete("disable-site-isolation-trials").
			Delete("disable-sync").
			Delete("enable-automation").
			Delete("enable-features").
			Delete("force-color-profile").
			Delete("metrics-recording-only").
			Delete("no-first-run").
			Delete("use-mock-keychain").
			XVFB("--server-num=5", "--server-args=-screen 0 1512x982x16")

		browser = rod.
			New().
			Client(lc.MustClient()).
			Timeout(3 * time.Minute).
			MustConnect()

	case "browserless-v1":
		launchArgs := []string{
			"--no-default-browser-check",
			"--no-first-run",
			"stealth=true",
			"ignoreDefaultArgs=true",
			"blockAds=true",
			"--disable-gpu",
			"dumpio=true",
		}
		if len(userDir) > 0 {
			launchArgs = append(launchArgs, "userDataDir="+userDir)
		}
		wsURL := args.RemoteURL + "?" + strings.Join(launchArgs, "&")
		log.Debugf("connecting to %s", wsURL)
		browser = rod.New().ControlURL(wsURL).Timeout(args.Timeout).MustConnect()

	case "browserless-v2":
		launchArgs := map[string]any{
			"stealth":  true,
			"blockAds": true,
			"args": []string{
				"--no-default-browser-check",
				"--no-first-run",
				"--disable-gpu",
			},
		}
		if len(userDir) > 0 {
			launchArgs["userDataDir"] = userDir
		}
		argsBytes, _ := json.Marshal(launchArgs)
		sep := "?"
		if strings.Contains(args.RemoteURL, "?") {
			sep = "&"
		}
		encodedLaunch := strings.ReplaceAll(string(argsBytes), " ", "")
		wsURL := args.RemoteURL + sep + "launch=" + encodedLaunch
		log.Debugf("connecting to %s", wsURL)
		browser = rod.New().ControlURL(wsURL).Timeout(args.Timeout).MustConnect()
	}
	return
}

func Auth(args Args) {
	run(args.RunFilePath())
	if args.Browser.ScreenshotPath != "" {
		screenshotDir = args.Browser.ScreenshotPath
		htmlDir = args.Browser.ScreenshotPath
	}
	cleanupDir(screenshotDir)
	browser, cleanup := browser(args.Browser)
	if cleanup != nil {
		defer cleanup()
	}
	defer browser.MustClose()

	var samlDone chan struct{}
	if args.Login.SAMLOutput != "" {
		var stop func()
		samlDone, stop = captureSAML(browser, args.Login.SAMLOutput, args.Login.SAMLSkip)
		defer stop()
	}

	targetURL := args.Login.URL
	page := browser.MustPage("")
	page.MustEmulate(devices.Device{
		UserAgent:      args.Browser.GetUserAgent(),
		AcceptLanguage: "en-US",
		Screen: devices.Screen{
			DevicePixelRatio: 2,
			Horizontal: devices.ScreenSize{
				Width:  1512,
				Height: 982,
			},
			Vertical: devices.ScreenSize{
				Width:  982,
				Height: 1512,
			},
		},
		Title: "MacBook Pro 14-inch, 2023",
	})
	log.Debugf("opening SSO page %s", targetURL)
	page.MustNavigate(targetURL)
	if err := page.WaitLoad(); err != nil {
		log.Debugf("WaitLoad returned error (likely page redirect): %v — continuing", err)
	}
	log.Debugf("waiting 15 seconds for the page to load")
	if waitOrSAML(samlDone, 15*time.Second) {
		log.Debugf("SAML response captured before any interaction was required")
		return
	}
	screenshot(page, `sso_page_after_15_seconds.png`)
	savePage(page, "sso_page_after_15_seconds.html")

	log.Debugf("checking the 'Confirm and continue' button")
	if hasConfirmAndContinue, _, _ := page.HasX(`//*[@id="cli_verification_btn"]`); hasConfirmAndContinue {
		log.Debugf("Confirm and continue button found, clicking on it")
		_ = page.MustElementX(`//*[@id="cli_verification_btn"]`).
			MustClick()
		log.Debugf("clicked on the 'Confirm and continue' button")
		screenshot(page, `sso_page_continue.png`)
		log.Debugf("waiting for 5 seconds")
		time.Sleep(10 * time.Second)
		screenshot(page, `sso_page_after_continue.png`)
		savePage(page, "sso_page_after_continue.html")
	}

	if hasErr, errE, _ := page.HasX(`//*[@id="alertFrame"]`); hasErr {
		screenshot(page, `sso_page_error.png`)
		err := errE.MustElementX(`//p[@class='alert-content']`).MustText()
		log.Fatalf("failed to login: %s", err)
	}

	log.Debugf("waiting for the Allow/Google login page to load")
	screenshot(page, `google_login_page.png`)

	// check do we have the allow button
	if hasAllow, _, _ := page.HasX(`//span[contains(text(), 'Allow')]/ancestor::button[1]`); hasAllow {
		log.Debugf("Allow button found, clicking on it")
		allow(page, samlDone)
		return
	}

	email := args.Login.Email
	var emailAttribute *string
	var emailValue string

	// check do we have `Verify it’s you` page
	if hasVerifyItsYou, _, _ := page.HasX(`//span[contains(text(), 'Verify it’s you')]`); hasVerifyItsYou {
		log.Debugf("`Verify it’s you` page found, will try to find the next button")
		// check do we have the `Next` button
		if hasNext, _, _ := page.HasX(`//span[contains(text(), 'Next')]/ancestor::button[1]`); hasNext {
			log.Debugf("Next button found, clicking on it")
			page.MustElementX(`//span[contains(text(), 'Next')]/ancestor::button[1]`).
				MustClick()
			log.Debugf("clicked on the 'Next' button")
			goto inputPassword
		}
	}

	// check do we have the `Choose an account` page
	if hasChooseAccount, _, _ := page.HasX(`//span[contains(text(), 'Choose an account')]`); hasChooseAccount {
		log.Debugf("Choose an account page found, will try to find the email address")
		// get the email address
		emailAlreadyLoggedIn, accountElement, _ := page.HasX(`//div[@data-identifier='` + email + `']`)
		if emailAlreadyLoggedIn {
			log.Debugf("email address %s already logged out, will choose it", args.Login.Email)
			accountElement.MustClick()

			// next will go to the password page
			goto inputPassword
		}

		log.Debugf("email address %s not found, will try to find the 'Use another account' button", args.Login.Email)
		// check do we have the `Use another account` button
		if hasUseAnotherAccount, anotherAccountElement, _ := page.HasX(`//div[contains(text(), 'Use another account')]`); hasUseAnotherAccount {
			log.Debugf("Use another account button found, clicking on it")
			anotherAccountElement.MustClick()
			log.Debugf("clicked on the 'Use another account' button")
		}
	}

	// wait for the Google login page to load
	_ = page.MustElementX(`//*[@id="identifierId"]`).
		WaitVisible()
	log.Debugf("Google login page loaded, starting login")

	screenshot(page, `google_login_email_page.png`)
	log.Debugf("filling in the email address")
	page.MustElementX(`//*[@id="identifierId"]`).
		MustInput(email).
		MustType()

	// get the input value
	emailAttribute = page.MustElementX(`//*[@id="identifierId"]`).MustAttribute("data-initial-value")
	if emailAttribute != nil {
		emailValue = *emailAttribute
	}
	if emailValue == email {
		log.Debugf("email address correctly filled")
	} else {
		log.Warnf("email address not correctly filled, expected %s, got %s", email, emailValue)
		log.Debugf("retrying to fill in the email address")
		page.MustElementX(`//*[@id="identifierId"]`).
			MustSelectAllText().
			MustInput("").
			MustInput(email).
			MustType()
	}
	screenshot(page, `google_login_email_page_filled.png`)
	log.Debugf("email address filled")

	log.Debugf("clicking on the 'Next' button")
	page.MustElementX(`//span[contains(text(), 'Next')]/ancestor::button[1]`).
		MustClick().
		MustType(input.Enter)
	log.Debugf("clicked on the 'Next' button")
	screenshot(page, `google_login_email_page_next.png`)
inputPassword:
	log.Debugf("waiting for 6 seconds")
	time.Sleep(6 * time.Second)
	screenshot(page, `google_login_email_page_next_after_wait.png`)
	// wait for the Google password page to load
	savePage(page, "google_login_email_page_next_after_wait.html")

	// check if `passkey` page is loaded
	checkAndBypassPasskey(page)
	checkAndBypassPasskey(page)

	// check do we have the `Choose how you want to sign in` page
	if hasChooseHowYouWantToSignIn, _, _ := page.HasX(`//span[contains(text(), 'Choose how you want to sign in')]`); hasChooseHowYouWantToSignIn {
		log.Debugf("Choose how you want to sign in page found, will try to find the `Enter your password` option")
		if hasPasswordOption, _, _ := page.HasX(`//div[contains(text(), 'Enter your password')]`); hasPasswordOption {
			log.Debugf("Password option found, clicking on it")
			page.MustElementX(`//div[contains(text(), 'Enter your password')]//parent::div`).
				MustClick()
			log.Debugf("clicked on the 'Enter your password' option")
			log.Debugf("waiting for 5 seconds")
			time.Sleep(5 * time.Second)
		}
	}

	log.Debugf("waiting for the Google Password page to load")
	_ = page.MustElementX(`//input[@type="password"]`).
		WaitVisible()
	log.Debugf("Google Password page loaded")

	screenshot(page, `google_login_password_page.png`)

	// fill in the password
	log.Debugf("filling in the Password")
	password := args.Login.Password
	page.MustElementX(`//input[@type="password"]`).
		MustInput(password)
	screenshot(page, `google_login_password_page_filled.png`)
	log.Debugf("Password filled")

	// click on the "Next" button
	log.Debugf("clicking on the 'Next' button")
	_ = page.MustElementX(`//span[contains(text(), 'Next')]/ancestor::button[1]`).
		MustClick()
	screenshot(page, `google_login_pasword_page_next.png`)
	log.Debugf("clicked on the 'Next' button")

	// sleep for 6 seconds
	log.Debugf("waiting for 5 seconds")
	time.Sleep(5 * time.Second)
	// check do we have the allow button
	if hasAllow, _, _ := page.HasX(`//span[contains(text(), 'Allow')]/ancestor::button[1]`); hasAllow {
		log.Debugf("Allow button found, clicking on it")
		allow(page, samlDone)
		return
	}

	// wait for the 2FA page to load
	log.Debugf("waiting for the 2FA page to load")
	page.MustElementX(`//span[contains(text(), '2-Step Verification')]`)
	screenshot(page, `google_login_2fa_page.png`)
	log.Debugf("2FA page loaded")

	// check if we have the "Try another way" button
	if hasTryAnotherWay, _, _ := page.HasX(`//span[contains(text(), 'Try another way')]/ancestor::button[1]`); hasTryAnotherWay {
		// Click "Try another way". Google's jsaction sometimes ignores rod's
		// native click on this card; fall through to JS .click() then a
		// keyboard Enter on the focused button if the page doesn't navigate.
		clickTryAnotherWay(page)
	}

	// snapshot the verification-options page before clicking; Google rewords
	// these options occasionally and these artifacts let us adapt without
	// re-running the full flow blind.
	screenshot(page, `google_login_2fa_options.png`)
	savePage(page, "google_login_2fa_options.html")

	// Bail out before generating an OTP if the account is already locked out.
	// This is the check that stops the bleeding: once Google shows "Too many
	// failed attempts" it hides the Authenticator option entirely, and every
	// further attempt from here would spend another 2FA submission for nothing.
	checkAuthActionRequired(page)

	// click the "Authenticator app" option. Google has used several wordings;
	// try each known variant and log the actual page on failure.
	log.Debugf("clicking on the Authenticator app option")
	authenticatorXPaths := []string{
		`//div[contains(text(), 'Get a verification code from the')]//parent::div`,
		`//div[contains(text(), 'Google Authenticator')]//ancestor::li[1]`,
		`//div[contains(text(), 'Authenticator app')]//ancestor::li[1]`,
		`//li[.//div[contains(text(), 'Authenticator')]]`,
		`//div[@role='link' and .//*[contains(text(), 'Authenticator')]]`,
	}
	clicked := false
	for _, xpath := range authenticatorXPaths {
		if has, el, _ := page.HasX(xpath); has && el != nil {
			el.MustClick()
			log.Debugf("clicked Authenticator option via xpath: %s", xpath)
			screenshot(page, `google_login_2fa_after_authenticator_click.png`)
			clicked = true
			break
		}
	}
	if !clicked {
		screenshot(page, `google_login_2fa_options_no_match.png`)
		log.Fatalf("could not find Authenticator-app verification option; see screenshots/ + html/ dumps for the page content")
	}

	// fill in the OTP code
	log.Debugf("filling in the OTP code")
	optSecret := args.Login.TOTPSecret
	otpCode := totp.TOTP(optSecret)
	page.MustElementX(`//input[@type="tel"]`).
		MustInput(otpCode).
		MustType()
	log.Debugf("OTP code filled")
	screenshot(page, `google_login_2fa_page_filled.png`)

	// click on the "Next" button
	log.Debugf("clicking on the 'Next' button")
	page.MustElementX(`//*[@id="totpNext"]`).
		MustClick()
	log.Debugf("clicked on the 'Next' button")
	screenshot(page, `google_login_2fa_after_otp_next.png`)

	// allow the SSO page
	allow(page, samlDone)
}

// Exit codes for auth states that no amount of retrying can get past. Callers
// (the credential server and its consumers) branch on these to back off instead
// of looping — each retry costs a 2FA submission on the Google account, and
// enough of them get the account rate-limited for hours.
const (
	ExitPasswordChangeRequired = 10 // Google demands a new password; needs a human
	ExitAccountRateLimited     = 11 // 2FA locked after too many failed attempts
)

// checkAuthActionRequired exits with a distinct, non-retryable code when Google
// is showing a page the automation can never get past. Callers invoke this only
// after the screenshot/HTML dumps have been written, so the artifacts that make
// a new interstitial diagnosable are always preserved.
func checkAuthActionRequired(page *rod.Page) {
	for _, marker := range []string{
		`//*[contains(text(), 'Create a strong password')]`,
		`//*[contains(text(), 'Create a new, strong password')]`,
		`//*[contains(text(), 'Change your password')]`,
		`//*[contains(text(), 'Update your password')]`,
	} {
		if has, _, _ := page.HasX(marker); has {
			screenshot(page, `password_change_required.png`)
			savePage(page, "password_change_required.html")
			log.Errorf("Google requires a password change for this account (%s); "+
				"reset the password manually and update the configured password. "+
				"Retrying cannot succeed.", marker)
			os.Exit(ExitPasswordChangeRequired)
		}
	}

	if has, _, _ := page.HasX(`//*[contains(text(), 'Too many failed attempts')]`); has {
		screenshot(page, `account_rate_limited.png`)
		savePage(page, "account_rate_limited.html")
		log.Errorf("Google has rate-limited 2FA on this account after too many " +
			"failed attempts; logins will keep failing for several hours")
		os.Exit(ExitAccountRateLimited)
	}
}

func allow(page *rod.Page, samlDone chan struct{}) {
	// wait for the SSO page to load
	log.Debugf("waiting for 10 seconds for the SSO page to load")
	if waitOrSAML(samlDone, 10*time.Second) {
		log.Debugf("SAML response captured before clicking 'Allow'")
		return
	}
	// Some Chrome builds (e.g. macOS) show a 'Simplify your sign-in' /
	// passkey-create interstitial after OTP. It's optional and not always
	// rendered, so probe briefly and dismiss only if present.
	dismissPasskeyPrompt(page)
	checkConfirmAndContinue(page)
	screenshot(page, `sso_page_after_login.png`)
	savePage(page, "sso_page_after_login.html")

	// Landing here on anything other than the consent screen used to fall
	// through to a generic "'Allow' button not found", which reads as transient
	// and invites an immediate retry. Classify the known-permanent states first.
	checkAuthActionRequired(page)

	if hasAllow, allowBtn, _ := page.HasX(`//span[contains(text(), 'Allow')]/ancestor::button[1]`); hasAllow {
		allowBtn.MustClick()
		log.Debugf("SSO page loaded, clicked on the 'Allow' button")
	} else if samlDone == nil {
		log.Fatalf("'Allow' button not found")
	} else {
		log.Debugf("'Allow' button not present; waiting for SAML response")
	}

	if samlDone != nil {
		log.Debugf("waiting for SAML response capture (up to 60s)")
		select {
		case <-samlDone:
			log.Debugf("SAML response captured")
		case <-time.After(60 * time.Second):
			log.Fatalf("timed out waiting for SAML response")
		}
		return
	}

	// wait for the `Request Approved` page to load
	log.Debugf("waiting for the 'Request Approved' page to load")
	page.MustElementX(`//div[contains(text(), 'Request approved')]`)
	log.Debugf("Request Approved page loaded")

	log.Debugln("Request approved")
	screenshot(page, `sso_request_approved.png`)
	page.MustClose()
}

// waitOrSAML waits for the given duration unless samlDone fires first.
// Returns true if samlDone fired (caller should bail out of the flow).
func waitOrSAML(samlDone chan struct{}, d time.Duration) bool {
	if samlDone == nil {
		time.Sleep(d)
		return false
	}
	select {
	case <-samlDone:
		return true
	case <-time.After(d):
		return false
	}
}

// clickTryAnotherWay clicks the "Try another way" button on the 2FA page
// and verifies the page actually navigated. Google's jsaction framework
// sometimes ignores rod's native MustClick on this card, so we escalate
// through three click strategies before giving up. Each strategy is
// wrapped in a per-attempt timeout so a hanging click can't stall the
// whole flow.
func clickTryAnotherWay(page *rod.Page) {
	xpath := `//span[contains(text(), 'Try another way')]/ancestor::button[1]`
	stillOnApproval := func() bool {
		has1, _, _ := page.HasX(`//*[contains(text(), 'Open the Gmail app')]`)
		has2, _, _ := page.HasX(`//*[contains(text(), "Don") and contains(text(), "ask again on this device")]`)
		return has1 || has2
	}

	// runWithTimeout invokes do() in a goroutine and returns true if it
	// completed, false on timeout. Recovers from rod panics.
	runWithTimeout := func(name string, timeout time.Duration, do func()) bool {
		done := make(chan struct{})
		go func() {
			defer func() {
				if r := recover(); r != nil {
					log.Warnf("strategy %s panicked: %v", name, r)
				}
				close(done)
			}()
			do()
		}()
		select {
		case <-done:
			return true
		case <-time.After(timeout):
			log.Warnf("strategy %s timed out after %v", name, timeout)
			return false
		}
	}

	tryStrategy := func(name string, do func()) bool {
		log.Debugf("clicking 'Try another way' via %s", name)
		if !runWithTimeout(name, 6*time.Second, do) {
			return false
		}
		screenshot(page, fmt.Sprintf(`tay_after_%s.png`, name))
		log.Debugf("waiting for 5 seconds after %s", name)
		time.Sleep(5 * time.Second)
		if !stillOnApproval() {
			log.Debugf("'Try another way' navigated away via %s", name)
			return true
		}
		log.Warnf("page still on phone-approval after %s — escalating", name)
		return false
	}

	// Strategy 1: rod's native MustClick (mouse press/release via CDP).
	if tryStrategy("native_click", func() {
		page.MustElementX(xpath).MustClick()
	}) {
		return
	}

	// Strategy 2: JavaScript element.click() — fires a synthetic click that
	// most jsaction handlers accept.
	if tryStrategy("js_click", func() {
		btn := page.MustElementX(xpath)
		btn.MustEval(`() => this.click()`)
	}) {
		return
	}

	// Strategy 3: focus the button and press Enter on the keyboard.
	if tryStrategy("focus_enter", func() {
		btn := page.MustElementX(xpath)
		btn.MustFocus()
		page.Keyboard.MustType(input.Enter)
	}) {
		return
	}

	log.Fatalf("'Try another way' click failed across all strategies; see screenshots/ + html/ dumps for the page content")
}

// dismissPasskeyPrompt clicks 'Not now' on Google's passkey-creation
// interstitial ('Simplify your sign-in') if it's shown. Most platforms
// don't render this card, so the function is a no-op when the prompt
// isn't present and never blocks the flow.
func dismissPasskeyPrompt(page *rod.Page) {
	// Probe briefly — page is already loaded by the caller, so a short
	// timeout is enough to detect a rendered prompt without delaying
	// the common case where it isn't.
	stillOnPrompt := func() bool {
		// "Simplify your sign-in" is the canonical title; if the click
		// dismissed the card we won't find it on the next page.
		has, _, _ := page.HasX(`//*[contains(text(), 'Simplify your sign-in')]`)
		return has
	}

	for _, marker := range []string{
		`//*[contains(text(), 'Simplify your sign-in')]`,
		`//*[contains(text(), 'Create a passkey')]`,
		`//*[contains(text(), 'Set up a passkey')]`,
	} {
		if has, _, _ := page.HasX(marker); has {
			screenshot(page, `passkey_prompt_detected.png`)
			log.Debugf("passkey-create prompt detected (%s), clicking 'Not now'", marker)
			// Prefer Google's own data-secondary-action-label hook — it's
			// the most stable selector across Material Design rewrites and
			// guarantees we click the secondary (cancel) action even if the
			// visible label changes capitalisation.
			notNowXPaths := []string{
				`//div[@data-secondary-action-label='Not now']//button[.//span[text()='Not now']]`,
				`//div[@data-secondary-action-label]//button[.//span[contains(text(), 'Not now')]]`,
				`//span[text()='Not now']/ancestor::button[1]`,
				`//span[contains(text(), 'Not now')]/ancestor::button[1]`,
				`//button[.//span[contains(text(), 'Not now')]]`,
			}
			for _, xp := range notNowXPaths {
				if hasBtn, btn, _ := page.HasX(xp); hasBtn && btn != nil {
					btn.MustClick()
					log.Debugf("clicked 'Not now' via %s", xp)
					time.Sleep(3 * time.Second)
					screenshot(page, `passkey_prompt_dismissed.png`)
					if !stillOnPrompt() {
						return
					}
					log.Warnf("'Not now' click via %s did not dismiss the prompt; trying next selector", xp)
				}
			}
			log.Warnf("passkey prompt could not be dismissed via any selector; continuing")
			screenshot(page, `passkey_prompt_no_dismiss_button.png`)
			return
		}
	}
}

func checkAndBypassPasskey(page *rod.Page) {
	// check if `passkey` page is loaded
	hasPasskeySpan, _, _ := page.HasX(`//span[contains(text(), 'passkey')]`)
	hasPasskeyDiv, _, _ := page.HasX(`//div[contains(text(), 'passkey')]`)
	if hasPasskeySpan || hasPasskeyDiv {
		log.Debugf("passkey page found, will try to find the `Try another way` button")
		// check do we have the `Try another way` button
		if hasTryAnotherWay, _, _ := page.HasX(`//span[contains(text(), 'Try another way')]/ancestor::button[1]`); hasTryAnotherWay {
			log.Debugf("Try another way button found, clicking on it")
			page.MustElementX(`//span[contains(text(), 'Try another way')]/ancestor::button[1]`).
				MustClick()
			log.Debugf("clicked on the 'Try another way' button")
		}
		log.Debugf("waiting for 5 seconds")
		time.Sleep(5 * time.Second)
		screenshot(page, `google_login_passkey_bypassed.png`)
		savePage(page, "google_login_selection_after_passkey.html")
	}
}

func checkConfirmAndContinue(page *rod.Page) {
	log.Debugf("checking the 'Confirm and continue' button")
	screenshot(page, `check_confirm_and_continue.png`)
	if hasConfirmAndContinue, _, _ := page.HasX(`//*[@id="cli_verification_btn"]`); hasConfirmAndContinue {
		log.Debugf("Confirm and continue button found, clicking on it")
		_ = page.MustElementX(`//*[@id="cli_verification_btn"]`).
			MustClick()
		log.Debugf("clicked on the 'Confirm and continue' button")
		screenshot(page, `sso_page_continue.png`)
		log.Debugf("waiting for 5 seconds")
		time.Sleep(10 * time.Second)
		screenshot(page, `sso_page_after_continue.png`)
		savePage(page, "sso_page_after_continue.html")
	}
}

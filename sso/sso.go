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
	return page
}

func browser(args Browser) (browser *rod.Browser, cleanup func()) {
	userDir := args.GetProfileLocation()
	switch args.Mode {
	case "local":
		log.Debugf("running SSO flow, round %d", runs)
		lc := launcher.
			NewUserMode().
			Set("no-default-browser-check").
			Set("no-first-run").
			Set("enable-automation").
			Set("disable-web-security").
			Set("allow-running-insecure-content").
			Headless(false).
			Devtools(true).
			Bin("/Applications/Google Chrome.app/Contents/MacOS/Google Chrome")

		if len(userDir) > 0 {
			lc = lc.UserDataDir(userDir)
		}

		cleanup = lc.Cleanup
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
			"--auto-open-devtools-for-tabs",
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
			"stealth":           true,
			"ignoreDefaultArgs": true,
			"blockAds":          true,
			"dumpio":            true,
			"args": []string{
				"--auto-open-devtools-for-tabs",
				"--no-default-browser-check",
				"--no-first-run",
				"--disable-gpu",
			},
		}
		if len(userDir) > 0 {
			launchArgs["userDataDir"] = userDir
		}
		argsBytes, _ := json.Marshal(launchArgs)
		wsURL := args.RemoteURL + "?launch=" + string(argsBytes)
		log.Debugf("connecting to %s", wsURL)
		browser = rod.New().ControlURL(wsURL).Timeout(args.Timeout).MustConnect()
	}
	return
}

func Auth(args Args) {
	defer run(args.RunFilePath())
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
	targetURL := args.Login.URL
	page := browser.MustPage(targetURL)
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
	page = page.
		MustNavigate(targetURL).
		MustReload().
		MustWaitLoad()
	log.Debugf("waiting 15 seconds for the page to load")
	time.Sleep(15 * time.Second)
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
	if hasAllow, _, _ := page.HasX(`//span[contains(text(), 'Allow')]//parent::button`); hasAllow {
		log.Debugf("Allow button found, clicking on it")
		allow(page)
		return
	}

	email := args.Login.Email
	var emailAttribute *string
	var emailValue string

	// check do we have `Verify it’s you` page
	if hasVerifyItsYou, _, _ := page.HasX(`//span[contains(text(), 'Verify it’s you')]`); hasVerifyItsYou {
		log.Debugf("`Verify it’s you` page found, will try to find the next button")
		// check do we have the `Next` button
		if hasNext, _, _ := page.HasX(`//span[contains(text(), 'Next')]//parent::button`); hasNext {
			log.Debugf("Next button found, clicking on it")
			page.MustElementX(`//span[contains(text(), 'Next')]//parent::button`).
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
	page.MustElementX(`//span[contains(text(), 'Next')]/parent::button`).
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
	_ = page.MustElementX(`//span[contains(text(), 'Next')]/parent::button`).
		MustClick()
	screenshot(page, `google_login_pasword_page_next.png`)
	log.Debugf("clicked on the 'Next' button")

	// sleep for 6 seconds
	log.Debugf("waiting for 5 seconds")
	time.Sleep(5 * time.Second)
	// check do we have the allow button
	if hasAllow, _, _ := page.HasX(`//span[contains(text(), 'Allow')]//parent::button`); hasAllow {
		log.Debugf("Allow button found, clicking on it")
		allow(page)
		return
	}

	// wait for the 2FA page to load
	log.Debugf("waiting for the 2FA page to load")
	page.MustElementX(`//span[contains(text(), '2-Step Verification')]`)
	screenshot(page, `google_login_2fa_page.png`)
	log.Debugf("2FA page loaded")

	// check if we have the "Try another way" button
	if hasTryAnotherWay, _, _ := page.HasX(`//span[contains(text(), 'Try another way')]//parent::button`); hasTryAnotherWay {
		// click on the "Try another way" button
		log.Debugf("`Try another way` button found, clicking on it")
		page.MustElementX(`//span[contains(text(), 'Try another way')]/parent::button`).
			MustClick()
		log.Debugf("clicked on the 'Try another way' button")
		log.Debugf("waiting for 5 seconds")
		time.Sleep(5 * time.Second)
	}

	// click on the "Get a verification code from the Google Authenticator app" button
	log.Debugf("clicking on the 'Get a verification code from the Google Authenticator app' button")
	page.MustElementX(`//div[contains(text(), 'Get a verification code from the')]//parent::div`).
		MustClick()
	log.Debugf("clicked on the 'Get a verification code from the Google Authenticator app' button")

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

	// allow the SSO page
	allow(page)
}

func allow(page *rod.Page) {
	// wait for the SSO page to load
	log.Debugf("waiting for 5 seconds for the SSO page to load")
	time.Sleep(10 * time.Second)
	checkConfirmAndContinue(page)
	screenshot(page, `sso_page_after_login.png`)
	savePage(page, "sso_page_after_login.html")
	page.MustElementX(`//span[contains(text(), 'Allow')]//parent::button`).
		MustClick()
	log.Debugf("SSO page loaded, clicked on the 'Allow' button")

	// wait for the `Request Approved` page to load
	log.Debugf("waiting for the 'Request Approved' page to load")
	page.MustElementX(`//div[contains(text(), 'Request approved')]`)
	log.Debugf("Request Approved page loaded")

	log.Debugln("Request approved")
	screenshot(page, `sso_request_approved.png`)
	page.MustClose()
}

func checkAndBypassPasskey(page *rod.Page) {
	// check if `passkey` page is loaded
	hasPasskeySpan, _, _ := page.HasX(`//span[contains(text(), 'passkey')]`)
	hasPasskeyDiv, _, _ := page.HasX(`//div[contains(text(), 'passkey')]`)
	if hasPasskeySpan || hasPasskeyDiv {
		log.Debugf("passkey page found, will try to find the `Try another way` button")
		// check do we have the `Try another way` button
		if hasTryAnotherWay, _, _ := page.HasX(`//span[contains(text(), 'Try another way')]//parent::button`); hasTryAnotherWay {
			log.Debugf("Try another way button found, clicking on it")
			page.MustElementX(`//span[contains(text(), 'Try another way')]//parent::button`).
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

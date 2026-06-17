package sso

import (
	"fmt"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/devices"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
	"github.com/mysqto/log"
	"os"
	"path"
	"sso/totp"
	"strings"
	"time"
)

// BackofficeResult holds the outcome of a backoffice screenshot capture.
type BackofficeResult struct {
	Status string // "OK" or "LOGIN"
	Error  string // non-empty if something went wrong
}

// BackofficeScreenshot navigates to a backoffice booking page (already authenticated
// via persisted browserless session) and captures multi-part 16:9 viewport screenshots.
// Each part is suitable for embedding on a PPT slide.
func BackofficeScreenshot(args BackofficeArgs) BackofficeResult {
	if args.Browser.ScreenshotPath != "" {
		screenshotDir = args.Browser.ScreenshotPath
	}

	browser, cleanup := browser(args.Browser)
	if cleanup != nil {
		defer cleanup()
	}
	defer browser.MustClose()

	page := browser.MustPage("")
	page.MustEmulate(devices.Device{
		UserAgent:      args.Browser.GetUserAgent(),
		AcceptLanguage: "en-US",
		Screen: devices.Screen{
			DevicePixelRatio: 2,
			Horizontal:       devices.ScreenSize{Width: 1440, Height: 810},
			Vertical:         devices.ScreenSize{Width: 810, Height: 1440},
		},
		Title: "Backoffice Screenshot 16:9",
	})

	targetURL := args.URL
	log.Debugf("navigating to %s", targetURL)
	page.MustNavigate(targetURL)
	if err := page.WaitLoad(); err != nil {
		log.Debugf("WaitLoad returned error: %v — continuing", err)
	}
	time.Sleep(5 * time.Second)

	// Check if authenticated — if not, attempt auto-re-login
	currentURL := page.MustInfo().URL
	if strings.Contains(currentURL, "signin") || strings.Contains(currentURL, "accounts.google.com") {
		log.Warnf("session expired — current URL: %s", currentURL)

		if args.Login.Email == "" || args.Login.Password == "" || args.Login.TOTPSecret == "" {
			log.Warnf("no SSO credentials provided — cannot auto-re-login")
			return BackofficeResult{Status: "LOGIN", Error: "no SSO credentials provided"}
		}

		log.Debugf("attempting auto-re-login with %s", args.Login.Email)
		time.Sleep(5 * time.Second) // Wait for login page to fully render

		// Debug: dump what elements are on the page
		debugInfo := page.MustEval(`() => {
			const iframes = Array.from(document.querySelectorAll('iframe')).map(f => f.src || f.id || 'no-src');
			const buttons = Array.from(document.querySelectorAll('button,a,[role="button"]')).map(b => b.outerHTML.substring(0, 200));
			const divs = Array.from(document.querySelectorAll('[id*="g_id"],[class*="g_id"],[id*="google"],[class*="google"],[id*="signin"],[class*="signin"]')).map(d => d.tagName + '#' + d.id + '.' + d.className);
			return JSON.stringify({iframes, buttons: buttons.slice(0,10), divs});
		}`).Str()
		log.Debugf("login page elements: %s", debugInfo)

		// Click the login button — it's an <a class="login-button"> with an image inside
		signInXPaths := []string{
			`//a[contains(@class, 'login-button')]`,
			`//a[contains(@href, '/proxy?redirect=')]`,
			`//*[contains(@id, 'g_id')]//div[@role='button']`,
			`//*[contains(text(), 'Sign in')]`,
		}
		clicked := false
		for _, xpath := range signInXPaths {
			if has, el, _ := page.HasX(xpath); has {
				log.Debugf("found sign-in element: %s", xpath)
				el.MustClick()
				clicked = true
				break
			}
		}

		if !clicked {
			log.Warnf("could not find sign-in button on login page")
			return BackofficeResult{Status: "LOGIN", Error: "could not find sign-in button"}
		}
		log.Debugf("clicked sign-in button, waiting for Google OAuth page")
		time.Sleep(10 * time.Second)
		screenshot(page, "after_signin_click.png")
		afterClickURL := page.MustInfo().URL
		log.Debugf("after sign-in click URL: %s", afterClickURL)

		// Check if OAuth already completed (Google session cached in browser profile)
		if strings.Contains(afterClickURL, "accounts.google.com") {
			if loginErr := googleLogin(page, args.Login); loginErr != nil {
				log.Warnf("auto-re-login: googleLogin failed: %v", loginErr)
				return BackofficeResult{Status: "LOGIN", Error: "auto-re-login failed: " + loginErr.Error()}
			}
		} else {
			log.Debugf("OAuth auto-completed, skipping googleLogin (already on: %s)", afterClickURL)
		}
		// Navigate back to backoffice URL
		page.MustNavigate(targetURL)
		if err := page.WaitLoad(); err != nil {
			log.Debugf("WaitLoad after re-login: %v", err)
		}
		time.Sleep(10 * time.Second)

		// Verify login succeeded
		afterURL := page.MustInfo().URL
		if strings.Contains(afterURL, "signin") || strings.Contains(afterURL, "accounts.google.com") {
			log.Warnf("auto-re-login failed — still on login page: %s", afterURL)
			return BackofficeResult{Status: "LOGIN", Error: "auto-re-login failed"}
		}
		log.Debugf("auto-re-login succeeded, now at: %s", afterURL)
	} else {
		log.Debugf("authenticated: %s", currentURL)
	}

	_ = page.WaitLoad()
	time.Sleep(15 * time.Second)

	// Dismiss any popup (e.g. "Booking Under Dispute" dialog)
	dismissPopup(page)
	time.Sleep(3 * time.Second)

	// Scroll and capture multi-part screenshots
	captureScrollingScreenshots(page)

	// Capture Contact/History/Booking log/Email logs sections as a separate screenshot
	captureContactLogsSection(page)

	return BackofficeResult{Status: "OK"}
}

// googleLogin performs Google OAuth login on the current page using the provided
// credentials. It returns a descriptive error naming the step that failed (rather
// than panicking or silently returning), and screenshots each step so the next
// real failure is diagnosable. Screenshots land in screenshotDir.
func googleLogin(page *rod.Page, login Login) (err error) {
	email := login.Email
	password := login.Password
	step := "start"

	// go-rod Must* methods panic on missing elements. Convert a panic into a
	// descriptive error that names the step we were on, plus a screenshot —
	// instead of bubbling up as a generic "browser panic".
	defer func() {
		if r := recover(); r != nil {
			screenshot(page, "google_login_panic.png")
			err = fmt.Errorf("panicked at step %q: %v", step, r)
		}
	}()

	log.Debugf("googleLogin: starting for %s", email)
	screenshot(page, "google_login_start.png")

	// Handle "Choose an account" page
	step = "choose-account"
	if has, _, _ := page.HasX(`//span[contains(text(), 'Choose an account')]`); has {
		log.Debugf("googleLogin: 'Choose an account' page")
		if hasAcc, el, _ := page.HasX(`//div[@data-identifier='` + email + `']`); hasAcc {
			el.MustClick()
			goto inputPassword
		}
		if hasAnother, el, _ := page.HasX(`//div[contains(text(), 'Use another account')]`); hasAnother {
			el.MustClick()
			time.Sleep(3 * time.Second)
		}
	}

	// Handle "Verify it's you" page
	step = "verify-its-you"
	if has, _, _ := page.HasX(`//span[contains(text(), 'Verify it')]`); has {
		if hasNext, _, _ := page.HasX(`//span[contains(text(), 'Next')]//parent::button`); hasNext {
			page.MustElementX(`//span[contains(text(), 'Next')]//parent::button`).MustClick()
			goto inputPassword
		}
	}

	// Enter email
	step = "email-input"
	if has, _, _ := page.HasX(`//*[@id="identifierId"]`); !has {
		screenshot(page, "google_login_no_email_field.png")
		return fmt.Errorf("email field not found — unexpected Google login layout")
	}
	_ = page.MustElementX(`//*[@id="identifierId"]`).WaitVisible()
	page.MustElementX(`//*[@id="identifierId"]`).MustInput(email).MustType()
	// Verify the email actually landed (mirrors the AWS-SSO flow in sso.go).
	if attr := page.MustElementX(`//*[@id="identifierId"]`).MustAttribute("data-initial-value"); attr == nil || *attr != email {
		log.Warnf("googleLogin: email not filled correctly, retrying")
		page.MustElementX(`//*[@id="identifierId"]`).MustSelectAllText().MustInput("").MustInput(email).MustType()
	}
	screenshot(page, "google_login_email_filled.png")
	step = "email-next"
	page.MustElementX(`//span[contains(text(), 'Next')]/parent::button`).MustClick().MustType(input.Enter)

inputPassword:
	step = "passkey-bypass"
	time.Sleep(6 * time.Second)
	screenshot(page, "google_login_after_email.png")
	checkAndBypassPasskey(page)
	checkAndBypassPasskey(page)

	// Handle "Choose how you want to sign in"
	step = "choose-signin-method"
	if has, _, _ := page.HasX(`//span[contains(text(), 'Choose how you want to sign in')]`); has {
		if hasPw, _, _ := page.HasX(`//div[contains(text(), 'Enter your password')]`); hasPw {
			page.MustElementX(`//div[contains(text(), 'Enter your password')]//parent::div`).MustClick()
			time.Sleep(5 * time.Second)
		}
	}

	// Enter password
	step = "password-input"
	if has, _, _ := page.HasX(`//input[@type="password"]`); !has {
		screenshot(page, "google_login_no_password_field.png")
		return fmt.Errorf("password field not found — flow may have diverged after email")
	}
	_ = page.MustElementX(`//input[@type="password"]`).WaitVisible()
	page.MustElementX(`//input[@type="password"]`).MustInput(password)
	screenshot(page, "google_login_password_filled.png")
	step = "password-next"
	page.MustElementX(`//span[contains(text(), 'Next')]/parent::button`).MustClick()
	time.Sleep(5 * time.Second)
	screenshot(page, "google_login_after_password.png")

	// 2FA?
	step = "2fa-detect"
	if has2FA, _, _ := page.HasX(`//span[contains(text(), '2-Step Verification')]`); !has2FA {
		log.Debugf("googleLogin: no 2FA prompt — login complete")
		return nil
	}
	log.Debugf("googleLogin: 2FA required")
	screenshot(page, "google_login_2fa.png")

	// If Google defaulted to another 2FA method, switch to "Try another way".
	step = "2fa-try-another-way"
	if has, _, _ := page.HasX(`//span[contains(text(), 'Try another way')]//parent::button`); has {
		page.MustElementX(`//span[contains(text(), 'Try another way')]/parent::button`).MustClick()
		time.Sleep(5 * time.Second)
		screenshot(page, "google_login_2fa_options.png")
	}

	// Select the authenticator-app option (if a chooser is shown).
	step = "2fa-select-authenticator"
	if has, _, _ := page.HasX(`//div[contains(text(), 'Get a verification code from the')]//parent::div`); has {
		page.MustElementX(`//div[contains(text(), 'Get a verification code from the')]//parent::div`).MustClick()
		time.Sleep(3 * time.Second)
	} else {
		log.Warnf("googleLogin: authenticator chooser not found — trying TOTP field directly")
	}

	// Enter the TOTP code
	step = "2fa-totp-input"
	if has, _, _ := page.HasX(`//input[@type="tel"]`); !has {
		screenshot(page, "google_login_no_totp_field.png")
		return fmt.Errorf("TOTP input not found — 2FA layout may have changed")
	}
	otpCode := totp.TOTP(login.TOTPSecret)
	page.MustElementX(`//input[@type="tel"]`).MustInput(otpCode).MustType()
	screenshot(page, "google_login_totp_filled.png")
	step = "2fa-totp-submit"
	if has, _, _ := page.HasX(`//*[@id="totpNext"]`); has {
		page.MustElementX(`//*[@id="totpNext"]`).MustClick()
	} else if hasNext, _, _ := page.HasX(`//span[contains(text(), 'Next')]/parent::button`); hasNext {
		page.MustElementX(`//span[contains(text(), 'Next')]/parent::button`).MustClick()
	}
	time.Sleep(6 * time.Second)
	screenshot(page, "google_login_after_totp.png")

	// Verify we actually left the Google login domain.
	step = "verify-complete"
	finalURL := page.MustInfo().URL
	if strings.Contains(finalURL, "accounts.google.com") {
		screenshot(page, "google_login_stuck.png")
		return fmt.Errorf("still on Google login after 2FA (%s) — password or TOTP may be rejected", finalURL)
	}
	log.Debugf("googleLogin: completed for %s, now at %s", email, finalURL)
	return nil
}

// dismissPopup tries to close any modal/popup on the page.
func dismissPopup(page *rod.Page) {
	closeXPaths := []string{
		`//button[contains(@class,'close') or contains(@aria-label,'Close') or contains(@aria-label,'close')]`,
		`//button[contains(text(),'Close') or contains(text(),'close')]`,
		`//span[contains(text(),'Close') or contains(text(),'close')]/parent::button`,
		`//button[contains(@class,'ant-modal-close')]`,
		`//span[contains(@class,'ant-modal-close')]`,
	}
	for _, xpath := range closeXPaths {
		if has, el, _ := page.HasX(xpath); has {
			log.Debugf("dismissing popup via: %s", xpath)
			el.MustClick()
			time.Sleep(2 * time.Second)
			return
		}
	}
}

// captureScrollingScreenshots scrolls through the page's content container
// and captures viewport-sized screenshots at each position.
func captureScrollingScreenshots(page *rod.Page) {
	_ = os.MkdirAll(screenshotDir, 0755)

	// Detect the scrollable container (Ant Design layout)
	scrollInfo := page.MustEval(`() => {
		const el = document.querySelector('.gx-layout-content') ||
			document.querySelector('.ant-layout-content') ||
			document.querySelector('main');
		if (el && el.scrollHeight > el.clientHeight) {
			return {scrollHeight: el.scrollHeight, clientHeight: el.clientHeight, useWindow: false};
		}
		return {scrollHeight: document.documentElement.scrollHeight, clientHeight: window.innerHeight, useWindow: true};
	}`)
	scrollHeight := scrollInfo.Get("scrollHeight").Int()
	clientHeight := scrollInfo.Get("clientHeight").Int()
	useWindow := scrollInfo.Get("useWindow").Bool()
	log.Debugf("scrollHeight: %d, clientHeight: %d, useWindow: %v", scrollHeight, clientHeight, useWindow)

	scrollTo := func(pos int) {
		if useWindow {
			page.MustEval(fmt.Sprintf(`() => window.scrollTo(0, %d)`, pos))
		} else {
			page.MustEval(fmt.Sprintf(`() => {
				const el = document.querySelector('.gx-layout-content') || document.querySelector('.ant-layout-content') || document.querySelector('main');
				if (el) el.scrollTop = %d;
			}`, pos))
		}
	}

	// Scroll to top
	scrollTo(0)
	time.Sleep(500 * time.Millisecond)

	// Scroll by full viewport height — no overlap between screenshots
	scrollStep := clientHeight

	scrollPos := 0
	part := 1
	for scrollPos < scrollHeight {
		scrollTo(scrollPos)
		time.Sleep(1 * time.Second)

		buf, err := page.Screenshot(false, &proto.PageCaptureScreenshot{
			Format: proto.PageCaptureScreenshotFormatPng,
		})
		if err != nil {
			log.Warnf("screenshot part %d failed: %v", part, err)
		} else {
			filePath := path.Join(screenshotDir, fmt.Sprintf("booking_part_%d.png", part))
			_ = os.WriteFile(filePath, buf, 0644)
			log.Debugf("screenshot: %s (scroll=%d/%d)", filePath, scrollPos, scrollHeight)
		}
		part++
		scrollPos += scrollStep
	}
	log.Debugf("captured %d screenshots", part-1)
}

// captureContactLogsSection scrolls to the Contact section and captures everything
// from Contact down to the bottom of the page (Contact, History, Booking logs, Email logs).
func captureContactLogsSection(page *rod.Page) {
	_ = os.MkdirAll(screenshotDir, 0755)

	// Find the Contact section and get positions
	info := page.MustEval(`() => {
		const container = document.querySelector('.gx-layout-content') ||
			document.querySelector('.ant-layout-content') ||
			document.querySelector('main');

		const allElements = document.querySelectorAll('h1,h2,h3,h4,h5,h6,div,span,th,td,label,p');
		for (const el of allElements) {
			const text = (el.textContent || '').trim().toLowerCase();
			if (text === 'contact' || text.startsWith('contact info') || text.startsWith('contact detail')) {
				if (container && container.scrollHeight > container.clientHeight) {
					const rect = el.getBoundingClientRect();
					const contactY = rect.top + container.scrollTop - 10;
					const remaining = container.scrollHeight - contactY;
					return { found: true, contactY: contactY, remaining: remaining, useWindow: false };
				}
				const rect = el.getBoundingClientRect();
				const contactY = rect.top + window.scrollY - 10;
				const remaining = document.documentElement.scrollHeight - contactY;
				return { found: true, contactY: contactY, remaining: remaining, useWindow: true };
			}
		}
		return { found: false, contactY: 0, remaining: 0, useWindow: false };
	}`)

	if !info.Get("found").Bool() {
		log.Debugf("Contact section not found, skipping contact/logs screenshot")
		return
	}

	contactY := info.Get("contactY").Int()
	remaining := info.Get("remaining").Int()
	useWindow := info.Get("useWindow").Bool()
	log.Debugf("Contact section at y=%d, remaining=%d, useWindow=%v", contactY, remaining, useWindow)

	// Scroll to Contact section
	if useWindow {
		page.MustEval(fmt.Sprintf(`() => window.scrollTo(0, %d)`, contactY))
	} else {
		page.MustEval(fmt.Sprintf(`() => {
			const el = document.querySelector('.gx-layout-content') || document.querySelector('.ant-layout-content') || document.querySelector('main');
			if (el) el.scrollTop = %d;
		}`, contactY))
	}
	time.Sleep(2 * time.Second)

	// Capture screenshots scrolling from Contact to the bottom
	clientHeight := page.MustEval(`() => window.innerHeight`).Int()
	scrollPos := contactY
	totalHeight := contactY + remaining
	part := 1

	for scrollPos < totalHeight {
		if useWindow {
			page.MustEval(fmt.Sprintf(`() => window.scrollTo(0, %d)`, scrollPos))
		} else {
			page.MustEval(fmt.Sprintf(`() => {
				const el = document.querySelector('.gx-layout-content') || document.querySelector('.ant-layout-content') || document.querySelector('main');
				if (el) el.scrollTop = %d;
			}`, scrollPos))
		}
		time.Sleep(1 * time.Second)

		buf, err := page.Screenshot(false, &proto.PageCaptureScreenshot{
			Format: proto.PageCaptureScreenshotFormatPng,
		})
		if err != nil {
			log.Warnf("contact_logs part %d failed: %v", part, err)
		} else {
			filePath := path.Join(screenshotDir, fmt.Sprintf("contact_logs_%d.png", part))
			_ = os.WriteFile(filePath, buf, 0644)
			log.Debugf("contact_logs: %s (scroll=%d/%d)", filePath, scrollPos, totalHeight)
		}
		part++
		scrollPos += clientHeight
	}
	log.Debugf("captured %d contact/logs screenshots", part-1)
}

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

// BackofficeScreenshot navigates to a backoffice booking page (already authenticated
// via persisted browserless session) and captures multi-part 16:9 viewport screenshots.
// Each part is suitable for embedding on a PPT slide.
func BackofficeScreenshot(args BackofficeArgs) {
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
			fmt.Println("LOGIN")
			return
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
			fmt.Println("LOGIN")
			return
		}
		log.Debugf("clicked sign-in button, waiting for Google OAuth page")
		time.Sleep(10 * time.Second)
		screenshot(page, "after_signin_click.png")
		log.Debugf("after sign-in click URL: %s", page.MustInfo().URL)

		googleLogin(page, args.Login)
		// After Google login, navigate back to backoffice URL
		page.MustNavigate(targetURL)
		if err := page.WaitLoad(); err != nil {
			log.Debugf("WaitLoad after re-login: %v", err)
		}
		time.Sleep(10 * time.Second)

		// Verify login succeeded
		afterURL := page.MustInfo().URL
		if strings.Contains(afterURL, "signin") || strings.Contains(afterURL, "accounts.google.com") {
			log.Warnf("auto-re-login failed — still on login page: %s", afterURL)
			fmt.Println("LOGIN")
			return
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

	fmt.Println("OK")
}

// googleLogin performs Google OAuth login on the current page using the provided credentials.
func googleLogin(page *rod.Page, login Login) {
	email := login.Email
	password := login.Password
	log.Debugf("starting Google login for %s", email)

	// Handle "Choose an account" page
	if has, _, _ := page.HasX(`//span[contains(text(), 'Choose an account')]`); has {
		if hasAcc, el, _ := page.HasX(`//div[@data-identifier='` + email + `']`); hasAcc {
			el.MustClick()
			goto inputPassword
		}
		if hasAnother, el, _ := page.HasX(`//div[contains(text(), 'Use another account')]`); hasAnother {
			el.MustClick()
		}
	}

	// Handle "Verify it's you" page
	if has, _, _ := page.HasX(`//span[contains(text(), 'Verify it')]`); has {
		if hasNext, _, _ := page.HasX(`//span[contains(text(), 'Next')]//parent::button`); hasNext {
			page.MustElementX(`//span[contains(text(), 'Next')]//parent::button`).MustClick()
			goto inputPassword
		}
	}

	// Enter email
	_ = page.MustElementX(`//*[@id="identifierId"]`).WaitVisible()
	page.MustElementX(`//*[@id="identifierId"]`).MustInput(email).MustType()
	page.MustElementX(`//span[contains(text(), 'Next')]/parent::button`).MustClick().MustType(input.Enter)

inputPassword:
	time.Sleep(6 * time.Second)
	checkAndBypassPasskey(page)
	checkAndBypassPasskey(page)

	// Handle "Choose how you want to sign in"
	if has, _, _ := page.HasX(`//span[contains(text(), 'Choose how you want to sign in')]`); has {
		if hasPw, _, _ := page.HasX(`//div[contains(text(), 'Enter your password')]`); hasPw {
			page.MustElementX(`//div[contains(text(), 'Enter your password')]//parent::div`).MustClick()
			time.Sleep(5 * time.Second)
		}
	}

	// Enter password
	_ = page.MustElementX(`//input[@type="password"]`).WaitVisible()
	page.MustElementX(`//input[@type="password"]`).MustInput(password)
	page.MustElementX(`//span[contains(text(), 'Next')]/parent::button`).MustClick()
	time.Sleep(5 * time.Second)

	// Check if 2FA is needed
	if has2FA, _, _ := page.HasX(`//span[contains(text(), '2-Step Verification')]`); !has2FA {
		log.Debugf("no 2FA required — login complete")
		return
	}

	// 2FA
	if has, _, _ := page.HasX(`//span[contains(text(), 'Try another way')]//parent::button`); has {
		page.MustElementX(`//span[contains(text(), 'Try another way')]/parent::button`).MustClick()
		time.Sleep(5 * time.Second)
	}

	page.MustElementX(`//div[contains(text(), 'Get a verification code from the')]//parent::div`).MustClick()

	otpCode := totp.TOTP(login.TOTPSecret)
	page.MustElementX(`//input[@type="tel"]`).MustInput(otpCode).MustType()
	page.MustElementX(`//*[@id="totpNext"]`).MustClick()
	time.Sleep(5 * time.Second)
	log.Debugf("Google login completed for %s", email)
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

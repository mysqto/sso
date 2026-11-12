package sso

import (
	"net/url"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/mysqto/log"
	"sso/totp"
)

// Looker's own sign-in: an email and password form, then Looker's
// authenticator-code page, then its approval page for the OAuth app. It is
// used for OAuth clients such as looker-cli whose account is a Looker user
// with its own password and authenticator, not the Google SSO account. The
// caller passes that account's email, password and TOTP secret in place of the
// Google ones.
//
// Only in capture mode: the flow ends when the browser is redirected to the
// client's loopback URI, which captureRedirect catches.

const (
	lookerEmailX    = `//input[@id='login-email']`
	lookerPasswordX = `//input[@id='login-password']`
	lookerSubmitX   = `//*[@id='login-submit']`
	lookerRememberX = `//input[@name='remember' and @type='checkbox']`
	// The authenticator-code box: a visible text-like input whose name or id
	// mentions a code or verification, on a Looker page.
	lookerCodeX = `//input[(@type='text' or @type='tel' or @type='number' or not(@type)) and ` +
		`(contains(@name,'code') or contains(@id,'code') or contains(@name,'verification') or ` +
		`contains(@id,'verification') or contains(@name,'token') or contains(@autocomplete,'one-time-code'))]`
	lookerFormSubmitX = `//button[@type='submit'] | //input[@type='submit']`
	// The OAuth approval button. Looker words it more than one way.
	lookerApproveX = `//button[normalize-space(.)='Allow' or normalize-space(.)='Authorize' or ` +
		`normalize-space(.)='Approve' or normalize-space(.)='Accept' or normalize-space(.)='Continue'] | ` +
		`//input[@type='submit' and (@value='Allow' or @value='Authorize' or @value='Approve' or ` +
		`@value='Accept' or @value='Continue')]`
	lookerErrorX = `//*[contains(@class,'alert-danger') or contains(@class,'login-error') or ` +
		`contains(@class,'error-message')][normalize-space(.)!='']`
)

// onLooker reports whether the page is on a Looker instance.
func onLooker(page *rod.Page) bool {
	info, err := page.Info()
	if err != nil {
		return false
	}
	u, err := url.Parse(info.URL)
	if err != nil {
		return false
	}
	return strings.HasSuffix(u.Hostname(), ".looker.com")
}

// lookerFlow drives the Looker pages from wherever the browser landed: the
// login form, the code page, or (with a remembered session) the approval page.
// It returns once the redirect is captured and exits the process when it can't
// get there.
func lookerFlow(page *rod.Page, login Login, captureDone chan struct{}) {
	if hasVisibleX(page, lookerEmailX) {
		lookerPasswordStep(page, login)
		if waitOrCapture(captureDone, 6*time.Second) {
			return
		}
		screenshot(page, `looker_after_password.png`)
		savePage(page, "looker_after_password.html")
		lookerFailIfError(page, "after the password")
	}

	if hasVisibleX(page, lookerCodeX) && !hasVisibleX(page, lookerPasswordX) {
		lookerCodeStep(page, login)
		if waitOrCapture(captureDone, 6*time.Second) {
			return
		}
		screenshot(page, `looker_after_code.png`)
		savePage(page, "looker_after_code.html")
		lookerFailIfError(page, "after the authenticator code")
	}

	for i := 0; i < 3; i++ {
		if !onLooker(page) || !hasVisibleX(page, lookerApproveX) {
			break
		}
		btn := page.MustElementX(lookerApproveX)
		label, _ := btn.Text()
		if label == "" {
			if v, _ := btn.Attribute("value"); v != nil {
				label = *v
			}
		}
		screenshot(page, `looker_oauth_approval.png`)
		savePage(page, "looker_oauth_approval.html")
		btn.MustClick()
		log.Debugf("clicked %q on the Looker approval page", strings.TrimSpace(label))
		if waitOrCapture(captureDone, 8*time.Second) {
			return
		}
	}

	if waitOrCapture(captureDone, 10*time.Second) {
		return
	}
	screenshot(page, `looker_no_redirect.png`)
	savePage(page, "looker_no_redirect.html")
	log.Fatalf("Looker sign-in did not reach the OAuth redirect; see looker_*.png and .html")
}

func lookerPasswordStep(page *rod.Page, login Login) {
	log.Debugf("Looker login form found, filling in the email and password")
	screenshot(page, `looker_login_page.png`)
	page.MustElementX(lookerEmailX).MustSelectAllText().MustInput(login.Email)
	page.MustElementX(lookerPasswordX).MustSelectAllText().MustInput(login.Password)
	// Remembered on the browser profile, a later sign-in skips the password
	// and the code, so each one costs fewer authenticator codes.
	if has, box, _ := page.HasX(lookerRememberX); has {
		_, _ = box.Eval(`() => { if (!this.checked) this.click() }`)
	}
	page.MustElementX(lookerSubmitX).MustClick()
	log.Debugf("submitted the Looker login form")
}

func lookerCodeStep(page *rod.Page, login Login) {
	log.Debugf("Looker authenticator-code page found, filling in the code")
	screenshot(page, `looker_code_page.png`)
	savePage(page, "looker_code_page.html")
	page.MustElementX(lookerCodeX).MustSelectAllText().MustInput(totp.TOTP(login.TOTPSecret))
	if has, btn, _ := page.HasX(lookerFormSubmitX); has {
		btn.MustClick()
	} else {
		page.Keyboard.MustType(input.Enter)
	}
	log.Debugf("submitted the Looker authenticator code")
}

// lookerFailIfError stops on a visible Looker error, such as a wrong password
// or code. Retrying those only spends more codes and risks a lockout.
func lookerFailIfError(page *rod.Page, step string) {
	if !hasVisibleX(page, lookerErrorX) {
		return
	}
	msg, _ := page.MustElementX(lookerErrorX).Text()
	log.Fatalf("Looker sign-in failed %s: %s", step, strings.TrimSpace(msg))
}

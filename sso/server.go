package sso

import (
	"encoding/json"
	"fmt"
	"github.com/go-rod/rod/lib/devices"
	"github.com/mysqto/log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ScreenshotRequest is the JSON body for POST /backoffice-screenshot.
type ScreenshotRequest struct {
	URL            string `json:"url"`
	ScreenshotPath string `json:"screenshotPath"`
	Email          string `json:"email,omitempty"`
	Password       string `json:"password,omitempty"`
	TOTPSecret     string `json:"totpSecret,omitempty"`
}

// ScreenshotResponse is the JSON response for POST /backoffice-screenshot.
type ScreenshotResponse struct {
	Status      string   `json:"status"`
	Error       string   `json:"error,omitempty"`
	Files       []string `json:"files,omitempty"`
	ContactLogs []string `json:"contactLogs,omitempty"`
}

// Serve starts the HTTP server on the given port.
// browserDefaults provides the browser connection settings (mode, remote URL, profile, etc.).
func Serve(port int, browserDefaults Browser) {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})

	mux.HandleFunc("POST /backoffice-screenshot", func(w http.ResponseWriter, r *http.Request) {
		var req ScreenshotRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, ScreenshotResponse{
				Status: "error",
				Error:  "invalid JSON: " + err.Error(),
			})
			return
		}

		if req.URL == "" {
			writeJSON(w, http.StatusBadRequest, ScreenshotResponse{
				Status: "error",
				Error:  "url is required",
			})
			return
		}

		if req.ScreenshotPath == "" {
			writeJSON(w, http.StatusBadRequest, ScreenshotResponse{
				Status: "error",
				Error:  "screenshotPath is required",
			})
			return
		}

		// Ensure screenshot directory exists
		if err := os.MkdirAll(req.ScreenshotPath, 0755); err != nil {
			writeJSON(w, http.StatusInternalServerError, ScreenshotResponse{
				Status: "error",
				Error:  "cannot create screenshot directory: " + err.Error(),
			})
			return
		}

		log.Debugf("POST /backoffice-screenshot url=%s path=%s", req.URL, req.ScreenshotPath)

		profileLoc := browserDefaults.ProfileLocation
		if profileLoc == "" {
			profileLoc = "/data"
		}
		boArgs := BackofficeArgs{
			URL: req.URL,
			Browser: Browser{
				Mode:            browserDefaults.Mode,
				RemoteURL:       browserDefaults.RemoteURL,
				UserAgent:       browserDefaults.UserAgent,
				ScreenshotPath:  req.ScreenshotPath,
				Timeout:         browserDefaults.Timeout,
				ProfileLocation: profileLoc,
			},
			Login: Login{
				Email:      req.Email,
				Password:   req.Password,
				TOTPSecret: req.TOTPSecret,
			},
		}

		// go-rod uses Must* methods that panic on error — recover gracefully
		var result BackofficeResult
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Warnf("BackofficeScreenshot panic: %v", r)
					result = BackofficeResult{Status: "error", Error: fmt.Sprintf("browser panic: %v", r)}
				}
			}()
			result = BackofficeScreenshot(boArgs)
		}()

		if result.Status != "OK" {
			writeJSON(w, http.StatusOK, ScreenshotResponse{
				Status: result.Status,
				Error:  result.Error,
			})
			return
		}

		// List screenshot files
		parts, contactLogs := listScreenshots(req.ScreenshotPath)

		writeJSON(w, http.StatusOK, ScreenshotResponse{
			Status:      "OK",
			Files:       parts,
			ContactLogs: contactLogs,
		})
	})

	mux.HandleFunc("POST /login", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			URL        string `json:"url"`
			Email      string `json:"email"`
			Password   string `json:"password"`
			TOTPSecret string `json:"totpSecret"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"status": "error", "error": "invalid JSON: " + err.Error()})
			return
		}
		if req.URL == "" {
			req.URL = "https://backoffice.wego.net"
		}
		if req.Email == "" || req.Password == "" || req.TOTPSecret == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"status": "error", "error": "email, password, and totpSecret are required"})
			return
		}

		log.Debugf("POST /login url=%s email=%s", req.URL, req.Email)

		var status, errMsg string
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Warnf("Login panic: %v", r)
					status = "error"
					errMsg = fmt.Sprintf("browser panic: %v", r)
				}
			}()
			status, errMsg = doLogin(browserDefaults, req.URL, Login{
				Email:      req.Email,
				Password:   req.Password,
				TOTPSecret: req.TOTPSecret,
			})
		}()

		resp := map[string]string{"status": status}
		if errMsg != "" {
			resp["error"] = errMsg
		}
		writeJSON(w, http.StatusOK, resp)
	})

	addr := fmt.Sprintf(":%d", port)
	log.Debugf("SSO HTTP server starting on %s", addr)

	server := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 5 * time.Minute, // screenshots can take a while
		IdleTimeout:  60 * time.Second,
	}

	if err := server.ListenAndServe(); err != nil {
		log.Fatalf("HTTP server error: %v", err)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// doLogin navigates to the backoffice, clicks Sign In with Google, and completes
// the Google OAuth login flow (email, password, TOTP). The session persists in the
// browserless Chrome profile for subsequent BackofficeScreenshot calls.
func doLogin(browserDefaults Browser, targetURL string, login Login) (status string, errMsg string) {
	// Use /data profile to persist session for browserless Chrome
	loginBrowser := browserDefaults
	if loginBrowser.ProfileLocation == "" {
		loginBrowser.ProfileLocation = "/data"
	}
	b, cleanup := browser(loginBrowser)
	if cleanup != nil {
		defer cleanup()
	}
	defer b.MustClose()

	page := b.MustPage("")
	page.MustEmulate(devices.Device{
		UserAgent:      browserDefaults.GetUserAgent(),
		AcceptLanguage: "en-US",
		Screen: devices.Screen{
			DevicePixelRatio: 2,
			Horizontal:       devices.ScreenSize{Width: 1440, Height: 810},
			Vertical:         devices.ScreenSize{Width: 810, Height: 1440},
		},
		Title: "Login Session",
	})

	log.Debugf("login: navigating to %s", targetURL)
	page.MustNavigate(targetURL)
	if err := page.WaitLoad(); err != nil {
		log.Debugf("login: WaitLoad error: %v", err)
	}
	time.Sleep(5 * time.Second)

	// Check if already authenticated
	currentURL := page.MustInfo().URL
	if !strings.Contains(currentURL, "signin") && !strings.Contains(currentURL, "accounts.google.com") {
		log.Debugf("login: already authenticated at %s", currentURL)
		return "OK", ""
	}

	// Click the Sign In with Google button
	signInXPaths := []string{
		`//a[contains(@class, 'login-button')]`,
		`//a[contains(@href, '/proxy?redirect=')]`,
		`//*[contains(text(), 'Sign in')]`,
	}
	clicked := false
	for _, xpath := range signInXPaths {
		if has, el, _ := page.HasX(xpath); has {
			log.Debugf("login: clicking sign-in element: %s", xpath)
			el.MustClick()
			clicked = true
			break
		}
	}
	if !clicked {
		return "error", "could not find sign-in button"
	}

	time.Sleep(10 * time.Second)
	afterClickURL := page.MustInfo().URL
	log.Debugf("login: after sign-in click: %s", afterClickURL)

	// If redirected to Google, perform the login
	if strings.Contains(afterClickURL, "accounts.google.com") {
		googleLogin(page, login)
	} else {
		log.Debugf("login: OAuth auto-completed (cached session)")
	}

	// Navigate back to target to verify
	page.MustNavigate(targetURL)
	if err := page.WaitLoad(); err != nil {
		log.Debugf("login: WaitLoad after login: %v", err)
	}
	time.Sleep(10 * time.Second)

	finalURL := page.MustInfo().URL
	if strings.Contains(finalURL, "signin") || strings.Contains(finalURL, "accounts.google.com") {
		log.Warnf("login: still on login page after auth: %s", finalURL)
		return "error", "login completed but session not established"
	}

	log.Debugf("login: success — authenticated at %s", finalURL)
	return "OK", ""
}

// listScreenshots scans the directory for booking_part_*.png and contact_logs_*.png files.
func listScreenshots(dir string) (parts []string, contactLogs []string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "booking_part_") && strings.HasSuffix(name, ".png") {
			parts = append(parts, filepath.Join(dir, name))
		} else if strings.HasPrefix(name, "contact_logs_") && strings.HasSuffix(name, ".png") {
			contactLogs = append(contactLogs, filepath.Join(dir, name))
		}
	}
	sort.Strings(parts)
	sort.Strings(contactLogs)
	return
}

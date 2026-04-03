package sso

import (
	"encoding/json"
	"fmt"
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

		boArgs := BackofficeArgs{
			URL: req.URL,
			Browser: Browser{
				Mode:            browserDefaults.Mode,
				RemoteURL:       browserDefaults.RemoteURL,
				UserAgent:       browserDefaults.UserAgent,
				ScreenshotPath:  req.ScreenshotPath,
				Timeout:         browserDefaults.Timeout,
				ProfileLocation: browserDefaults.ProfileLocation,
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

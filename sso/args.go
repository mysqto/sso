package sso

import (
	"os"
	"time"
)

var (
	defaultUserAgent   = `Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36`
	defaultRunFilePath = "/var/run/sso"
)

// Browser defines the browser information
type Browser struct {
	Mode            string
	RemoteURL       string
	UserAgent       string
	ScreenshotPath  string
	Timeout         time.Duration
	ProfileLocation string
}

// GetUserAgent returns the user agent
func (a Browser) GetUserAgent() string {
	if a.UserAgent == "" {
		return defaultUserAgent
	}
	return a.UserAgent
}

// GetProfileLocation returns the profile location
func (a Browser) GetProfileLocation() string {
	if a.ProfileLocation == "" {
		dir, err := os.MkdirTemp("sso", "google-chrome-*")
		if err != nil {
			_ = os.RemoveAll(dir)
		}
		a.ProfileLocation = dir
	}
	return a.ProfileLocation
}

// Login defines the login information
type Login struct {
	URL        string
	Email      string
	Password   string
	TOTPSecret string
}

// Args defines the arguments for the sso
type Args struct {
	Login   Login
	Browser Browser
	RunFile string
}

// RunFilePath returns the run file path
func (a Args) RunFilePath() string {
	if a.RunFile == "" {
		return defaultRunFilePath
	}
	return a.RunFile
}

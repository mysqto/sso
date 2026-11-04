package sso

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/mysqto/log"
)

// RedirectCapture is the payload written to the redirect output path.
type RedirectCapture struct {
	CapturedAt string `json:"captured_at"`
	URL        string `json:"url"`
}

// captureRedirect installs a browser-wide hijack that waits for a request to a
// URL starting with prefix, writes that URL to outputPath (stdout when "-"),
// and aborts the request.
//
// It is for OAuth clients with a loopback redirect URI, such as a CLI's
// http://localhost:<port>/callback. The authorization code is in the query
// string of that request. The client that started the flow holds the PKCE
// verifier and exchanges the code itself, so the code never needs to reach the
// loopback address. Aborting it before it leaves the browser also stops
// anything that happens to listen on that port inside the browser's host from
// receiving it.
//
// Redirect hops are new requests to the Fetch domain, so a 302 from the
// authorization server to the prefix is paused here like any other request.
func captureRedirect(browser *rod.Browser, prefix, outputPath string) (chan struct{}, func()) {
	done := make(chan struct{})
	var once sync.Once
	router := browser.HijackRequests()
	router.MustAdd("*", func(ctx *rod.Hijack) {
		target := ctx.Request.URL().String()
		if !strings.HasPrefix(target, prefix) {
			ctx.ContinueRequest(&proto.FetchContinueRequest{})
			return
		}
		log.Debugf("captured redirect to %s", prefix)
		capture := RedirectCapture{
			CapturedAt: time.Now().UTC().Format(time.RFC3339),
			URL:        target,
		}
		if err := writeRedirectOutput(capture, outputPath); err != nil {
			log.Warnf("failed to write redirect output: %v", err)
		}
		ctx.Response.Fail(proto.NetworkErrorReasonAborted)
		once.Do(func() { close(done) })
	})
	go router.Run()
	return done, func() { _ = router.Stop() }
}

func writeRedirectOutput(capture RedirectCapture, outputPath string) error {
	data, err := json.Marshal(capture)
	if err != nil {
		return fmt.Errorf("marshal capture: %w", err)
	}
	data = append(data, '\n')
	if outputPath == "-" {
		_, err = os.Stdout.Write(data)
		return err
	}
	return os.WriteFile(outputPath, data, 0600)
}

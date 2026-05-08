package sso

import (
	"encoding/json"
	"fmt"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/mysqto/log"
	netUrl "net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// SAMLCapture is the structured payload written to the output path.
type SAMLCapture struct {
	CapturedAt string            `json:"captured_at"`
	URL        string            `json:"url"`
	Method     string            `json:"method"`
	Headers    map[string]string `json:"headers"`
	Form       map[string]string `json:"form"`
	Body       string            `json:"body"`
	Curl       string            `json:"curl"`
}

// captureSAML installs a browser-wide hijack that intercepts a SAMLResponse
// POST, writes it to outputPath (stdout when "-"), and aborts the request so
// it can be replayed via curl. The first `skip` matching POSTs are passed
// through unmodified — useful when the flow chains through an intermediate
// IdP (e.g. AWS IAM Identity Center) before the final assertion. Returns a
// channel that closes once the capture completes and a stop function for
// cleanup.
func captureSAML(browser *rod.Browser, outputPath string, skip int) (chan struct{}, func()) {
	done := make(chan struct{})
	var (
		once    sync.Once
		seenMu  sync.Mutex
		seen    int
	)
	router := browser.HijackRequests()
	router.MustAdd("*", func(ctx *rod.Hijack) {
		if ctx.Request.Method() != "POST" {
			ctx.ContinueRequest(&proto.FetchContinueRequest{})
			return
		}
		body := ctx.Request.Body()
		if !strings.Contains(body, "SAMLResponse=") {
			ctx.ContinueRequest(&proto.FetchContinueRequest{})
			return
		}
		seenMu.Lock()
		seen++
		idx := seen
		seenMu.Unlock()
		target := ctx.Request.URL().String()
		if idx <= skip {
			log.Debugf("passing through SAML POST #%d to %s (skip=%d)", idx, target, skip)
			ctx.ContinueRequest(&proto.FetchContinueRequest{})
			return
		}
		log.Debugf("captured SAML POST #%d to %s", idx, target)
		headers := map[string]string{}
		for k, v := range ctx.Request.Headers() {
			headers[k] = v.String()
		}
		capture := buildSAMLCapture(target, body, headers)
		if err := writeSAMLOutput(capture, outputPath); err != nil {
			log.Warnf("failed to write SAML output: %v", err)
		} else if outputPath != "-" {
			log.Debugf("SAML capture written to %s", outputPath)
		}
		ctx.Response.Fail(proto.NetworkErrorReasonAborted)
		once.Do(func() { close(done) })
	})
	go router.Run()
	return done, func() { _ = router.Stop() }
}

func buildSAMLCapture(url, body string, headers map[string]string) SAMLCapture {
	form := map[string]string{}
	if values, err := netUrl.ParseQuery(body); err == nil {
		for k, vs := range values {
			if len(vs) > 0 {
				form[k] = vs[0]
			}
		}
	}
	return SAMLCapture{
		CapturedAt: time.Now().UTC().Format(time.RFC3339),
		URL:        url,
		Method:     "POST",
		Headers:    headers,
		Form:       form,
		Body:       body,
		Curl:       buildCurlCommand(url, form),
	}
}

func buildCurlCommand(url string, form map[string]string) string {
	var sb strings.Builder
	sb.WriteString("curl -i -X POST ")
	sb.WriteString(shellQuote(url))
	sb.WriteString(" \\\n  -H 'Content-Type: application/x-www-form-urlencoded'")
	for k, v := range form {
		sb.WriteString(" \\\n  --data-urlencode ")
		sb.WriteString(shellQuote(k + "=" + v))
	}
	return sb.String()
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func writeSAMLOutput(capture SAMLCapture, outputPath string) error {
	data, err := json.MarshalIndent(capture, "", "  ")
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

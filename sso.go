package main

import (
	"flag"
	"fmt"
	"github.com/mysqto/log"
	"os"
	"sso/sso"
	"strings"
	"time"
)

func getOptionalArg(arg *string, env string) (value string) {
	if arg == nil || *arg == "" {
		value = os.Getenv(env)
	} else {
		value = *arg
	}
	return
}

func getArg(arg *string, errors *[]string, env, argName string) (value string) {
	if arg == nil || *arg == "" {
		value = os.Getenv(env)
	} else {
		value = *arg
	}
	if value == "" {
		log.Debugf("%s is required", argName)
		*errors = append(*errors, fmt.Sprintf("%s is required", argName))
	}
	return
}

func getArgWithValues(arg *string, values []string, errors *[]string, env, argName string) (value string) {
	value = getArg(arg, errors, env, argName)
	if value != "" {
		for _, v := range values {
			if value == v {
				return
			}
		}
		*errors = append(*errors, fmt.Sprintf("%s is invalid, available values are: %v", argName, values))
	}
	return
}

var (
	browserModes = []string{"local", "rod-managed", "browserless-v1", "browserless-v2"}
)

func parseArgs() (args sso.Args, errors []string) {
	// get arguments --sso-url --email --password --otp-secret --mode --user-agent --remote-url --screenshot-path --telegram-bot-token --browser-timeout
	ssoURLArg := flag.String("sso-url", "", "SSO URL")
	emailArg := flag.String("email", "", "Email")
	passwordArg := flag.String("password", "", "Password")
	otpSecretArg := flag.String("otp-secret, ", "", "OTP secret")
	modeArg := flag.String("mode", "", "Browser mode, available mode are: local, rod-managed, browserless-v1, browserless-v2")
	remoteURLArg := flag.String("remote-url", "", "Remote URL")
	userAgentArg := flag.String("user-agent", "", "User agent")
	screenShotPathArg := flag.String("screenshot-path", "", "Screenshot path")
	browserTimeoutArg := flag.String("browser-timeout", "", "Browser timeout")
	browserProfileLocationArg := flag.String("profile", "", "Browser profile location")
	runFileArg := flag.String("run-file", "", "Run file path")
	flag.Parse()

	var mode, remoteURL string
	args.Login.URL = getArg(ssoURLArg, &errors, "SSO_URL", "SSO URL")
	args.Login.Email = getArg(emailArg, &errors, "SSO_EMAIL", "Email")
	args.Login.Password = getArg(passwordArg, &errors, "SSO_PASSWORD", "Password")
	args.Login.TOTPSecret = getArg(otpSecretArg, &errors, "SSO_OTP_SECRET", "OTP Secret")
	mode = getArgWithValues(modeArg, browserModes, &errors, "BROWSER_MODE", "Browser mode")
	remoteURL = getOptionalArg(remoteURLArg, "BROWSER_REMOTE_URL")
	args.Browser.Mode = mode
	args.Browser.RemoteURL = remoteURL
	args.Browser.UserAgent = getOptionalArg(userAgentArg, "USER_AGENT")
	args.Browser.ScreenshotPath = getOptionalArg(screenShotPathArg, "SCREENSHOT_PATH")
	args.Browser.ProfileLocation = getOptionalArg(browserProfileLocationArg, "CHROME_PROFILE")
	browserTimeoutStr := getOptionalArg(browserTimeoutArg, "BROWSER_TIMEOUT")
	args.RunFile = getOptionalArg(runFileArg, "RUN_FILE")
	args.Browser.Timeout = 4 * time.Minute
	if browserTimeoutStr != "" {
		timout, err := time.ParseDuration(browserTimeoutStr)
		if err != nil || timout < 3*time.Minute || timout > 10*time.Minute {
			log.Warnf("invalid browser timeout %s using default browser-timeout 4 minutes", browserTimeoutStr)
		} else {
			args.Browser.Timeout = timout
		}
	}
	if mode == "browserless-v1" || mode == "browserless-v2" {
		if remoteURL == "" {
			errors = append(errors, "remote-url is required for %s", mode)
		}
	}
	return
}

func main() {
	args, errors := parseArgs()
	if len(errors) > 0 {
		log.Fatalf("fatal:\n%v", strings.Join(errors, "\n"))
	}

	// do the sso login
	sso.Auth(args)
}

package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
)

const (
	pathToGoogleChrome        string = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
	chromeRemoteDebuggingPort string = "9222"
	chromeDebugCheckUrl       string = "http://127.0.0.1:" + chromeRemoteDebuggingPort
	chromeWebsocketUrl        string = "ws://127.0.0.1:" + chromeRemoteDebuggingPort
	selOktaFastPassSelect     string = "div[data-se=okta_verify-signed_nonce] a.select-factor"
	selAllowButton            string = "#cli_login_button"
	selLoginSuccessIcon       string = ".awsui-icon-variant-success"
)

func performSSOLogin() error {
	// Automated SSO login requires Chrome running with remote debugging enabled.
	if err := ensureChromeWithRemoteDebuggingEnabled(); err != nil {
		return fmt.Errorf("error (re)starting chrome with remote debugging enabled: %w", err)
	}

	// In case anything goes wrong, we set a 1m timeout to avoid hanging indefinitely.
	ctx := context.Background()
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	// Run sso login with the --no-browser option since we will open the browser ourselves.
	cmd := exec.CommandContext(ctx, "aws", "sso", "login", "--no-browser")
	outPipe, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		return err
	}

	var ssoUrl string
	scanner := bufio.NewScanner(outPipe)
	for scanner.Scan() {
		line := scanner.Text()
		log.Println(line)
		if strings.Contains(line, "?user_code=") {
			ssoUrl = line
			break
		}
	}

	if err := approveSSOLoginWithChrome(ssoUrl); err != nil {
		return fmt.Errorf("failed approving login with chrome: %w", err)
	}

	return cmd.Wait()
}

func ensureChromeWithRemoteDebuggingEnabled() error {
	// If we can't reach the debugging port, we need to restart Chrome.
	if _, err := http.Get(chromeDebugCheckUrl); err != nil {
		err := exec.Command("killall", "Google Chrome").Run()
		// Wait until killall returns an error (no matching processes found)
		for err == nil {
			time.Sleep(500 * time.Millisecond)
			err = exec.Command("killall", "Google Chrome").Run()
		}
		// (Re-)Start Chrome with remote debugging enabled.
		// TODO: This is currently MacOS specific, but should be easy enough to do on Linux.
		_ = exec.Command("open", "-a", pathToGoogleChrome, "--args", fmt.Sprintf("--remote-debugging-port=%s", chromeRemoteDebuggingPort)).Run()
		// Wait until the remote debugging port is reachable
		for i := 0; i < 5; i++ {
			if _, err := http.Get(chromeDebugCheckUrl); err != nil {
				time.Sleep(time.Second)
			} else {
				return nil
			}
		}
		return fmt.Errorf("chrome debug port still not reachable after 5s")
	}
	return nil
}

func approveSSOLoginWithChrome(ssoUrl string) error {
	// Create a new Chrome context with a remote allocator to attach to the debug websocket.
	allocCtx, cancel := chromedp.NewRemoteAllocator(context.Background(), chromeWebsocketUrl)
	defer cancel()
	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	// Add a timeout to avoid blocking forever if something goes wrong.
	ctx, cancel = context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	return chromedp.Run(ctx,
		// Open SSO URL, which should contain a '?user_code=' parameter to autofill the code.
		chromedp.Navigate(ssoUrl),
		// We will be forwarded to the Okta verify page, which ultimately redirects us to
		// the AWS page for approving the request. Sometimes, user interaction will be required
		// to (re-)authorize Okta, but typically should be automatic. Here, we simply wait until
		// the "Approve" button on the AWS page is visible...
		chromedp.WaitVisible(selAllowButton, chromedp.ByQuery),
		// ...and click it.
		chromedp.Click(selAllowButton),
		// Wait until the success icon is visible on the next page.
		chromedp.WaitVisible(selLoginSuccessIcon, chromedp.ByQuery),
	)
}

func main() {
	if err := performSSOLogin(); err != nil {
		log.Printf("SSO login failed: %s\n", err.Error())
	} else {
		log.Printf("Login Success!")
	}
}

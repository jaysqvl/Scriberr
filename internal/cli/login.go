package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Authenticate with the Scriberr server",
	Run:   runLogin,
}

var serverURL string

type cliAuthorizationStartResponse struct {
	State string `json:"state"`
}

type cliAuthorizationCallback struct {
	Code  string
	State string
}

type cliTokenResponse struct {
	Token    string `json:"token"`
	Username string `json:"username"`
}

func init() {
	rootCmd.AddCommand(loginCmd)
	loginCmd.Flags().StringVarP(&serverURL, "server", "s", "http://localhost:8080", "Scriberr server URL")
}

func runLogin(cmd *cobra.Command, args []string) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Printf("Failed to start local callback server: %v\n", err)
		return
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	callbackURL := fmt.Sprintf("http://127.0.0.1:%d/callback", port)

	verifier, challenge, err := newPKCEPair()
	if err != nil {
		fmt.Printf("Failed to secure CLI login: %v\n", err)
		return
	}

	client := &http.Client{Timeout: 15 * time.Second}
	startResponse, err := startCLILogin(client, serverURL, callbackURL, challenge)
	if err != nil {
		fmt.Printf("Failed to start CLI login: %v\n", err)
		return
	}

	callbackChan := make(chan cliAuthorizationCallback, 1)
	errChan := make(chan error, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		state := r.URL.Query().Get("state")
		if code == "" || state != startResponse.State {
			http.Error(w, "Login failed: invalid authorization response.", http.StatusBadRequest)
			select {
			case errChan <- fmt.Errorf("invalid authorization callback"):
			default:
			}
			return
		}

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "Login approved. You can close this window.")
		select {
		case callbackChan <- cliAuthorizationCallback{Code: code, State: state}:
		default:
		}
	})

	localServer := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       15 * time.Second,
	}
	go func() {
		if err := localServer.Serve(listener); err != nil && err != http.ErrServerClosed {
			select {
			case errChan <- err:
			default:
			}
		}
	}()

	authURL := strings.TrimRight(serverURL, "/") + "/auth/cli/authorize?state=" + url.QueryEscape(startResponse.State)
	fmt.Printf("Opening browser to authorize: %s\n", authURL)
	if err := openBrowser(authURL); err != nil {
		fmt.Printf("Could not open the browser: %v\nOpen the URL above manually.\n", err)
	}

	var callback cliAuthorizationCallback
	select {
	case callback = <-callbackChan:
	case err := <-errChan:
		fmt.Printf("Login failed: %v\n", err)
		return
	case <-time.After(5 * time.Minute):
		fmt.Println("Login timed out.")
		return
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = localServer.Shutdown(shutdownCtx)

	tokenResponse, err := redeemCLILogin(client, serverURL, callback, verifier)
	if err != nil {
		fmt.Printf("Login failed: %v\n", err)
		return
	}
	if _, err := SaveConfig(serverURL, tokenResponse.Token, ""); err != nil {
		fmt.Printf("Error saving config: %v\n", err)
		return
	}
	fmt.Printf("Logged in as %s.\n", tokenResponse.Username)
}

func startCLILogin(client *http.Client, baseURL, callbackURL, challenge string) (cliAuthorizationStartResponse, error) {
	body := map[string]string{
		"callback_url":          callbackURL,
		"device_name":           "Scriberr CLI",
		"code_challenge":        challenge,
		"code_challenge_method": "S256",
	}
	var response cliAuthorizationStartResponse
	if err := postCLIJSON(client, strings.TrimRight(baseURL, "/")+"/api/v1/auth/cli/start", body, &response); err != nil {
		return response, err
	}
	if response.State == "" {
		return response, fmt.Errorf("server returned an empty authorization state")
	}
	return response, nil
}

func redeemCLILogin(client *http.Client, baseURL string, callback cliAuthorizationCallback, verifier string) (cliTokenResponse, error) {
	body := map[string]string{
		"state":         callback.State,
		"code":          callback.Code,
		"code_verifier": verifier,
	}
	var response cliTokenResponse
	if err := postCLIJSON(client, strings.TrimRight(baseURL, "/")+"/api/v1/auth/cli/token", body, &response); err != nil {
		return response, err
	}
	if response.Token == "" {
		return response, fmt.Errorf("server returned an empty CLI token")
	}
	return response, nil
}

func postCLIJSON(client *http.Client, endpoint string, body, response interface{}) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("server returned %s: %s", resp.Status, strings.TrimSpace(string(message)))
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(response)
}

func newPKCEPair() (string, string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", err
	}
	verifier := base64.RawURLEncoding.EncodeToString(raw)
	digest := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(digest[:]), nil
}

func openBrowser(targetURL string) error {
	var command string
	var args []string

	switch runtime.GOOS {
	case "windows":
		command = "cmd"
		args = []string{"/c", "start", "", targetURL}
	case "darwin":
		command = "open"
		args = []string{targetURL}
	default:
		command = "xdg-open"
		args = []string{targetURL}
	}
	return exec.Command(command, args...).Start()
}

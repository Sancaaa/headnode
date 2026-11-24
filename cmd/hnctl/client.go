package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// client talks to the headnode API with the login token from the config.
type client struct {
	server string
	token  string
	http   *http.Client
}

func newClient(c config) *client {
	// The timeout covers one API call (a list, opening a session). A tunnel is
	// a WebSocket and is not subject to it.
	return &client{server: c.Server, token: c.Token, http: &http.Client{Timeout: 30 * time.Second}}
}

// apiError is an error answer from the server: its status and its message.
type apiError struct {
	status  int
	message string
}

func (e *apiError) Error() string {
	if e.status == http.StatusUnauthorized {
		return e.message + " (run `hnctl login` again)"
	}
	return e.message
}

// do sends one API request and decodes a successful JSON reply into into (nil
// to ignore it). A body, if given, is sent as JSON.
func (c *client) do(method, path string, body, into any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, c.server+path, reader)
	if err != nil {
		return err
	}
	if c.token != "" {
		// A bearer token, not a cookie: it is only sent by this program, so the
		// server does not need an Origin header to tell it from a forged
		// request, and none is sent.
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach %s: %w", c.server, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		var reply struct {
			Error string `json:"error"`
		}
		json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&reply) // best effort: the status is enough if this fails
		message := reply.Error
		if message == "" {
			message = fmt.Sprintf("the server answered HTTP %d", resp.StatusCode)
		}
		return &apiError{status: resp.StatusCode, message: message}
	}
	if into != nil {
		if err := json.NewDecoder(resp.Body).Decode(into); err != nil {
			return fmt.Errorf("read the server's answer: %w", err)
		}
	}
	return nil
}

type loginReply struct {
	Username     string `json:"username"`
	Token        string `json:"token"`
	TOTPRequired bool   `json:"totp_required"`
	TOTPSetup    bool   `json:"totp_setup"`
	Secret       string `json:"secret"`
	OTPAuthURI   string `json:"otpauth_uri"`
}

// login asks the server for a token. client:"cli" is what makes the server
// reply with a bearer token instead of setting a cookie (protocol.md §1).
// When the server asks for a one-time code (or first-time setup), promptOTP
// receives the setup secret and otpauth:// URI (both empty when already set up).
func login(server, username, password string, promptOTP func(secret, otpauthURI string) (string, error)) (config, error) {
	c := &client{server: server, http: &http.Client{Timeout: 30 * time.Second}}
	var reply loginReply
	err := c.do("POST", "/api/login", map[string]string{"username": username, "password": password, "client": "cli"}, &reply)
	if err != nil {
		return config{}, err
	}
	if reply.TOTPSetup || reply.TOTPRequired {
		if promptOTP == nil {
			return config{}, errors.New("the server requires a one-time code")
		}
		code, err := promptOTP(reply.Secret, reply.OTPAuthURI)
		if err != nil {
			return config{}, err
		}
		reply = loginReply{}
		err = c.do("POST", "/api/login", map[string]string{
			"username": username,
			"password": password,
			"client":   "cli",
			"code":     strings.TrimSpace(code),
		}, &reply)
		if err != nil {
			return config{}, err
		}
	}
	if reply.Token == "" {
		return config{}, errors.New("the server did not send a token: is it an up-to-date headnode?")
	}
	return config{Server: server, Token: reply.Token, Username: reply.Username}, nil
}

// wsURL turns the server address into a WebSocket URL: http becomes ws and
// https becomes wss.
func wsURL(server, path string) string {
	return "ws" + strings.TrimPrefix(server, "http") + path
}

//go:generate api15gen ./api_data.json
package rsapi15

import (
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rightscale/rsc/cmds"
)

// RightScale API 1.5 client
// Instances of this struct should be created through `New`.
type Api15 struct {
	AccountId             int           // Account in which client is currently operating
	Auth                  Authenticator // Authenticator, signs requests for auth
	Logger                *log.Logger   // Optional logger, if specified requests and responses get logged
	Host                  string        // API host, e.g. "us-3.rightscale.com"
	Client                HttpClient    // Underlying http client
	DumpRequestResponse   bool          // Whether to dump HTTP requests and responses to STDOUT
	FetchLocationResource bool          // Whether to fetch resource pointed by Location header
}

// New returns a API 1.5 client that uses User oauth authentication.
// logger and client are optional.
// host may be blank in which case client attempts to resolve it using auth.
// If no HTTP client is specified then the default client is used.
func New(accountId int, refreshToken string, host string, logger *log.Logger,
	client HttpClient) (*Api15, error) {
	if client == nil {
		client = http.DefaultClient
	}
	var auth = OAuthAuthenticator{
		RefreshToken: refreshToken,
		RefreshAt:    time.Now().Add(-1 * time.Hour),
		Client:       client,
	}
	if host == "" {
		if resolved, err := auth.ResolveHost(accountId); err != nil {
			return nil, err
		} else {
			host = resolved
		}
	}
	return &Api15{
		AccountId: accountId,
		Auth:      &auth,
		Logger:    logger,
		Host:      host,
		Client:    client,
	}, nil
}

// NewRL10 returns a API 1.5 client that uses the information stored in /var/run/rll-secret to do
// auth and configure the host. The client behaves identically to the new returned by New in
// all other regards.
func NewRL10(logger *log.Logger, client HttpClient) (*Api15, error) {
	var rllConfig, err = ioutil.ReadFile("/var/run/rll-secret")
	if err != nil {
		return nil, fmt.Errorf("Failed to load RLL config: %s", err.Error())
	}
	var port string
	var secret string
	for _, line := range strings.Split(string(rllConfig), "\n") {
		var elems = strings.Split(line, "=")
		if len(elems) != 2 {
			return nil, fmt.Errorf("Invalid RLL configuration line '%s'", line)
		}
		switch elems[0] {
		case "RS_RLL_PORT":
			port = elems[1]
			if _, err := strconv.Atoi(elems[1]); err != nil {
				return nil, fmt.Errorf("Invalid port value '%s'", port)
			}
		case "RS_RLL_SECRET":
			secret = elems[1]
		}
	}
	var auth = RL10Authenticator{secret}
	var host = "localhost:" + port
	return &Api15{
		Auth:   &auth,
		Logger: logger,
		Host:   host,
		Client: client,
	}, nil
}

// Build client from command line
func FromCommandLine(cmdLine *cmds.CommandLine) (*Api15, error) {
	var client *Api15
	var httpClient *http.Client
	if cmdLine.NoRedirect {
		httpClient = &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return fmt.Errorf("Client configured to prevent redirection")
			},
		}
	} else {
		httpClient = http.DefaultClient
	}
	var err error
	if cmdLine.RL10 {
		client, err = NewRL10(nil, httpClient)
	} else {
		client, err = New(cmdLine.Account, cmdLine.Token, cmdLine.Host, nil, httpClient)
	}
	if err != nil {
		return nil, fmt.Errorf("Failed to create API session: %v", err.Error())
	}
	if cmdLine.ShowHelp {
		err = client.ShowHelp(cmdLine.Command)
		return nil, err
	} else {
		if cmdLine.Token == "" && !cmdLine.RL10 {
			return nil, fmt.Errorf("Missing OAuth token, use '-token TOKEN' or 'setup'")
		}
		client.DumpRequestResponse = cmdLine.Dump
		client.FetchLocationResource = cmdLine.FetchResource
	}
	return client, nil
}

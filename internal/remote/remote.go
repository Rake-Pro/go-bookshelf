// Package remote performs the only outbound HTTP the server ever makes: cover
// and metadata lookups against an online provider. It is disabled unless a
// provider is explicitly configured, and when enabled it refuses to connect to
// anything but a public http(s) endpoint.
package remote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"syscall"
	"time"
)

// Errors returned when a request is refused before it leaves the process.
var (
	ErrDisabled = errors.New("remote: outbound metadata fetch is disabled")
	ErrScheme   = errors.New("remote: only http and https URLs are allowed")
	ErrBlocked  = errors.New("remote: destination address is not permitted")
	ErrTooLarge = errors.New("remote: response exceeds the size limit")
)

// MaxResponseSize bounds a downloaded body.
const MaxResponseSize = 16 << 20

// Fetcher performs guarded outbound requests.
type Fetcher struct {
	enabled      bool
	allowPrivate bool
	client       *http.Client
}

// New returns a Fetcher. When enabled is false every call fails with
// ErrDisabled; when allowPrivate is false, private, loopback, link-local,
// multicast and unspecified destinations are refused.
func New(enabled, allowPrivate bool) *Fetcher {
	f := &Fetcher{enabled: enabled, allowPrivate: allowPrivate}
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
		// Control runs after DNS resolution with the address actually being
		// connected to, so a hostname that resolves to a private address (or
		// changes its answer between check and connect) is still refused.
		Control: func(network, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return fmt.Errorf("%w: %s", ErrBlocked, address)
			}
			ip := net.ParseIP(host)
			if !f.addressAllowed(ip) {
				return fmt.Errorf("%w: %s", ErrBlocked, host)
			}
			return nil
		},
	}
	f.client = &http.Client{
		Timeout: 20 * time.Second,
		Transport: &http.Transport{
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 15 * time.Second,
			DisableKeepAlives:     true,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("remote: too many redirects")
			}
			return checkScheme(req.URL)
		},
	}
	return f
}

// Enabled reports whether outbound fetching is switched on.
func (f *Fetcher) Enabled() bool { return f.enabled }

// AddressAllowed reports whether the fetcher would connect to ip.
func (f *Fetcher) AddressAllowed(ip net.IP) bool { return f.addressAllowed(ip) }

func (f *Fetcher) addressAllowed(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if f.allowPrivate {
		return true
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	// Carrier-grade NAT and the IPv4 "this network" block are neither private
	// nor loopback by the stdlib's definition, but are never a legitimate
	// metadata provider.
	if v4 := ip.To4(); v4 != nil {
		if v4[0] == 0 || (v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127) || v4[0] == 169 && v4[1] == 254 {
			return false
		}
	}
	return true
}

// CheckURL validates a URL without making a request.
func (f *Fetcher) CheckURL(raw string) error {
	if !f.enabled {
		return ErrDisabled
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("remote: parse url: %w", err)
	}
	if err := checkScheme(u); err != nil {
		return err
	}
	// A literal IP in the URL is checked here as well as at dial time, so an
	// obviously blocked target fails without any network activity at all.
	if ip := net.ParseIP(u.Hostname()); ip != nil && !f.addressAllowed(ip) {
		return fmt.Errorf("%w: %s", ErrBlocked, u.Hostname())
	}
	return nil
}

// Get fetches a URL and returns the body and content type.
func (f *Fetcher) Get(ctx context.Context, raw string) ([]byte, string, error) {
	if err := f.CheckURL(raw); err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "go-bookshelf")
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("remote: unexpected status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseSize+1))
	if err != nil {
		return nil, "", err
	}
	if len(body) > MaxResponseSize {
		return nil, "", ErrTooLarge
	}
	return body, resp.Header.Get("Content-Type"), nil
}

func checkScheme(u *url.URL) error {
	switch u.Scheme {
	case "http", "https":
		return nil
	}
	return fmt.Errorf("%w: %q", ErrScheme, u.Scheme)
}

// MaxDownloadSize bounds a streamed download. It is far larger than
// MaxResponseSize because this is the path a book arrives on, and an
// audiobook legitimately runs to gigabytes; nothing is held in memory.
const MaxDownloadSize = 2 << 30

// MaxDownloadTime bounds a streamed download end to end. The buffered client's
// 20-second Timeout cannot be used here: it covers reading the body as well as
// getting the headers, so it would cut off any book that takes longer than that
// to arrive - which, at the sizes this path exists for, is most of them.
const MaxDownloadTime = 30 * time.Minute

// Open performs a guarded GET and hands back the response body for streaming,
// refusing to read more than max bytes from it.
//
// Every guard the buffered Get applies still applies here - the scheme check,
// the literal-address check, the per-connection address check that runs after
// DNS resolution, and the redirect check, which re-runs both for every hop -
// because they all live below this call rather than in it. What changes is
// only that the caller consumes the body instead of receiving it whole, and
// that the deadline is the download's rather than a request's.
func (f *Fetcher) Open(ctx context.Context, raw string, max int64) (io.ReadCloser, string, error) {
	if err := f.CheckURL(raw); err != nil {
		return nil, "", err
	}
	if max <= 0 || max > MaxDownloadSize {
		max = MaxDownloadSize
	}
	// The deadline lives on the context, and so lasts until the body is closed
	// rather than until Do returns. Everything below still applies: the same
	// transport carries the dial-time address check, and the same redirect
	// policy re-runs the scheme check per hop.
	ctx, cancel := context.WithTimeout(ctx, MaxDownloadTime)
	streaming := &http.Client{Transport: f.client.Transport, CheckRedirect: f.client.CheckRedirect}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		cancel()
		return nil, "", err
	}
	req.Header.Set("User-Agent", "go-bookshelf")
	resp, err := streaming.Do(req)
	if err != nil {
		cancel()
		return nil, "", err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		cancel()
		return nil, "", fmt.Errorf("remote: unexpected status %d", resp.StatusCode)
	}
	// A declared length over the cap is refused before a single byte of the
	// body is read; a server that understates it is caught by the reader.
	if resp.ContentLength > max {
		resp.Body.Close()
		cancel()
		return nil, "", ErrTooLarge
	}
	return &cappedBody{body: resp.Body, left: max + 1, cancel: cancel}, resp.Header.Get("Content-Type"), nil
}

// cappedBody fails with ErrTooLarge instead of returning more than its limit.
type cappedBody struct {
	body   io.ReadCloser
	left   int64
	cancel context.CancelFunc
}

func (c *cappedBody) Read(p []byte) (int, error) {
	if c.left <= 0 {
		return 0, ErrTooLarge
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.body.Read(p)
	c.left -= int64(n)
	if c.left <= 0 && err == nil {
		return n, ErrTooLarge
	}
	return n, err
}

func (c *cappedBody) Close() error {
	err := c.body.Close()
	c.cancel()
	return err
}

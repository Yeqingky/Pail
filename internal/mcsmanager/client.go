// Package mcsmanager talks to an MCSManager panel's HTTP API. It covers the
// node and instance lists plus the file manager routes Pail uses. An API key
// travels in the apikey query parameter; a username and password use the
// panel session cookie plus its token parameter. Errors and logs never include
// the request URL.
package mcsmanager

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Config describes one MCSManager panel. Use either APIKey or
// Username/Password.
type Config struct {
	Endpoint           string
	APIKey             string
	Username           string
	Password           string
	Code               string // optional two-factor or verification code for the first login
	InsecureSkipVerify bool
	// HTTPClient overrides the transport (tests). When nil, NewHTTPClient is used.
	HTTPClient *http.Client
}

// ErrNeed2FA means the panel asked for a one-time two-factor code before it
// would create a session. Retry the login with Config.Code set.
var ErrNeed2FA = errors.New("the panel requires a two-factor code")

// NewHTTPClient returns a client with connection-level timeouts but no total
// request timeout, so file transfers are not cut off. It keeps cookies so a
// password sign-in's session survives across requests.
func NewHTTPClient(insecureSkipVerify bool) *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		ExpectContinueTimeout: time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   32,
		ForceAttemptHTTP2:     true,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: insecureSkipVerify},
	}}
}

// Client is one panel connection. It is safe for concurrent use.
type Client struct {
	base *url.URL
	key  string
	user string
	pass string
	hc   *http.Client // panel requests: keeps the session cookie
	dhc  *http.Client // daemon upload/download: no panel cookies

	smu   sync.Mutex // serializes sign-in and guards token, gen and code
	token string
	gen   uint64
	code  string // one-shot code for the next password sign-in
}

// New validates cfg and builds a client. No network traffic happens until the
// first call.
func New(cfg Config) (*Client, error) {
	base, err := url.Parse(strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/"))
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") ||
		base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, errors.New("endpoint must be an http(s) URL without credentials, query or fragment")
	}
	key := strings.TrimSpace(cfg.APIKey)
	user := strings.TrimSpace(cfg.Username)
	pass := cfg.Password
	if key != "" && (user != "" || pass != "") {
		return nil, errors.New("use either an API key or a username and password")
	}
	if key == "" && (user == "" || pass == "") {
		return nil, errors.New("a username and password are required")
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = NewHTTPClient(cfg.InsecureSkipVerify)
	}
	if hc.Jar == nil {
		jar, err := cookiejar.New(nil)
		if err != nil {
			return nil, err
		}
		hc = &http.Client{Transport: hc.Transport, CheckRedirect: hc.CheckRedirect, Timeout: hc.Timeout, Jar: jar}
	}
	dhc := &http.Client{Transport: hc.Transport, CheckRedirect: hc.CheckRedirect, Timeout: hc.Timeout}
	return &Client{base: base, key: key, user: user, pass: pass, hc: hc, dhc: dhc, code: strings.TrimSpace(cfg.Code)}, nil
}

// SetLoginCode supplies the one-time two-factor code for the next password
// sign-in. It is consumed even when the panel rejects it.
func (c *Client) SetLoginCode(code string) {
	c.smu.Lock()
	c.code = strings.TrimSpace(code)
	c.smu.Unlock()
}

// APIError is a panel answer that is not a success. HTTPStatus is the transport
// status; Status is the panel's JSON status (they usually agree, but the panel
// also reports 403 in a 200 body).
type APIError struct {
	HTTPStatus int
	Status     int
	Message    string
}

func (e *APIError) Error() string {
	msg := strings.TrimSpace(e.Message)
	if msg == "" {
		msg = "the panel rejected the request"
	}
	return msg
}

// AccessDenied reports whether the panel said the credentials lack permission.
func (e *APIError) AccessDenied() bool {
	return e.HTTPStatus == http.StatusUnauthorized || e.HTTPStatus == http.StatusForbidden ||
		e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden
}

// NotFound reports whether the panel said the resource does not exist.
func (e *APIError) NotFound() bool {
	return e.HTTPStatus == http.StatusNotFound || e.Status == http.StatusNotFound
}

type response struct {
	Status int             `json:"status"`
	Data   json.RawMessage `json:"data"`
	Time   int64           `json:"time"`
}

// do performs one panel request. path starts with "/". out may be nil. With a
// password account it signs in first and retries once after a session expires.
func (c *Client) do(ctx context.Context, method, route string, q url.Values, body, out any) error {
	if c.key == "" {
		if err := c.ensureSession(ctx); err != nil {
			return err
		}
	}
	for attempt := 0; ; attempt++ {
		token, gen := c.session()
		err := c.doOnce(ctx, method, route, q, body, out, token)
		if err == nil {
			return nil
		}
		if c.key != "" || attempt > 0 || !authFailure(err) {
			return err
		}
		valid, checkErr := c.sessionValid(ctx)
		if checkErr == nil && valid {
			return err // the session is fine; the route denied its permission
		}
		if err := c.relogin(ctx, gen); err != nil {
			return err
		}
	}
}

// doOnce performs one request with the given session token.
func (c *Client) doOnce(ctx context.Context, method, route string, q url.Values, body, out any, token string) error {
	u := *c.base
	u.Path = strings.TrimRight(c.base.Path, "/") + route
	if q == nil {
		q = url.Values{}
	}
	if c.key != "" {
		q.Set("apikey", c.key)
	} else {
		q.Set("token", token)
		q.Del("apikey")
	}
	u.RawQuery = q.Encode()

	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rdr)
	if err != nil {
		return err
	}
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	res, err := c.hc.Do(req)
	if err != nil {
		return redactURLError(err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return err
	}
	var r response
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &r)
	}
	if res.StatusCode >= 400 || r.Status != 0 && r.Status != http.StatusOK {
		return apiError(raw, r, res.StatusCode)
	}
	if out != nil {
		if len(r.Data) == 0 || string(r.Data) == "null" {
			return nil
		}
		if err := json.Unmarshal(r.Data, out); err != nil {
			return fmt.Errorf("decode %s %s: %w", method, route, err)
		}
	}
	return nil
}

// apiError turns a panel error answer into an APIError.
func apiError(raw []byte, r response, httpStatus int) *APIError {
	msg := ""
	// data often carries the reason on errors; keep it short.
	var s string
	if json.Unmarshal(r.Data, &s) == nil {
		msg = s
	}
	if strings.TrimSpace(msg) == "" && len(raw) > 0 {
		// The permission middleware answers with a plain localized string,
		// and some routes use message or msg in a JSON body.
		if json.Valid(raw) {
			var e struct {
				Message string `json:"message"`
				Msg     string `json:"msg"`
			}
			_ = json.Unmarshal(raw, &e)
			msg = strings.TrimSpace(e.Message)
			if msg == "" {
				msg = strings.TrimSpace(e.Msg)
			}
		}
		if msg == "" {
			msg = shortText(raw)
		}
	}
	return &APIError{HTTPStatus: httpStatus, Status: r.Status, Message: msg}
}

// authFailure reports whether err means the session is missing or expired.
func authFailure(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.AccessDenied()
}

// session returns the current token and its generation.
func (c *Client) session() (string, uint64) {
	c.smu.Lock()
	defer c.smu.Unlock()
	return c.token, c.gen
}

// ensureSession signs in once when no session exists yet.
func (c *Client) ensureSession(ctx context.Context) error {
	c.smu.Lock()
	defer c.smu.Unlock()
	if c.token != "" {
		return nil
	}
	return c.loginLocked(ctx)
}

// sessionValid asks the account endpoint whether the current token and cookie
// still belong to a session. A 403 from an ordinary route can mean either an
// expired session or a permission the account does not have.
func (c *Client) sessionValid(ctx context.Context) (bool, error) {
	token, _ := c.session()
	u := *c.base
	u.Path = strings.TrimRight(c.base.Path, "/") + "/api/auth/"
	q := url.Values{}
	q.Set("token", token)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	res, err := c.hc.Do(req)
	if err != nil {
		return false, redactURLError(err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return false, err
	}
	var r response
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &r)
	}
	return res.StatusCode == http.StatusOK && (r.Status == 0 || r.Status == http.StatusOK), nil
}

// relogin replaces an expired session. gen is the generation the caller saw;
// when another request already refreshed it, this is a no-op.
func (c *Client) relogin(ctx context.Context, gen uint64) error {
	c.smu.Lock()
	defer c.smu.Unlock()
	if c.gen != gen && c.token != "" {
		return nil
	}
	c.token = ""
	return c.loginLocked(ctx)
}

// loginLocked signs in and stores the panel's token. The session cookie is
// kept by the HTTP client's jar. The caller holds smu.
func (c *Client) loginLocked(ctx context.Context) error {
	body := map[string]string{"username": c.user, "password": c.pass}
	if c.code != "" {
		body["code"] = c.code
	}
	c.code = "" // one-shot, even when the panel rejects it
	reqBody, err := json.Marshal(body)
	if err != nil {
		return err
	}
	u := *c.base
	u.Path = strings.TrimRight(c.base.Path, "/") + "/api/auth/login"
	u.RawQuery = ""
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(reqBody))
	if err != nil {
		return err
	}
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	res, err := c.hc.Do(req)
	if err != nil {
		return redactURLError(err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return err
	}
	var r response
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &r)
	}
	if res.StatusCode >= 400 || r.Status != 0 && r.Status != http.StatusOK {
		return apiError(raw, r, res.StatusCode)
	}
	var value string
	if json.Unmarshal(r.Data, &value) != nil || value == "" {
		return errors.New("the panel returned no session token")
	}
	if value == "NEED_2FA" {
		return ErrNeed2FA
	}
	if value == "Logined" {
		return c.fetchTokenLocked(ctx)
	}
	c.token, c.gen = value, c.gen+1
	return nil
}

// fetchTokenLocked asks the panel for the token of the session in the cookie
// jar, for the rare "already logged in" answer to a login request.
func (c *Client) fetchTokenLocked(ctx context.Context) error {
	u := *c.base
	u.Path = strings.TrimRight(c.base.Path, "/") + "/api/auth/token"
	u.RawQuery = ""
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	res, err := c.hc.Do(req)
	if err != nil {
		return redactURLError(err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return err
	}
	var r response
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &r)
	}
	if res.StatusCode >= 400 || r.Status != 0 && r.Status != http.StatusOK {
		return apiError(raw, r, res.StatusCode)
	}
	var token string
	if json.Unmarshal(r.Data, &token) != nil || token == "" {
		return errors.New("the panel returned no session token")
	}
	c.token, c.gen = token, c.gen+1
	return nil
}

// redactURLError keeps the cause of a transport error without the URL: panel
// URLs carry the API key and daemon URLs carry a one-time password.
func redactURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s: %w", ue.Op, ue.Err)
	}
	return err
}

// shortText keeps an error body readable in one notice.
func shortText(b []byte) string {
	r := []rune(strings.TrimSpace(string(b)))
	if len(r) > 300 {
		return string(r[:300]) + "…"
	}
	return string(r)
}

// ---- nodes and instances -------------------------------------------------

// Daemon is one node connected to the panel.
type Daemon struct {
	UUID      string `json:"uuid"`
	IP        string `json:"ip"`
	Port      int    `json:"port"`
	Prefix    string `json:"prefix"`
	Available bool   `json:"available"`
	Remarks   string `json:"remarks"`
}

// InstanceConfig is the subset of an instance configuration the file manager
// needs.
type InstanceConfig struct {
	Nickname string `json:"nickname"`
	Cwd      string `json:"cwd"`
	Type     string `json:"type"`
}

// Instance is one server instance.
type Instance struct {
	UUID    string         `json:"instanceUuid"`
	Status  int            `json:"status"`
	Started int            `json:"started"`
	Config  InstanceConfig `json:"config"`
}

// Name returns the nickname, or the UUID when the panel has none.
func (i Instance) Name() string {
	if s := strings.TrimSpace(i.Config.Nickname); s != "" {
		return s
	}
	return i.UUID
}

// InstancePage is one page of a node's instances.
type InstancePage struct {
	Page     int        `json:"page"`
	PageSize int        `json:"pageSize"`
	MaxPage  int        `json:"maxPage"`
	Data     []Instance `json:"data"`
}

// ListDaemons returns the connected nodes. It requires an administrator API key.
func (c *Client) ListDaemons(ctx context.Context) ([]Daemon, error) {
	var out []Daemon
	err := c.do(ctx, http.MethodGet, "/api/service/remote_services_list", nil, nil, &out)
	return out, err
}

// UserInstance is one instance assigned to the API key's account. The panel
// fills nickname, hostIp, remarks and status when advanced is true; the basic
// answer only carries instanceUuid and daemonId.
type UserInstance struct {
	DaemonID string `json:"daemonId"`
	UUID     string `json:"instanceUuid"`
	Nickname string `json:"nickname"`
	HostIP   string `json:"hostIp"`
	Remarks  string `json:"remarks"`
	Status   int    `json:"status"`
}

// User is the account an API key belongs to, as /api/auth returns it.
type User struct {
	UUID       string         `json:"uuid"`
	UserName   string         `json:"userName"`
	Permission int            `json:"permission"`
	Instances  []UserInstance `json:"instances"`
}

// CurrentUser returns the account behind the API key and the instances
// assigned to it. Node and instance lists require an administrator; a regular
// account reaches its instances through this endpoint instead. advanced asks
// the panel to query each node for the instance nickname and status.
func (c *Client) CurrentUser(ctx context.Context, advanced bool) (User, error) {
	var q url.Values
	if advanced {
		q = url.Values{"advanced": {"true"}}
	}
	var out User
	err := c.do(ctx, http.MethodGet, "/api/auth", q, nil, &out)
	return out, err
}

// ListInstances returns one page of a node's instances.
func (c *Client) ListInstances(ctx context.Context, daemonID string, page, pageSize int, name string) (InstancePage, error) {
	if daemonID == "" {
		return InstancePage{}, errors.New("a node ID is required")
	}
	q := url.Values{}
	q.Set("daemonId", daemonID)
	q.Set("page", strconv.Itoa(max(page, 1)))
	q.Set("page_size", strconv.Itoa(clampPageSize(pageSize)))
	if name != "" {
		q.Set("instance_name", name)
	}
	var out InstancePage
	err := c.do(ctx, http.MethodGet, "/api/service/remote_service_instances", q, nil, &out)
	return out, err
}

func clampPageSize(n int) int {
	if n < 1 {
		return 10
	}
	if n > 50 {
		return 50
	}
	return n
}

// clampFilePageSize is the file manager's larger page limit.
func clampFilePageSize(n int) int {
	if n < 1 {
		return 10
	}
	if n > 100 {
		return 100
	}
	return n
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ---- file manager --------------------------------------------------------

// File is one directory entry.
type File struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	Time string `json:"time"`
	Mode int    `json:"mode"`
	Type int    `json:"type"` // 0 = folder, 1 = file
}

// Folder reports whether the entry is a directory.
func (f File) Folder() bool { return f.Type == 0 }

// FilePage is one page of a directory listing.
type FilePage struct {
	Items    []File `json:"items"`
	Page     int    `json:"page"`
	PageSize int    `json:"pageSize"`
	Total    int    `json:"total"`
	// Target is the instance-relative directory that was requested. The
	// panel's absolutePath is a path on the daemon host and must never be
	// sent back as a target: the daemon would join it below the instance root.
	Target string `json:"-"`
}

// DownloadTask is one URL download task of the instance.
type DownloadTask struct {
	TaskID  string  `json:"taskId"`
	Path    string  `json:"path"`
	Total   int64   `json:"total"`
	Current int64   `json:"current"`
	Status  int     `json:"status"`
	Error   *string `json:"error"`
}

// FileStatus is the file task state of one instance.
type FileStatus struct {
	InstanceFileTask        int            `json:"instanceFileTask"`
	GlobalFileTask          int            `json:"globalFileTask"`
	DownloadFileFromURLTask int            `json:"downloadFileFromURLTask"`
	DownloadTasks           []DownloadTask `json:"downloadTasks"`
	Platform                string         `json:"platform"`
	IsGlobalInstance        bool           `json:"isGlobalInstance"`
	Disks                   []string       `json:"disks"`
}

// Passport is the one-time upload or download credential of a daemon.
type Passport struct {
	Password string `json:"password"`
	Addr     string `json:"addr"`
}

// ListFiles returns one page of dir inside an instance.
func (c *Client) ListFiles(ctx context.Context, daemonID, uuid, dir string, page, pageSize int, fileName string) (FilePage, error) {
	if daemonID == "" || uuid == "" {
		return FilePage{}, errors.New("a node ID and an instance ID are required")
	}
	q := url.Values{}
	q.Set("daemonId", daemonID)
	q.Set("uuid", uuid)
	target := cleanTarget(dir)
	q.Set("target", target)
	q.Set("page", strconv.Itoa(max(page, 0)))
	q.Set("page_size", strconv.Itoa(clampFilePageSize(pageSize)))
	// The panel turns a missing file_name into the string "undefined" and
	// filters every entry out. An empty value means "no filter".
	q.Set("file_name", fileName)
	var out FilePage
	err := c.do(ctx, http.MethodGet, "/api/files/list", q, nil, &out)
	out.Target = target
	return out, err
}

// FileStatus returns the instance's file task state.
func (c *Client) FileStatus(ctx context.Context, daemonID, uuid string) (FileStatus, error) {
	q := url.Values{}
	q.Set("daemonId", daemonID)
	q.Set("uuid", uuid)
	var out FileStatus
	err := c.do(ctx, http.MethodGet, "/api/files/status", q, nil, &out)
	return out, err
}

func targetQuery(daemonID, uuid string) url.Values {
	q := url.Values{}
	q.Set("daemonId", daemonID)
	q.Set("uuid", uuid)
	return q
}

// Delete removes files and folders (recursively).
func (c *Client) Delete(ctx context.Context, daemonID, uuid string, targets []string) error {
	if len(targets) == 0 {
		return errors.New("no files were given")
	}
	return c.do(ctx, http.MethodDelete, "/api/files", targetQuery(daemonID, uuid), map[string]any{"targets": targets}, nil)
}

// Mkdir creates one folder.
func (c *Client) Mkdir(ctx context.Context, daemonID, uuid, target string) error {
	return c.do(ctx, http.MethodPost, "/api/files/mkdir", targetQuery(daemonID, uuid), map[string]string{"target": target}, nil)
}

// Touch creates one empty file.
func (c *Client) Touch(ctx context.Context, daemonID, uuid, target string) error {
	return c.do(ctx, http.MethodPost, "/api/files/touch", targetQuery(daemonID, uuid), map[string]string{"target": target}, nil)
}

// Copy copies source/destination pairs inside one instance.
func (c *Client) Copy(ctx context.Context, daemonID, uuid string, pairs [][2]string) error {
	return c.pairs(ctx, "/api/files/copy", daemonID, uuid, pairs)
}

// Move moves or renames source/destination pairs inside one instance.
func (c *Client) Move(ctx context.Context, daemonID, uuid string, pairs [][2]string) error {
	return c.pairs(ctx, "/api/files/move", daemonID, uuid, pairs)
}

func (c *Client) pairs(ctx context.Context, route, daemonID, uuid string, pairs [][2]string) error {
	if len(pairs) == 0 {
		return errors.New("no paths were given")
	}
	targets := make([][2]string, 0, len(pairs))
	for _, p := range pairs {
		targets = append(targets, [2]string{cleanTarget(p[0]), cleanTarget(p[1])})
	}
	return c.do(ctx, http.MethodPost, route, targetQuery(daemonID, uuid), map[string]any{"targets": targets}, nil)
}

// Chmod changes a file mode. deep applies it to the subtree. Linux only.
func (c *Client) Chmod(ctx context.Context, daemonID, uuid, target string, mode int, deep bool) error {
	return c.do(ctx, http.MethodPut, "/api/files/chmod", targetQuery(daemonID, uuid),
		map[string]any{"target": cleanTarget(target), "chmod": mode, "deep": deep}, nil)
}

// Compress writes targets into the zip file source.
func (c *Client) Compress(ctx context.Context, daemonID, uuid, source string, targets []string) error {
	if len(targets) == 0 {
		return errors.New("no paths were given")
	}
	clean := make([]string, 0, len(targets))
	for _, t := range targets {
		clean = append(clean, cleanTarget(t))
	}
	return c.do(ctx, http.MethodPost, "/api/files/compress", targetQuery(daemonID, uuid),
		map[string]any{"type": 1, "code": "utf-8", "source": cleanTarget(source), "targets": clean}, nil)
}

// Decompress extracts source into target with the given filename code page.
func (c *Client) Decompress(ctx context.Context, daemonID, uuid, source, target, code string) error {
	if code == "" {
		code = "utf-8"
	}
	return c.do(ctx, http.MethodPost, "/api/files/compress", targetQuery(daemonID, uuid),
		map[string]any{"type": 2, "code": code, "source": cleanTarget(source), "targets": cleanTarget(target)}, nil)
}

// ReadFile returns a text file's content.
func (c *Client) ReadFile(ctx context.Context, daemonID, uuid, target string) (string, error) {
	var out string
	err := c.do(ctx, http.MethodPut, "/api/files/", targetQuery(daemonID, uuid), map[string]string{"target": cleanTarget(target)}, &out)
	return out, err
}

// WriteFile replaces a text file's content.
func (c *Client) WriteFile(ctx context.Context, daemonID, uuid, target, text string) error {
	return c.do(ctx, http.MethodPut, "/api/files/", targetQuery(daemonID, uuid),
		map[string]string{"target": cleanTarget(target), "text": text}, nil)
}

// DownloadPassport asks for a one-time download credential for fileName.
func (c *Client) DownloadPassport(ctx context.Context, daemonID, uuid, fileName string) (Passport, error) {
	q := targetQuery(daemonID, uuid)
	q.Set("file_name", cleanTarget(fileName))
	var out Passport
	err := c.do(ctx, http.MethodGet, "/api/files/download", q, nil, &out)
	return out, err
}

// UploadPassport asks for a one-time upload credential for uploadDir.
func (c *Client) UploadPassport(ctx context.Context, daemonID, uuid, uploadDir string) (Passport, error) {
	q := targetQuery(daemonID, uuid)
	q.Set("upload_dir", cleanTarget(uploadDir))
	var out Passport
	err := c.do(ctx, http.MethodGet, "/api/files/upload", q, nil, &out)
	return out, err
}

// daemonURL builds a URL on the daemon that issued p. addr may omit its
// scheme; the panel's scheme is used then. The panel reports the daemon's
// WebSocket address when it is reached through a WebSocket reverse proxy;
// upload and download are still plain HTTP(S) routes.
func (c *Client) daemonURL(p Passport, route string) (string, error) {
	addr := strings.TrimSpace(p.Addr)
	if addr == "" || p.Password == "" {
		return "", errors.New("the panel returned an incomplete file passport")
	}
	switch {
	case strings.HasPrefix(strings.ToLower(addr), "wss://"):
		addr = "https://" + addr[len("wss://"):]
	case strings.HasPrefix(strings.ToLower(addr), "ws://"):
		addr = "http://" + addr[len("ws://"):]
	case !strings.Contains(addr, "://"):
		addr = c.base.Scheme + "://" + addr
	}
	u, err := url.Parse(strings.TrimRight(addr, "/"))
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("the panel returned an invalid daemon address %q", addr)
	}
	u.Path = strings.TrimRight(u.Path, "/") + route
	u.RawPath = ""
	return u.String(), nil
}

// DownloadURL is the one-time address of fileName on the daemon.
func (c *Client) DownloadURL(p Passport, fileName string) (string, error) {
	return c.daemonURL(p, "/download/"+p.Password+"/"+strings.TrimPrefix(cleanTarget(fileName), "/"))
}

// UploadURL is the one-time upload address on the daemon.
func (c *Client) UploadURL(p Passport) (string, error) {
	return c.daemonURL(p, "/upload/"+url.PathEscape(p.Password))
}

// Download streams fileName from the daemon into w. The passport is consumed.
func (c *Client) Download(ctx context.Context, p Passport, fileName string, w io.Writer) error {
	u, err := c.DownloadURL(p, fileName)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	res, err := c.dhc.Do(req)
	if err != nil {
		return redactURLError(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return &APIError{HTTPStatus: res.StatusCode, Message: "the daemon refused the download"}
	}
	_, err = io.Copy(w, res.Body)
	return err
}

// Upload posts one local file to uploadDir. overwrite=false lets the daemon
// keep an existing file under a new name. progress, when not nil, receives the
// number of file bytes read so far.
func (c *Client) Upload(ctx context.Context, daemonID, uuid, uploadDir, localPath string, overwrite bool, progress func(int64)) error {
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()
	p, err := c.UploadPassport(ctx, daemonID, uuid, uploadDir)
	if err != nil {
		return err
	}
	u, err := c.UploadURL(p)
	if err != nil {
		return err
	}
	var src io.Reader = f
	if progress != nil {
		src = &progressReader{r: f, fn: progress}
	}
	body, ct := multipartFile("file", filepath.Base(localPath), src)
	if !overwrite {
		q, _ := url.Parse(u)
		vals := q.Query()
		vals.Set("overwrite", "false")
		q.RawQuery = vals.Encode()
		u = q.String()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, body)
	if err != nil {
		if closer, ok := body.(io.Closer); ok {
			closer.Close()
		}
		return err
	}
	req.Header.Set("Content-Type", ct)
	res, err := c.dhc.Do(req)
	if err != nil {
		return redactURLError(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if res.StatusCode != http.StatusOK {
		return &APIError{HTTPStatus: res.StatusCode, Message: strings.TrimSpace(string(raw))}
	}
	return nil
}

// cleanTarget makes a path absolute inside the instance. Windows paths with a
// drive letter keep it; a trailing slash is removed.
func cleanTarget(p string) string {
	p = strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	if p == "" || p == "." {
		return "/"
	}
	if winDrive.MatchString(p) {
		return path.Clean(p)
	}
	return path.Clean("/" + strings.TrimPrefix(p, "/"))
}

var winDrive = regexp.MustCompile(`^[A-Za-z]:`)

// progressReader reports cumulative bytes read from r.
type progressReader struct {
	r  io.Reader
	n  int64
	fn func(int64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	if n > 0 {
		p.n += int64(n)
		p.fn(p.n)
	}
	return n, err
}

// multipartFile streams r as one multipart/form-data file field.
func multipartFile(field, name string, r io.Reader) (io.Reader, string) {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	ct := mw.FormDataContentType()
	go func() {
		part, err := mw.CreateFormFile(field, name)
		if err == nil {
			_, err = io.Copy(part, r)
		}
		if err == nil {
			err = mw.Close()
		}
		pw.CloseWithError(err)
	}()
	return pr, ct
}

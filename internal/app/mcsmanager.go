package app

import (
	"context"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/DejavuMoe/Pail/internal/mcsmanager"
	"github.com/DejavuMoe/Pail/internal/safepath"
)

// MCSManager file manager (APP_API section 11). The panel addresses files by
// node (daemon), instance (uuid) and an absolute path inside the instance.
// These methods are the only place the app layer uses the panel API directly;
// the S3 operations in the rest of the package are not involved.

// MCSDaemon is one node connected to the panel.
type MCSDaemon struct {
	ID, Name, Address string
	Available         bool
}

// MCSInstance is one server instance.
type MCSInstance struct {
	DaemonID, UUID, Name string
	Status               int
}

// MCSFile is one entry of an instance directory.
type MCSFile struct {
	Name, Path string
	Folder     bool
	Size       int64
	Mode       int
	Modified   string // the panel's display string
}

// MCSFilePage is one page of an instance directory.
type MCSFilePage struct {
	Entries               []MCSFile
	Page, PageSize, Total int
	Path                  string
}

// mapMCS classifies a panel error as an OpError.
func mapMCS(op string, err error) error {
	if err == nil {
		return nil
	}
	var oe *OpError
	if errors.As(err, &oe) {
		return oe
	}
	var ae *mcsmanager.APIError
	if errors.As(err, &ae) {
		kind := ErrOther
		switch {
		case ae.AccessDenied():
			kind = ErrAccessDenied
		case ae.NotFound():
			kind = ErrNotFound
		case ae.HTTPStatus == http.StatusBadRequest || ae.Status == http.StatusBadRequest:
			kind = ErrInvalid
		case ae.HTTPStatus == http.StatusConflict:
			kind = ErrConflict
		case ae.HTTPStatus == http.StatusNotImplemented:
			kind = ErrUnsupported
		}
		return opErr(op, kind, "%s", ae.Error())
	}
	return classify(op, err)
}

// MCSDaemons lists the nodes the account may see. A non-admin API key gets
// its assigned instances from the account endpoint, grouped by node ID.
func (s *Service) MCSDaemons(ctx context.Context, accountID string) ([]MCSDaemon, error) {
	const op = "MCSDaemons"
	if err := s.requireCapability(op, accountID, CapMCSInstanceList); err != nil {
		return nil, err
	}
	c, err := s.mcsClient(ctx, accountID)
	if err != nil {
		return nil, mapMCS(op, err)
	}
	ds, err := c.ListDaemons(ctx)
	if err == nil {
		out := make([]MCSDaemon, 0, len(ds))
		for _, d := range ds {
			name := strings.TrimSpace(d.Remarks)
			if name == "" {
				name = d.IP
			}
			out = append(out, MCSDaemon{ID: d.UUID, Name: name, Address: net.JoinHostPort(d.IP, strconv.Itoa(d.Port)), Available: d.Available})
		}
		sort.SliceStable(out, func(i, j int) bool {
			a, b := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
			if a != b {
				return a < b
			}
			return out[i].ID < out[j].ID
		})
		return out, nil
	}
	if !isMCSAccessDenied(err) {
		return nil, mapMCS(op, err)
	}
	u, err := c.CurrentUser(ctx, true)
	if err != nil {
		return nil, mapMCS(op, err)
	}
	type node struct{ name, addr string }
	nodes := map[string]*node{}
	for _, in := range u.Instances {
		id := strings.TrimSpace(in.DaemonID)
		if id == "" {
			continue
		}
		n := nodes[id]
		if n == nil {
			n = &node{}
			nodes[id] = n
		}
		if n.addr == "" {
			n.addr = strings.TrimSpace(in.HostIP)
			if n.addr == "-- Unknown --" {
				n.addr = ""
			}
		}
		if n.name == "" {
			if r := strings.TrimSpace(in.Remarks); r != "" {
				n.name = r
			} else if n.addr != "" {
				n.name = n.addr
			}
		}
	}
	out := make([]MCSDaemon, 0, len(nodes))
	for id, n := range nodes {
		name := n.name
		if name == "" {
			name = id
		}
		out = append(out, MCSDaemon{ID: id, Name: name, Address: n.addr, Available: true})
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if a != b {
			return a < b
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// MCSInstances lists one page of a node's instances.
func (s *Service) MCSInstances(ctx context.Context, accountID, daemonID string, page, pageSize int) ([]MCSInstance, error) {
	const op = "MCSInstances"
	if err := s.requireCapability(op, accountID, CapMCSInstanceList); err != nil {
		return nil, err
	}
	c, err := s.mcsClient(ctx, accountID)
	if err != nil {
		return nil, mapMCS(op, err)
	}
	p, err := c.ListInstances(ctx, daemonID, page, pageSize, "")
	if err != nil {
		if !isMCSAccessDenied(err) {
			return nil, mapMCS(op, err)
		}
		return mcsUserInstances(ctx, c, daemonID, page, pageSize)
	}
	out := make([]MCSInstance, 0, len(p.Data))
	for _, in := range p.Data {
		out = append(out, MCSInstance{DaemonID: daemonID, UUID: in.UUID, Name: in.Name(), Status: in.Status})
	}
	return out, nil
}

// mcsUserInstances lists the instances a non-administrator API key can see.
// The account endpoint has no pagination, so the page is cut here.
func mcsUserInstances(ctx context.Context, c *mcsmanager.Client, daemonID string, page, pageSize int) ([]MCSInstance, error) {
	u, err := c.CurrentUser(ctx, true)
	if err != nil {
		return nil, mapMCS("MCSInstances", err)
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 50
	}
	start := (page - 1) * pageSize
	out := make([]MCSInstance, 0, pageSize)
	matched := 0
	for _, in := range u.Instances {
		if strings.TrimSpace(in.DaemonID) != daemonID || strings.TrimSpace(in.UUID) == "" {
			continue
		}
		matched++
		if matched <= start {
			continue
		}
		if len(out) == pageSize {
			break
		}
		name := strings.TrimSpace(in.Nickname)
		if name == "" || name == "-- Unknown --" {
			name = in.UUID
		}
		out = append(out, MCSInstance{DaemonID: daemonID, UUID: in.UUID, Name: name, Status: in.Status})
	}
	return out, nil
}

// MCSFiles lists one page of dir inside an instance.
func (s *Service) MCSFiles(ctx context.Context, accountID, daemonID, uuid, dir string, page, pageSize int, filter string) (MCSFilePage, error) {
	const op = "MCSFiles"
	if err := s.requireCapability(op, accountID, CapMCSFileList); err != nil {
		return MCSFilePage{}, err
	}
	c, err := s.mcsClient(ctx, accountID)
	if err != nil {
		return MCSFilePage{}, mapMCS(op, err)
	}
	p, err := c.ListFiles(ctx, daemonID, uuid, dir, page, pageSize, filter)
	if err != nil {
		return MCSFilePage{}, mapMCS(op, err)
	}
	base := p.AbsolutePath
	if base == "" {
		base = dir
	}
	out := MCSFilePage{Page: p.Page, PageSize: p.PageSize, Total: p.Total, Path: base}
	for _, f := range p.Items {
		out.Entries = append(out.Entries, MCSFile{
			Name: f.Name, Path: joinMCS(base, f.Name), Folder: f.Folder(),
			Size: f.Size, Mode: f.Mode, Modified: f.Time,
		})
	}
	sort.SliceStable(out.Entries, func(i, j int) bool {
		a, b := out.Entries[i], out.Entries[j]
		if a.Folder != b.Folder {
			return a.Folder
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	return out, nil
}

// joinMCS joins a daemon directory and a child name with "/".
func joinMCS(dir, name string) string {
	dir = strings.TrimRight(dir, "/\\")
	if dir == "" {
		return "/" + name
	}
	return dir + "/" + name
}

// MCSMkdir creates one folder inside an instance.
func (s *Service) MCSMkdir(ctx context.Context, accountID, daemonID, uuid, target string) error {
	const op = "MCSMkdir"
	if err := s.requireCapability(op, accountID, CapMCSFileMkdir); err != nil {
		return err
	}
	c, err := s.mcsClient(ctx, accountID)
	if err != nil {
		return mapMCS(op, err)
	}
	return mapMCS(op, c.Mkdir(ctx, daemonID, uuid, target))
}

// MCSTouch creates an empty file inside an instance.
func (s *Service) MCSTouch(ctx context.Context, accountID, daemonID, uuid, target string) error {
	const op = "MCSTouch"
	if err := s.requireCapability(op, accountID, CapMCSFileTouch); err != nil {
		return err
	}
	c, err := s.mcsClient(ctx, accountID)
	if err != nil {
		return mapMCS(op, err)
	}
	return mapMCS(op, c.Touch(ctx, daemonID, uuid, target))
}

// MCSDelete removes files and folders.
func (s *Service) MCSDelete(ctx context.Context, accountID, daemonID, uuid string, targets []string) error {
	const op = "MCSDelete"
	if err := s.requireCapability(op, accountID, CapMCSFileDelete); err != nil {
		return err
	}
	c, err := s.mcsClient(ctx, accountID)
	if err != nil {
		return mapMCS(op, err)
	}
	return mapMCS(op, c.Delete(ctx, daemonID, uuid, targets))
}

// MCSCopy copies source/destination pairs inside one instance.
func (s *Service) MCSCopy(ctx context.Context, accountID, daemonID, uuid string, pairs [][2]string) error {
	const op = "MCSCopy"
	if err := s.requireCapability(op, accountID, CapMCSFileCopy); err != nil {
		return err
	}
	c, err := s.mcsClient(ctx, accountID)
	if err != nil {
		return mapMCS(op, err)
	}
	return mapMCS(op, c.Copy(ctx, daemonID, uuid, pairs))
}

// MCSMove moves or renames source/destination pairs inside one instance.
func (s *Service) MCSMove(ctx context.Context, accountID, daemonID, uuid string, pairs [][2]string) error {
	const op = "MCSMove"
	if err := s.requireCapability(op, accountID, CapMCSFileMove); err != nil {
		return err
	}
	c, err := s.mcsClient(ctx, accountID)
	if err != nil {
		return mapMCS(op, err)
	}
	return mapMCS(op, c.Move(ctx, daemonID, uuid, pairs))
}

// MCSChmod changes a file mode (Linux daemons only).
func (s *Service) MCSChmod(ctx context.Context, accountID, daemonID, uuid, target string, mode int, deep bool) error {
	const op = "MCSChmod"
	if err := s.requireCapability(op, accountID, CapMCSFileChmod); err != nil {
		return err
	}
	c, err := s.mcsClient(ctx, accountID)
	if err != nil {
		return mapMCS(op, err)
	}
	return mapMCS(op, c.Chmod(ctx, daemonID, uuid, target, mode, deep))
}

// MCSCompress writes targets into a zip file inside an instance.
func (s *Service) MCSCompress(ctx context.Context, accountID, daemonID, uuid, source string, targets []string) error {
	const op = "MCSCompress"
	if err := s.requireCapability(op, accountID, CapMCSFileArchive); err != nil {
		return err
	}
	c, err := s.mcsClient(ctx, accountID)
	if err != nil {
		return mapMCS(op, err)
	}
	return mapMCS(op, c.Compress(ctx, daemonID, uuid, source, targets))
}

// MCSDecompress extracts a zip file inside an instance.
func (s *Service) MCSDecompress(ctx context.Context, accountID, daemonID, uuid, source, target, code string) error {
	const op = "MCSDecompress"
	if err := s.requireCapability(op, accountID, CapMCSFileArchive); err != nil {
		return err
	}
	c, err := s.mcsClient(ctx, accountID)
	if err != nil {
		return mapMCS(op, err)
	}
	return mapMCS(op, c.Decompress(ctx, daemonID, uuid, source, target, code))
}

// MCSReadText reads a text file inside an instance.
func (s *Service) MCSReadText(ctx context.Context, accountID, daemonID, uuid, target string) (string, error) {
	const op = "MCSReadText"
	if err := s.requireCapability(op, accountID, CapMCSFileText); err != nil {
		return "", err
	}
	c, err := s.mcsClient(ctx, accountID)
	if err != nil {
		return "", mapMCS(op, err)
	}
	v, err := c.ReadFile(ctx, daemonID, uuid, target)
	return v, mapMCS(op, err)
}

// MCSWriteText replaces a text file inside an instance.
func (s *Service) MCSWriteText(ctx context.Context, accountID, daemonID, uuid, target, text string) error {
	const op = "MCSWriteText"
	if err := s.requireCapability(op, accountID, CapMCSFileText); err != nil {
		return err
	}
	c, err := s.mcsClient(ctx, accountID)
	if err != nil {
		return mapMCS(op, err)
	}
	return mapMCS(op, c.WriteFile(ctx, daemonID, uuid, target, text))
}

// MCSFileStatus returns the instance's file task state.
func (s *Service) MCSFileStatus(ctx context.Context, accountID, daemonID, uuid string) (mcsmanager.FileStatus, error) {
	const op = "MCSFileStatus"
	if err := s.requireCapability(op, accountID, CapMCSFileStatus); err != nil {
		return mcsmanager.FileStatus{}, err
	}
	c, err := s.mcsClient(ctx, accountID)
	if err != nil {
		return mcsmanager.FileStatus{}, mapMCS(op, err)
	}
	st, err := c.FileStatus(ctx, daemonID, uuid)
	return st, mapMCS(op, err)
}

// MCSDownload downloads one file into destDir and returns the local path. It
// writes a temporary file next to the destination and publishes it without
// overwriting an existing file; a numbered name is chosen instead.
func (s *Service) MCSDownload(ctx context.Context, accountID, daemonID, uuid, remotePath, destDir string) (string, error) {
	const op = "MCSDownload"
	if err := s.requireCapability(op, accountID, CapMCSFileDownload); err != nil {
		return "", err
	}
	name := path.Base(strings.ReplaceAll(remotePath, "\\", "/"))
	if name == "" || name == "." || name == "/" {
		return "", invalid(op, "the remote path has no file name")
	}
	if err := safepath.ValidKey(name); err != nil {
		return "", invalid(op, "the remote name cannot be stored locally: %v", err)
	}
	if strings.TrimSpace(destDir) == "" {
		return "", invalid(op, "a destination folder is required")
	}
	dir, err := filepath.Abs(destDir)
	if err != nil || !filepath.IsAbs(dir) {
		return "", invalid(op, "a destination folder is required")
	}
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		return "", invalid(op, "the destination %q is not a folder", destDir)
	}
	c, err := s.mcsClient(ctx, accountID)
	if err != nil {
		return "", mapMCS(op, err)
	}
	p, err := c.DownloadPassport(ctx, daemonID, uuid, remotePath)
	if err != nil {
		return "", mapMCS(op, err)
	}
	tmp, err := os.CreateTemp(dir, ".pail-mcs-*")
	if err != nil {
		return "", classify(op, err)
	}
	tmpName := tmp.Name()
	defer func() {
		if tmpName != "" {
			os.Remove(tmpName)
		}
	}()
	if err := c.Download(ctx, p, remotePath, tmp); err != nil {
		tmp.Close()
		return "", mapMCS(op, err)
	}
	if err := tmp.Close(); err != nil {
		return "", classify(op, err)
	}

	orig := filepath.Join(dir, name)
	final := orig
	for n := 1; ; n++ {
		if _, err := os.Lstat(final); errors.Is(err, fs.ErrNotExist) {
			break
		} else if err != nil {
			return "", classify(op, err)
		}
		final = numberedPath(orig, n)
		if n > 10000 {
			return "", opErr(op, ErrConflict, "no free file name for %q", name)
		}
	}
	if err := safepath.Publish(tmpName, final, false); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return "", opErr(op, ErrConflict, "%q already exists", final)
		}
		return "", classify(op, err)
	}
	tmpName = "" // published; the deferred remove must not delete the result
	return final, nil
}

// MCSUpload uploads regular files into remoteDir and returns how many were
// sent. The daemon keeps an existing file under a new name when overwrite is
// false.
func (s *Service) MCSUpload(ctx context.Context, accountID, daemonID, uuid, remoteDir string, localPaths []string) (int, error) {
	const op = "MCSUpload"
	if err := s.requireCapability(op, accountID, CapMCSFileUpload); err != nil {
		return 0, err
	}
	if len(localPaths) == 0 {
		return 0, invalid(op, "no files were selected")
	}
	c, err := s.mcsClient(ctx, accountID)
	if err != nil {
		return 0, mapMCS(op, err)
	}
	n := 0
	for _, p := range localPaths {
		fi, err := os.Lstat(p)
		if err != nil {
			return n, classify(op, err)
		}
		if !fi.Mode().IsRegular() {
			continue
		}
		if err := c.Upload(ctx, daemonID, uuid, remoteDir, p, false); err != nil {
			return n, mapMCS(op, err)
		}
		n++
	}
	if n == 0 {
		return 0, invalid(op, "no regular files were selected")
	}
	return n, nil
}

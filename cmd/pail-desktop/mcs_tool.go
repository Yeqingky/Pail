package main

import (
	"errors"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/DejavuMoe/Pail/cmd/pail-desktop/icons"
	"github.com/DejavuMoe/Pail/cmd/pail-desktop/locales"
	core "github.com/DejavuMoe/Pail/internal/app"
	"github.com/egoist/mygo/ui"
)

// MCSManager tool: choose a node and an instance, browse its files, and
// upload, download, create folders or delete. The panel addresses files by
// node, instance and absolute path, so this view keeps its own path instead
// of the S3 browser's bucket/prefix.

type mcsState struct {
	daemons    []core.MCSDaemon
	daemonID   string
	instances  []core.MCSInstance
	instanceID string

	path                string
	files               []core.MCSFile
	sel                 ui.Selection[string]
	list                ui.ListState
	cursor              int
	filePage, fileTotal int

	loading bool
	err     string
	gen     int

	folderOpen bool
	folderName string
	folderErr  string
	folderBusy bool

	renameOpen bool
	renamePath string
	renameName string
	renameErr  string
	renameBusy bool

	rowRects map[string]ui.Rect

	codeOpen  bool
	code      string
	codeErr   string
	codeBusy  bool
	codeRetry func()
}

func (m *mcsState) daemonName() string {
	for _, d := range m.daemons {
		if d.ID == m.daemonID {
			return d.Name
		}
	}
	return m.daemonID
}

func (m *mcsState) instanceName() string {
	for _, in := range m.instances {
		if in.UUID == m.instanceID {
			return in.Name
		}
	}
	return m.instanceID
}

// mcsErrorText explains a panel access denial or a missing second factor.
func mcsErrorText(err error) string {
	if errors.Is(err, core.ErrMCSNeed2FA) {
		return locales.McsNeed2FA()
	}
	if core.KindOf(err) != core.ErrAccessDenied {
		return humanError(err)
	}
	text := locales.McsAccessDenied()
	var oe *core.OpError
	if errors.As(err, &oe) {
		if msg := strings.TrimSpace(oe.Message); msg != "" && msg != "the panel rejected the request" {
			text += " (" + msg + ")"
		}
	}
	return text
}

func mcsColumns() []ui.TableColumn {
	return []ui.TableColumn{
		{Title: locales.CommonName(), ID: "name", MinWidth: 200},
		{Title: locales.CommonSize(), ID: "size", Width: 100, MinWidth: 70, Align: ui.End},
		{Title: locales.InspectorModified(), ID: "modified", Width: 190, MinWidth: 110},
		{Title: locales.McsMode(), ID: "mode", Width: 74, MinWidth: 50, Align: ui.End},
	}
}

func (a *app) mcsEnter() {
	m := &a.tl.mcs
	gen := m.gen + 1
	*m = mcsState{gen: gen, path: "/", cursor: -1}
	a.mcsLoadDaemons()
}

func (a *app) mcsLeave() { a.tl.mcs.gen++ }

func (a *app) mcsLoadDaemons() {
	m := &a.tl.mcs
	gen := m.gen + 1
	m.gen, m.loading, m.err = gen, true, ""
	acct := a.accountID
	a.bg(func() func() {
		ctx, cancel := a.timeout(30 * time.Second)
		defer cancel()
		ds, err := a.svc.MCSDaemons(ctx, acct)
		return func() {
			if gen != m.gen {
				return
			}
			m.loading = false
			if err != nil {
				if errors.Is(err, core.ErrMCSNeed2FA) {
					a.mcsAskCode(a.mcsLoadDaemons)
					return
				}
				m.err = mcsErrorText(err)
				return
			}
			m.daemons, m.daemonID = ds, ""
			m.instances, m.instanceID = nil, ""
			if len(ds) > 0 {
				m.daemonID = ds[0].ID
				a.mcsLoadInstances()
			}
		}
	})
}

func (a *app) mcsLoadInstances() {
	m := &a.tl.mcs
	gen := m.gen + 1
	m.gen, m.loading, m.err = gen, true, ""
	acct, daemon := a.accountID, m.daemonID
	a.bg(func() func() {
		ctx, cancel := a.timeout(30 * time.Second)
		defer cancel()
		ins, err := a.svc.MCSInstances(ctx, acct, daemon, 1, 50)
		return func() {
			if gen != m.gen {
				return
			}
			m.loading = false
			if err != nil {
				if errors.Is(err, core.ErrMCSNeed2FA) {
					a.mcsAskCode(a.mcsLoadInstances)
					return
				}
				m.err = mcsErrorText(err)
				return
			}
			m.instances, m.instanceID = ins, ""
			if len(ins) > 0 {
				m.instanceID = ins[0].UUID
				m.path = "/"
				a.mcsLoadFiles()
			}
		}
	})
}

func (a *app) mcsLoadFiles() {
	a.mcsFetchFiles(0, false)
}

func (a *app) mcsMoreFiles() {
	m := &a.tl.mcs
	if m.loading || len(m.files) >= m.fileTotal {
		return
	}
	a.mcsFetchFiles(m.filePage+1, true)
}

func (a *app) mcsFetchFiles(page int, appendRows bool) {
	m := &a.tl.mcs
	if m.daemonID == "" || m.instanceID == "" {
		return
	}
	gen := m.gen + 1
	m.gen, m.loading, m.err = gen, true, ""
	acct, daemon, instance, dir := a.accountID, m.daemonID, m.instanceID, m.path
	a.bg(func() func() {
		ctx, cancel := a.timeout(60 * time.Second)
		defer cancel()
		p, err := a.svc.MCSFiles(ctx, acct, daemon, instance, dir, page, 100, "")
		return func() {
			if gen != m.gen {
				return
			}
			m.loading = false
			if err != nil {
				if errors.Is(err, core.ErrMCSNeed2FA) {
					a.mcsAskCode(a.mcsLoadFiles)
					return
				}
				m.err = mcsErrorText(err)
				return
			}
			if appendRows {
				m.files = append(m.files, p.Entries...)
			} else {
				m.files = p.Entries
				m.sel.Clear()
				m.cursor = -1
			}
			m.path = p.Path
			m.filePage, m.fileTotal = p.Page, p.Total
		}
	})
}

func (a *app) mcsOpen(f core.MCSFile) {
	if !f.Folder {
		return
	}
	a.tl.mcs.path = f.Path
	a.mcsLoadFiles()
}

func (a *app) mcsUp() {
	m := &a.tl.mcs
	parent := mcsParent(m.path)
	if parent == m.path {
		return
	}
	m.path = parent
	a.mcsLoadFiles()
}

// mcsParent is the folder above p. Drive roots keep their slash.
func mcsParent(p string) string {
	normalized := strings.TrimRight(strings.ReplaceAll(p, "\\", "/"), "/")
	if mcsAtRoot(normalized) {
		return normalized
	}
	if normalized == "" {
		return "/"
	}
	parent := path.Dir(normalized)
	if parent == "." {
		return "/"
	}
	if len(parent) == 2 && parent[1] == ':' {
		return parent + "/"
	}
	return parent
}

func mcsAtRoot(p string) bool {
	p = strings.TrimRight(strings.ReplaceAll(p, "\\", "/"), "/")
	return p == "" || (len(p) == 2 && p[1] == ':')
}

func (a *app) mcsView(c *ui.Context) {
	k := tk(c)
	m := &a.tl.mcs
	ui.Column(c).Grow(1).MinHeight(0).Children(func() {
		ui.Row(c).Height(48).Padding(0, 16).Gap(8).AlignItems(ui.Center).Shrink(0).Children(func() {
			if len(m.daemons) > 0 {
				opts := make([]choice, 0, len(m.daemons))
				for _, d := range m.daemons {
					opts = append(opts, choice{d.ID, d.Name})
				}
				if selectBox(c, locales.McsNode(), &m.daemonID, opts, 200) {
					m.instances, m.instanceID, m.files = nil, "", nil
					m.sel.Clear()
					m.cursor = -1
					a.mcsLoadInstances()
				}
			}
			if len(m.instances) > 0 {
				opts := make([]choice, 0, len(m.instances))
				for _, in := range m.instances {
					opts = append(opts, choice{in.UUID, in.Name})
				}
				if selectBox(c, locales.McsInstance(), &m.instanceID, opts, 220) {
					m.path, m.files = "/", nil
					m.sel.Clear()
					m.cursor = -1
					a.mcsLoadFiles()
				}
			}
			if ghostButton(c, icons.Get("refresh-cw"), locales.CommonRefresh(), locales.CommonRefresh(), m.instanceID == "" || m.loading).Clicked() {
				if m.daemonID == "" {
					a.mcsLoadDaemons()
				} else if m.instanceID == "" {
					a.mcsLoadInstances()
				} else {
					a.mcsLoadFiles()
				}
			}
			if m.loading {
				ui.Spinner(c).Size(14, 14)
			}
			ui.Spacer(c)
			helpIcon(c, locales.McsHelp())
		})
		paneBar(c, "mcs-path", func() {
			up := ghostButton(c, icons.Get("arrow-up"), "", locales.McsUp(), m.instanceID == "" || mcsAtRoot(m.path))
			if up.Clicked() {
				a.mcsUp()
			}
			ui.Text(c, mcsDisplayPath(m.path)).FontSize(sizeSecondary).TextColor(k.TextMuted).SingleLine().Grow(1).MinWidth(0).Tooltip(m.path)
			if a.hasCap(core.CapMCSFileUpload) {
				if ghostButton(c, icons.Get("upload"), locales.ToolbarUpload(), "", m.instanceID == "").Clicked() {
					a.mcsPickUpload()
				}
			}
			if a.hasCap(core.CapMCSFileMkdir) {
				if ghostButton(c, icons.Get("folder-plus"), locales.CmdNewFolder(), "", m.instanceID == "").Clicked() {
					m.folderOpen, m.folderName, m.folderErr = true, "", ""
				}
			}
			if a.hasCap(core.CapMCSFileDownload) {
				if ghostButton(c, icons.Get("download"), locales.CmdDownload(), "", a.mcsSingleFile() == nil).Clicked() {
					a.mcsPickDownload()
				}
			}
			if a.hasCap(core.CapMCSFileDelete) {
				if ghostButton(c, icons.Get("trash-2"), locales.CommonDelete(), "", m.sel.Len() == 0).Clicked() {
					a.mcsAskDelete()
				}
			}
		})
		hairline(c)
		switch {
		case m.err != "":
			ui.Row(c).Margin(12).Padding(8, 12).Gap(10).AlignItems(ui.Center).Radius(6).Shrink(0).
				Background(k.Danger.Alpha(0.10)).Border(1, k.Danger.Alpha(0.35)).Children(func() {
				ui.Icon(c, icons.Get("circle-alert")).TextColor(k.Danger)
				ui.Text(c, m.err).Grow(1).MinWidth(0).TextColor(k.Text)
				retry := a.mcsLoadFiles
				if m.daemonID == "" {
					retry = a.mcsLoadDaemons
				} else if m.instanceID == "" {
					retry = a.mcsLoadInstances
				}
				secondaryButton(c, locales.CommonRetry(), false).OnClick(retry)
			})
		case m.loading && len(m.files) == 0:
			ui.Column(c).Grow(1).Center().Gap(10).Children(func() {
				ui.Spinner(c).Size(22, 22)
				mutedText(c, locales.CommonLoading())
			})
		case len(m.daemons) == 0:
			emptyBlock(c, "server", locales.McsNoNodes(), "", 0)
		case len(m.instances) == 0:
			emptyBlock(c, "server", locales.McsNoInstances(), "", 0)
		case len(m.files) == 0:
			empty := emptyBlock(c, "folder", locales.McsNoFiles(), "", 0)
			a.contextMenu(empty, menuSpec{name: locales.McsMenuActions()}, func(mb *menuBuilder) {
				m.sel.Clear()
				m.cursor = -1
				a.buildMCSMenu(mb)
			})
		default:
			files := m.files
			m.list.Key = func(i int) any { return files[i].Path }
			m.list.Label = func(i int) string { return files[i].Name }
			m.list.Selection = &m.sel
			m.list.Selected = &m.cursor
			m.list.DragSelect = true
			m.rowRects = make(map[string]ui.Rect, len(files))
			tbl := toolTable(c, &m.list, mcsColumns(), len(files), func(row, col int) {
				f := &files[row]
				switch col {
				case 0:
					cell := ui.Row(c).Gap(6).AlignItems(ui.Center)
					cell.Children(func() {
						ic := "file"
						if f.Folder {
							ic = "folder"
						}
						ui.Icon(c, icons.Get(ic)).FontSize(14).TextColor(k.TextMuted)
						ui.Text(c, f.Name).FontSize(sizeSecondary).TextColor(k.Text).SingleLine().Grow(1).MinWidth(0)
					})
					m.rowRects[f.Path] = cell.Bounds()
				case 1:
					if !f.Folder {
						secondaryCell(c, humanBytes(f.Size), true, ui.End)
					}
				case 2:
					secondaryCell(c, f.Modified, f.Modified != "", ui.Start)
				case 3:
					secondaryCell(c, strconv.Itoa(f.Mode), f.Mode != 0, ui.End)
				}
			})
			tbl.Grow(1).MinHeight(0).Label(locales.McsTableLabel())
			a.contextMenu(tbl, menuSpec{name: locales.McsMenuActions(), keyAt: a.mcsCursorCorner}, func(mb *menuBuilder) {
				if !mb.keyboard {
					if path, ok := m.rowAtPointer(tbl); ok {
						if !m.sel.Has(path) {
							m.sel.Clear()
							m.sel.Add(path)
							for i := range m.files {
								if m.files[i].Path == path {
									m.cursor = i
									break
								}
							}
						}
					} else {
						m.sel.Clear()
						m.cursor = -1
					}
				}
				a.buildMCSMenu(mb)
			})
			if tbl.Submitted() && m.cursor >= 0 && m.cursor < len(files) {
				a.mcsOpen(files[m.cursor])
			}
			if len(files) < m.fileTotal {
				ui.Row(c).Padding(0, 16, 8, 16).Shrink(0).Children(func() {
					if secondaryButton(c, locales.McsLoadMore(), m.loading).Clicked() {
						a.mcsMoreFiles()
					}
				})
			}
		}
	})
	a.mcsFolderDialog(c)
	a.mcsRenameDialog(c)
	a.mcsCodeDialog(c)
}

// mcsDisplayPath shows the instance-relative path, with "/" for the root.
func mcsDisplayPath(p string) string {
	if p == "" {
		return "/"
	}
	return p
}

func (a *app) mcsSingleFile() *core.MCSFile {
	m := &a.tl.mcs
	var found *core.MCSFile
	n := 0
	for i := range m.files {
		if m.files[i].Folder || !m.sel.Has(m.files[i].Path) {
			continue
		}
		found = &m.files[i]
		n++
	}
	if n != 1 {
		return nil
	}
	return found
}

func (a *app) mcsSelectedPaths() []string {
	m := &a.tl.mcs
	var out []string
	for _, f := range m.files {
		if m.sel.Has(f.Path) {
			out = append(out, f.Path)
		}
	}
	return out
}

func (a *app) mcsPickUpload() {
	m := &a.tl.mcs
	acct, daemon, instance, dir := a.accountID, m.daemonID, m.instanceID, m.path
	if instance == "" {
		return
	}
	go func() {
		paths := a.pick(false)
		if len(paths) == 0 {
			return
		}
		a.post(func() {
			a.mcsUpload(acct, daemon, instance, dir, paths)
		})
	}()
}

// mcsUpload queues the panel upload and asks for a two-factor code when the
// panel needs one before it will sign in.
func (a *app) mcsUpload(acct, daemon, instance, dir string, paths []string) {
	a.bg(func() func() {
		gid, err := a.svc.MCSUpload(a.ctx, acct, daemon, instance, dir, paths)
		return func() {
			if err != nil {
				if errors.Is(err, core.ErrMCSNeed2FA) {
					a.mcsAskCode(func() { a.mcsUpload(acct, daemon, instance, dir, paths) })
					return
				}
				a.toast(locales.McsFailed(mcsErrorText(err)))
				return
			}
			a.transfersOpen, a.transTab = true, 0
			a.addWatch(gid, &watch{
				kind: "mcs-upload", mcsAccount: acct, mcsDaemon: daemon, mcsInstance: instance,
			})
		}
	})
}

func (a *app) mcsPickDownload() {
	f := a.mcsSingleFile()
	if f == nil {
		return
	}
	m := &a.tl.mcs
	acct, daemon, instance, remote := a.accountID, m.daemonID, m.instanceID, f.Path
	go func() {
		dirs := a.pick(true)
		if len(dirs) == 0 {
			return
		}
		a.post(func() {
			a.bg(func() func() {
				local, err := a.svc.MCSDownload(a.ctx, acct, daemon, instance, remote, dirs[0])
				return func() {
					if err != nil {
						a.toast(locales.McsFailed(mcsErrorText(err)))
						return
					}
					a.toast(locales.McsDownloaded(filepath.Base(local)))
				}
			})
		})
	}()
}

func (a *app) mcsAskDelete() {
	targets := a.mcsSelectedPaths()
	if len(targets) == 0 {
		return
	}
	m := &a.tl.mcs
	acct, daemon, instance := a.accountID, m.daemonID, m.instanceID
	a.confirm = confirmState{
		open: true, title: locales.McsDeleteTitle(len(targets)), danger: true, action: locales.CommonDelete(),
		rows: []KV{
			{locales.CommonAccount(), a.account().Name},
			{locales.McsNode(), m.daemonName()},
			{locales.McsInstance(), m.instanceName()},
		},
		message: locales.McsDeleteMessage(),
		run: func() {
			a.bg(func() func() {
				err := a.svc.MCSDelete(a.ctx, acct, daemon, instance, targets)
				return func() {
					if err != nil {
						a.toast(locales.McsFailed(mcsErrorText(err)))
						return
					}
					a.toast(locales.McsDeleted(len(targets)))
					a.mcsLoadFiles()
				}
			})
		},
	}
}

func (a *app) mcsFolderDialog(c *ui.Context) {
	m := &a.tl.mcs
	if !m.folderOpen {
		return
	}
	dialogW(c, &m.folderOpen, locales.McsNewFolderTitle(), 400, func() {
		formField(c, locales.McsFolderName(), m.folderErr, func() {
			in := inputBox(c, &m.folderName, locales.McsFolderName(), "", inputOpts{invalid: m.folderErr != "", focus: true})
			if in.Submitted() {
				a.mcsCreateFolder()
			}
			if in.Changed() {
				m.folderErr = ""
			}
		})
	}, func() {
		buttonRow(c, func() {
			if secondaryButton(c, locales.CommonCancel(), false).Clicked() {
				m.folderOpen = false
			}
			if primaryButton(c, locales.CommonCreate(), m.folderBusy).Clicked() {
				a.mcsCreateFolder()
			}
		})
	})
}

func (a *app) mcsCreateFolder() {
	m := &a.tl.mcs
	if m.folderBusy || m.instanceID == "" {
		return
	}
	name := strings.TrimSpace(m.folderName)
	switch {
	case name == "":
		m.folderErr = locales.McsFolderNameRequired()
		return
	case strings.ContainsAny(name, "/\\") || name == "." || name == "..":
		m.folderErr = locales.McsFolderNameRequired()
		return
	}
	acct, daemon, instance := a.accountID, m.daemonID, m.instanceID
	target := joinMCSPath(m.path, name)
	m.folderBusy, m.folderErr = true, ""
	a.bg(func() func() {
		err := a.svc.MCSMkdir(a.ctx, acct, daemon, instance, target)
		return func() {
			m.folderBusy = false
			if err != nil {
				m.folderErr = mcsErrorText(err)
				return
			}
			m.folderOpen = false
			a.toast(locales.McsFolderCreated(name))
			a.mcsLoadFiles()
		}
	})
}

// joinMCSPath appends one name to a daemon path.
func joinMCSPath(dir, name string) string {
	dir = strings.TrimRight(dir, "/\\")
	if dir == "" {
		return "/" + name
	}
	return dir + "/" + name
}

// mcsAskCode asks for a two-factor code, then runs retry once the panel has
// the code for its next sign-in.
func (a *app) mcsAskCode(retry func()) {
	m := &a.tl.mcs
	m.codeOpen, m.code, m.codeErr, m.codeBusy = true, "", "", false
	m.codeRetry = retry
}

func (a *app) mcsSubmitCode() {
	m := &a.tl.mcs
	if m.codeBusy {
		return
	}
	code := strings.TrimSpace(m.code)
	if code == "" {
		m.codeErr = locales.McsCodeRequired()
		return
	}
	acct := a.accountID
	m.codeBusy, m.codeErr = true, ""
	a.bg(func() func() {
		err := a.svc.SetMCSLoginCode(a.ctx, acct, code)
		return func() {
			m.codeBusy = false
			if err != nil {
				m.codeErr = mcsErrorText(err)
				return
			}
			retry := m.codeRetry
			m.codeOpen, m.codeRetry = false, nil
			if retry != nil {
				retry()
			}
		}
	})
}

func (a *app) mcsCodeDialog(c *ui.Context) {
	m := &a.tl.mcs
	if !m.codeOpen {
		return
	}
	dialogW(c, &m.codeOpen, locales.McsCodeTitle(), 420, func() {
		formField(c, locales.McsCodeTitle(), m.codeErr, func() {
			in := inputBox(c, &m.code, locales.McsCodeTitle(), "", inputOpts{invalid: m.codeErr != "", focus: true})
			if in.Submitted() {
				a.mcsSubmitCode()
			}
			if in.Changed() {
				m.codeErr = ""
			}
		})
		mutedText(c, locales.McsCodeHelp())
	}, func() {
		buttonRow(c, func() {
			if secondaryButton(c, locales.CommonCancel(), false).Clicked() {
				m.codeOpen, m.codeRetry = false, nil
			}
			if primaryButton(c, locales.McsCodeSubmit(), m.codeBusy).Clicked() {
				a.mcsSubmitCode()
			}
		})
	})
}

// rowAtPointer returns the file row under the pointer, if any.
func (m *mcsState) rowAtPointer(tbl ui.Element) (string, bool) {
	_, py, over := tbl.PointerPosition()
	if !over {
		return "", false
	}
	b := tbl.Bounds()
	for path, r := range m.rowRects {
		y := r.Y - b.Y
		if py >= y && py < y+r.H {
			return path, true
		}
	}
	return "", false
}

// mcsCursorCorner is where Shift+F10 opens the file menu: below the name of
// the row the keyboard is on.
func (a *app) mcsCursorCorner() (x, y float32, ok bool) {
	m := &a.tl.mcs
	if m.cursor < 0 || m.cursor >= len(m.files) {
		return 0, 0, false
	}
	r, ok := m.rowRects[m.files[m.cursor].Path]
	if !ok || r.W <= 0 {
		return 0, 0, false
	}
	return r.X, r.Y + r.H, true
}

// singleSelected returns the one selected file, or nil.
func (m *mcsState) singleSelected() (*core.MCSFile, bool) {
	if m.sel.Len() != 1 {
		return nil, false
	}
	for i := range m.files {
		if m.sel.Has(m.files[i].Path) {
			return &m.files[i], true
		}
	}
	return nil, false
}

func (a *app) mcsSelectAll() {
	m := &a.tl.mcs
	for _, f := range m.files {
		m.sel.Add(f.Path)
	}
	if len(m.files) > 0 {
		m.cursor = len(m.files) - 1
	}
}

// buildMCSMenu fills the file table's context menu.
func (a *app) buildMCSMenu(mb *menuBuilder) {
	m := &a.tl.mcs
	one, hasOne := m.singleSelected()

	open := mb.Item(locales.CmdOpen(), func() {
		if f, ok := m.singleSelected(); ok {
			a.mcsOpen(*f)
		}
	}).Icon("folder-open")
	open.Disabled(!hasOne || !one.Folder)

	down := mb.Item(locales.CmdDownload(), a.mcsPickDownload).Icon("download")
	down.Disabled(a.mcsSingleFile() == nil)

	ren := mb.Item(locales.CmdRename(), a.mcsAskRename).Icon("pencil")
	ren.Disabled(!hasOne)

	cp := mb.Item(locales.McsCopyPath(), func() {
		if f, ok := m.singleSelected(); ok {
			a.copyText(f.Path)
			a.toast(locales.McsPathCopied())
		}
	}).Icon("link")
	cp.Disabled(!hasOne)

	mb.Separator()
	mb.Item(locales.CmdUploadFiles(), a.mcsPickUpload).Icon("upload")
	mb.Item(locales.CmdNewFolder(), func() {
		m.folderOpen, m.folderName, m.folderErr = true, "", ""
	}).Icon("folder-plus")

	mb.Separator()
	del := mb.Item(locales.CommonDelete(), a.mcsAskDelete).Icon("trash-2")
	del.Disabled(m.sel.Len() == 0)

	mb.Separator()
	mb.Item(locales.CommonRefresh(), a.mcsLoadFiles).Icon("refresh-cw")
	mb.Item(locales.CmdSelectAll(), a.mcsSelectAll).Icon("check-square")
}

func (a *app) mcsAskRename() {
	m := &a.tl.mcs
	f, ok := m.singleSelected()
	if !ok {
		return
	}
	m.renameOpen, m.renamePath, m.renameName, m.renameErr = true, f.Path, f.Name, ""
}

func (a *app) mcsRenameDialog(c *ui.Context) {
	m := &a.tl.mcs
	if !m.renameOpen {
		return
	}
	dialogW(c, &m.renameOpen, locales.McsRenameTitle(), 400, func() {
		formField(c, locales.McsRenameName(), m.renameErr, func() {
			in := inputBox(c, &m.renameName, locales.McsRenameName(), "", inputOpts{invalid: m.renameErr != "", focus: true})
			if in.Submitted() {
				a.mcsRename()
			}
			if in.Changed() {
				m.renameErr = ""
			}
		})
	}, func() {
		buttonRow(c, func() {
			if secondaryButton(c, locales.CommonCancel(), false).Clicked() {
				m.renameOpen = false
			}
			if primaryButton(c, locales.CmdRename(), m.renameBusy).Clicked() {
				a.mcsRename()
			}
		})
	})
}

func (a *app) mcsRename() {
	m := &a.tl.mcs
	if m.renameBusy || m.instanceID == "" {
		return
	}
	name := strings.TrimSpace(m.renameName)
	switch {
	case name == "":
		m.renameErr = locales.McsFolderNameRequired()
		return
	case strings.ContainsAny(name, "/\\") || name == "." || name == "..":
		m.renameErr = locales.McsFolderNameRequired()
		return
	}
	old := m.renamePath
	dest := joinMCSPath(mcsParent(old), name)
	if dest == old {
		m.renameOpen = false
		return
	}
	for _, f := range m.files {
		if f.Path == dest {
			m.renameErr = locales.McsNameExists()
			return
		}
	}
	acct, daemon, instance := a.accountID, m.daemonID, m.instanceID
	m.renameBusy, m.renameErr = true, ""
	a.bg(func() func() {
		err := a.svc.MCSMove(a.ctx, acct, daemon, instance, [][2]string{{old, dest}})
		return func() {
			m.renameBusy = false
			if err != nil {
				if errors.Is(err, core.ErrMCSNeed2FA) {
					a.mcsAskCode(a.mcsRename)
					return
				}
				m.renameErr = mcsErrorText(err)
				return
			}
			m.renameOpen = false
			a.toast(locales.McsRenamed(name))
			a.mcsLoadFiles()
		}
	})
}

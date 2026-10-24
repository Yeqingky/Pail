package main

import (
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

	path   string
	files  []core.MCSFile
	sel    ui.Selection[string]
	list   ui.ListState
	cursor int

	loading bool
	err     string
	gen     int

	folderOpen bool
	folderName string
	folderErr  string
	folderBusy bool
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
				m.err = humanError(err)
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
				m.err = humanError(err)
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
		p, err := a.svc.MCSFiles(ctx, acct, daemon, instance, dir, 0, 50, "")
		return func() {
			if gen != m.gen {
				return
			}
			m.loading = false
			if err != nil {
				m.err = humanError(err)
				return
			}
			m.files, m.path = p.Entries, p.Path
			m.sel.Clear()
			m.cursor = -1
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
			emptyBlock(c, "folder", locales.McsNoFiles(), "", 0)
		default:
			files := m.files
			m.list.Key = func(i int) any { return files[i].Path }
			m.list.Label = func(i int) string { return files[i].Name }
			m.list.Selection = &m.sel
			m.list.Selected = &m.cursor
			tbl := toolTable(c, &m.list, mcsColumns(), len(files), func(row, col int) {
				f := &files[row]
				switch col {
				case 0:
					ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
						ic := "file"
						if f.Folder {
							ic = "folder"
						}
						ui.Icon(c, icons.Get(ic)).FontSize(14).TextColor(k.TextMuted)
						ui.Text(c, f.Name).FontSize(sizeSecondary).TextColor(k.Text).SingleLine().Grow(1).MinWidth(0)
					})
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
			if tbl.Submitted() && m.cursor >= 0 && m.cursor < len(files) {
				a.mcsOpen(files[m.cursor])
			}
		}
	})
	a.mcsFolderDialog(c)
}

// mcsDisplayPath shows the panel's absolute path, with "/" for the root.
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
			a.bg(func() func() {
				n, err := a.svc.MCSUpload(a.ctx, acct, daemon, instance, dir, paths)
				return func() {
					if err != nil {
						a.toast(locales.McsFailed(humanError(err)))
						return
					}
					a.toast(locales.McsUploaded(n))
					a.mcsLoadFiles()
				}
			})
		})
	}()
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
						a.toast(locales.McsFailed(humanError(err)))
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
						a.toast(locales.McsFailed(humanError(err)))
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
				m.folderErr = humanError(err)
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

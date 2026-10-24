package main

import (
	"strings"

	"github.com/DejavuMoe/Pail/cmd/pail-desktop/icons"
	"github.com/DejavuMoe/Pail/cmd/pail-desktop/locales"
	core "github.com/DejavuMoe/Pail/internal/app"
	"github.com/egoist/mygo/ui"
)

var toolIDs = []string{"sync", "cloudfront", "mcs", "rules", "mime", "multipart", "settings"}

// sidebar is the account switcher, the bucket list and the tool list.
func (a *app) sidebar(c *ui.Context) {
	k := tk(c)
	ui.Column(c).Fill().MinWidth(0).MinHeight(0).Background(k.Sidebar).Children(func() {
		a.accountSwitcher(c)
		if a.mcsMode() {
			a.mcsNav(c)
		} else {
			a.bucketList(c)
		}
		a.toolList(c)
	})
}

// accountSwitcher is the sidebar's 44px header, level with the toolbar. On
// macOS the traffic lights take the room c.TitleBar().Left at its left end;
// that room, like any free space here, drags the window.
func (a *app) accountSwitcher(c *ui.Context) {
	bar := c.TitleBar()
	ui.Row(c).Height(topBarH).Shrink(0).AlignItems(ui.Center).DragWindow().Children(func() {
		if bar.Left > 0 {
			ui.Box(c).Width(bar.Left).Height(topBarH).Shrink(0)
		}
		a.accountButton(c)
	})
}

// accountButton is one 44px hover target: initial tile, name over region,
// chevron. It opens the account menu.
func (a *app) accountButton(c *ui.Context) {
	k := tk(c)
	ac := a.account()
	name, sub := ac.Name, ac.Region
	if sub == "" {
		sub = ac.Endpoint
	}
	if ac.ID == "" {
		name, sub = locales.SidebarNoAccount(), locales.SidebarAddOneToStart()
	}
	btn := ui.ButtonBase(c.Key("account")).Label(locales.CommonAccount()).Tooltip(locales.SidebarSwitchAccount()).Height(topBarH).Grow(1).MinWidth(0).Padding(0, 12).Gap(8).AlignItems(ui.Center).Transition(hoverFade())
	if btn.Hovered() {
		btn.Background(k.SurfaceHover)
	}
	btn.Children(func() {
		ui.Box(c).Size(24, 24).Radius(6).Background(k.ActiveFill).Center().Shrink(0).Children(func() {
			ui.Text(c, initialOf(name)).FontSize(sizeSecondary).FontWeight(600).TextColor(k.Text)
		})
		ui.Column(c).Grow(1).MinWidth(0).Children(func() {
			ui.Text(c, name).FontSize(sizeBody).FontWeight(500).TextColor(k.Text).SingleLine()
			if ac.SecretMissing {
				secretBadge(c)
			} else {
				ui.Text(c, sub).FontSize(sizeLabel).TextColor(k.TextSubtle).SingleLine()
			}
		})
		ui.Icon(c, icons.Get("chevron-down")).FontSize(14).TextColor(k.TextMuted)
	})
	a.buttonMenu(c, btn, "account", locales.CommonAccount(), func(m *menuBuilder) {
		for _, x := range a.accounts {
			label := x.Name
			if x.SecretMissing {
				label = locales.SidebarAccountSecretMissing(x.Name)
			}
			m.Item(label, func() { a.selectAccount(x.ID) }).Icon("user").Checked(x.ID == a.accountID)
		}
		m.Separator()
		m.Item(locales.SidebarAddAccount(), func() { a.openAccountForm(false) }).Icon("plus")
		m.Item(locales.SidebarEditAccount(), func() { a.openAccountForm(true) }).Icon("pencil").Disabled(ac.ID == "")
		m.Item(locales.SidebarRemoveAccount(), a.askRemoveAccount).Icon("trash-2").Disabled(ac.ID == "")
	})
}

// secretBadge replaces the region line of an account that lost its secret: a
// Warning-tinted pill with a triangle icon (icon, fill and words; the text keeps
// the Text color for contrast).
func secretBadge(c *ui.Context) {
	k := tk(c)
	ui.Row(c).Height(16).Padding(0, 5).Gap(3).Radius(4).AlignItems(ui.Center).AlignSelf(ui.Start).
		Background(k.Warning.Alpha(0.14)).Border(1, k.Warning.Alpha(0.45)).Children(func() {
		ui.Icon(c, icons.Get("triangle-alert")).FontSize(10).TextColor(k.Warning)
		ui.Text(c, locales.SidebarSecretMissing()).FontSize(sizeLabel).FontWeight(500).TextColor(k.Text).SingleLine()
	})
}

func initialOf(name string) string {
	for _, r := range name {
		return strings.ToUpper(string(r))
	}
	return "?"
}

// sidebarLabel is a section label row: 16px side padding, 16 above, 6 below,
// with an optional count in TextSubtle.
func sidebarLabel(c *ui.Context, title string, count int, trailing func()) {
	k := tk(c)
	ui.Row(c).Padding(16, 16, 6, 16).Gap(6).AlignItems(ui.Center).Children(func() {
		sectionLabel(c, title)
		if count > 0 {
			ui.Text(c, groupInt(count)).FontSize(sizeLabel).TextColor(k.TextSubtle)
		}
		ui.Spacer(c)
		if trailing != nil {
			trailing()
		}
	})
}

// sidebarItem is one 28px source-list row. The row sits 8px in from the
// sidebar edge with 8px padding, so icons line up with the section labels.
// A disabled row is dimmed and does nothing.
func (a *app) sidebarItem(c *ui.Context, id string, icon *ui.SVG, name, region string, disabled bool) ui.Element {
	k := tk(c)
	on := a.navSel == id
	row := ui.ButtonBase(c.Key(id)).Label(name).Height(28).Margin(0, 8).Padding(0, 8).Gap(8).Radius(6).AlignItems(ui.Center).Disabled(disabled).Transition(hoverFade())
	switch {
	case disabled:
		row.Opacity(0.4)
	case on:
		row.Background(k.ActiveFill)
	case row.Hovered():
		row.Background(k.SurfaceHover)
	}
	row.Children(func() {
		ic := k.TextMuted
		w := 400
		if on && !disabled {
			ic, w = k.Accent, 600
			selectionBar(c, 6, "sidebar")
		}
		ui.Icon(c, icon).FontSize(14).TextColor(ic)
		ui.Text(c, name).FontSize(sizeBody).FontWeight(w).TextColor(k.Text).SingleLine().Grow(1).MinWidth(0)
		if region != "" {
			rc := k.TextSubtle
			if on && !disabled {
				rc = k.TextMuted // TextSubtle on ActiveFill is under 3:1
			}
			ui.Text(c, region).FontSize(sizeLabel).TextColor(rc).SingleLine().Shrink(0)
		}
	})
	if !disabled && row.Clicked() {
		a.navSel = id
		a.navigate()
	}
	return row
}

func (a *app) bucketList(c *ui.Context) {
	if !a.hasCap(core.CapBucketList) {
		return
	}
	k := tk(c)
	ui.Column(c).Grow(1).MinHeight(0).Children(func() {
		sidebarLabel(c, locales.SidebarBuckets(), len(a.buckets), func() {
			if a.bucketsLoad {
				ui.Spinner(c).Size(12, 12)
			}
			if a.hasAccount() {
				if iconButton(c, icons.Get("plus"), locales.SidebarNewBucket()).Clicked() {
					a.openNewBucket()
				}
			}
		})
		ui.Box(c).Margin(0, 12, 8, 12).Children(func() {
			searchBox(c, &a.bucketFilter, &a.filterH, locales.SidebarFilterBuckets(), locales.SidebarFilterBuckets(), 0, icons.Get("search"), icons.Get("x"))
		})
		if a.bucketsErr != "" {
			ui.Column(c).Margin(0, 12, 8, 12).Padding(8, 10).Gap(6).Radius(6).
				Background(k.Danger.Alpha(0.10)).Border(1, k.Danger.Alpha(0.35)).Children(func() {
				ui.Row(c).Gap(6).AlignItems(ui.Start).Children(func() {
					ui.Icon(c, icons.Get("circle-alert")).FontSize(14).TextColor(k.Danger).Shrink(0).MarginY(1)
					ui.Text(c, a.bucketsErrText()).FontSize(sizeSecondary).TextColor(k.Text).Grow(1).MinWidth(0)
				})
				ui.Row(c).Justify(ui.End).Children(func() {
					switch a.bucketsFix {
					case "unlock":
						secondaryButton(c, locales.SidebarUnlock(), a.bucketsLoad).OnClick(func() {
							id := a.accountID
							a.askUnlock("buckets", func() {
								if a.accountID == id {
									a.loadBuckets(true)
								}
							})
						})
					case "secret":
						secondaryButton(c, locales.SidebarEnterSecret(), false).OnClick(func() { a.openAccountForm(true) })
					default:
						secondaryButton(c, locales.CommonRetry(), a.bucketsLoad).OnClick(func() { a.loadBuckets(true) })
					}
				})
			})
		}
		ui.Scroll(c).Grow(1).MinHeight(0).ScrollbarInsets(0, 2, 0, 0).Children(func() {
			ui.Column(c).Gap(0).Children(func() {
				filter := strings.ToLower(strings.TrimSpace(a.bucketFilter))
				shown := 0
				for _, b := range a.buckets {
					if filter != "" && !strings.Contains(strings.ToLower(b.Name), filter) {
						continue
					}
					shown++
					tip := b.Name
					if b.Region != "" {
						tip += " · " + b.Region
					}
					item := a.sidebarItem(c, "b:"+b.Name, icons.Get("database"), b.Name, b.Region, false).Tooltip(tip)
					a.contextMenu(item, menuSpec{name: locales.MenuBucketActions(b.Name)}, func(m *menuBuilder) {
						name := b.Name
						m.Item(locales.SidebarOpen(), func() { a.openBucket(name) }).Icon("folder-open")
						if a.hasCap(core.CapBucketConfig) {
							m.Item(locales.SidebarBucketSettings(), func() { a.openBucketSettings(name) }).Icon("sliders-horizontal")
						}
						m.Separator()
						if a.hasCap(core.CapObjectDownload) {
							m.Item(locales.SidebarDownloadBucket(), func() { a.downloadBucket(name) }).Icon("download")
						}
						if a.hasCap(core.CapObjectCopy) {
							m.Item(locales.SidebarCopyContents(), func() { a.openBucketCopy(name, false) }).Icon("copy")
						}
						if a.hasCap(core.CapObjectMove) {
							m.Item(locales.SidebarMoveContents(), func() { a.openBucketCopy(name, true) }).Icon("arrow-left-right")
						}
						m.Separator()
						if a.hasCap(core.CapObjectStorageClass) {
							m.Item(locales.SidebarChangeClass(), func() {
								a.openRewrite(false, rewriteScope{loc: a.bucketRoot(name), all: true})
							}).Icon("layers")
						}
						if a.hasCap(core.CapObjectEncryption) {
							m.Item(locales.SidebarChangeEncryption(), func() {
								a.openRewrite(true, rewriteScope{loc: a.bucketRoot(name), all: true})
							}).Icon("file-lock")
						}
						if a.cfAvailable() {
							m.Item(locales.SidebarInvalidateCloudFront(), func() { a.invalidateBucket(name) }).Icon("cloud")
						}
						m.Separator()
						if a.hasCap(core.CapBucketEmpty) {
							m.Item(locales.SidebarEmptyBucket(), func() { a.openEmptyBucket(name) }).Icon("trash-x")
						}
						if a.hasCap(core.CapBucketDelete) {
							m.Item(locales.SidebarDeleteBucket(), func() { a.openDeleteBucket(name) }).Icon("trash-2")
						}
						m.Separator()
						m.Item(locales.SidebarCopyName(), func() {
							a.copyText(name)
							a.toast(locales.SidebarBucketNameCopied())
						}).Icon("tag")
					})
				}
				if shown == 0 && len(a.buckets) > 0 {
					ui.Text(c, locales.SidebarNoMatching()).FontSize(sizeSecondary).TextColor(k.TextMuted).Padding(8, 16)
				}
				if len(a.buckets) == 0 && !a.bucketsLoad && a.bucketsErr == "" && a.hasAccount() {
					ui.Text(c, locales.SidebarNoBuckets()).FontSize(sizeSecondary).TextColor(k.TextMuted).Padding(8, 16)
				}
			})
		})
	})
}

func (a *app) toolList(c *ui.Context) {
	a.toolSync()
	ui.Column(c).Padding(0, 0, 8, 0).Children(func() {
		sidebarLabel(c, locales.SidebarTools(), 0, nil)
		for _, id := range toolIDs {
			if id == "mcs" && a.mcsMode() {
				continue // the browser is the account's main view
			}
			cmd := a.cmd("tool." + id)
			if !a.visible(cmd) {
				continue
			}
			a.sidebarItem(c, "t:"+id, icons.Get(cmd.Icon), cmd.text(), "", !a.enabled(cmd))
		}
	})
}

// navigate reacts to a new sidebar choice.
func (a *app) navigate() {
	switch {
	case strings.HasPrefix(a.navSel, "b:"):
		a.openBucket(strings.TrimPrefix(a.navSel, "b:"))
	case strings.HasPrefix(a.navSel, "t:"):
		a.openTool(strings.TrimPrefix(a.navSel, "t:"))
	}
}

// bucketsErrText is the error line of the bucket list. The missing-secret
// message is said again every frame, so it follows a language change.
func (a *app) bucketsErrText() string {
	if a.bucketsFix == "secret" {
		return locales.StateBucketsSecretMissing()
	}
	return a.bucketsErr
}

// mcsMode reports whether the account browses an MCSManager panel instead of
// S3 buckets. Its node and instance tree takes the bucket list's place.
func (a *app) mcsMode() bool {
	return core.ProviderHasCapability(a.account().Provider, core.CapMCSInstanceList)
}

// mcsNav is the panel node and instance list, with the same filter box as the
// bucket list.
func (a *app) mcsNav(c *ui.Context) {
	a.mcsEnsure()
	m := &a.tl.mcs
	k := tk(c)
	total := 0
	for _, n := range m.tree {
		total += len(n.Instances)
	}
	ui.Column(c).Grow(1).MinHeight(0).Children(func() {
		sidebarLabel(c, locales.SidebarInstances(), total, func() {
			if m.loading {
				ui.Spinner(c).Size(12, 12)
			}
			if iconButton(c, icons.Get("refresh-cw"), locales.CommonRefresh()).Clicked() {
				a.mcsLoadDaemons()
			}
		})
		ui.Box(c).Margin(0, 12, 8, 12).Children(func() {
			searchBox(c, &a.mcsFilter, &a.mcsFilterH, locales.SidebarFilterInstances(), locales.SidebarFilterInstances(), 0, icons.Get("search"), icons.Get("x"))
		})
		if m.navErr != "" {
			ui.Column(c).Margin(0, 12, 8, 12).Padding(8, 10).Gap(6).Radius(6).
				Background(k.Danger.Alpha(0.10)).Border(1, k.Danger.Alpha(0.35)).Children(func() {
				ui.Row(c).Gap(6).AlignItems(ui.Start).Children(func() {
					ui.Icon(c, icons.Get("circle-alert")).FontSize(14).TextColor(k.Danger).Shrink(0).MarginY(1)
					ui.Text(c, m.navErr).FontSize(sizeSecondary).TextColor(k.Text).Grow(1).MinWidth(0)
				})
				ui.Row(c).Justify(ui.End).Children(func() {
					secondaryButton(c, locales.CommonRetry(), m.loading).OnClick(a.mcsLoadDaemons)
				})
			})
		}
		ui.Scroll(c).Grow(1).MinHeight(0).ScrollbarInsets(0, 2, 0, 0).Children(func() {
			ui.Column(c).Gap(0).Children(func() {
				filter := strings.ToLower(strings.TrimSpace(a.mcsFilter))
				shown := 0
				for _, n := range m.tree {
					nodeMatch := filter == "" || strings.Contains(strings.ToLower(n.Daemon.Name), filter)
					var instances []core.MCSInstance
					for _, in := range n.Instances {
						if filter == "" || nodeMatch || strings.Contains(strings.ToLower(in.Name), filter) {
							instances = append(instances, in)
						}
					}
					if !nodeMatch && len(instances) == 0 {
						continue
					}
					shown++
					ui.Row(c).Padding(16, 10, 2, 16).Gap(6).AlignItems(ui.Center).Children(func() {
						ui.Icon(c, icons.Get("server")).FontSize(12).TextColor(k.TextMuted)
						ui.Text(c, n.Daemon.Name).FontSize(sizeLabel).FontWeight(500).TextColor(k.TextMuted).SingleLine().Grow(1).MinWidth(0)
					})
					if n.Err != "" {
						ui.Text(c, n.Err).FontSize(sizeLabel).TextColor(k.Danger).Padding(8, 16).SingleLine()
					}
					for _, in := range instances {
						a.mcsSidebarItem(c, n.Daemon, in)
					}
				}
				if shown == 0 && len(m.tree) > 0 {
					ui.Text(c, locales.SidebarNoMatchingInstances()).FontSize(sizeSecondary).TextColor(k.TextMuted).Padding(8, 16)
				}
				if len(m.tree) == 0 && !m.loading && m.navErr == "" {
					ui.Text(c, locales.SidebarNoInstances()).FontSize(sizeSecondary).TextColor(k.TextMuted).Padding(8, 16)
				}
			})
		})
	})
}

// mcsSidebarItem is one 28px instance row under its node heading.
func (a *app) mcsSidebarItem(c *ui.Context, d core.MCSDaemon, in core.MCSInstance) {
	k := tk(c)
	m := &a.tl.mcs
	on := m.daemonID == d.ID && m.instanceID == in.UUID
	row := ui.ButtonBase(c.Key("mcs:"+d.ID+"/"+in.UUID)).Label(in.Name).Height(28).Margin(0, 8).Padding(0, 8).Gap(8).Radius(6).AlignItems(ui.Center).Transition(hoverFade())
	switch {
	case on:
		row.Background(k.ActiveFill)
	case row.Hovered():
		row.Background(k.SurfaceHover)
	}
	row.Children(func() {
		ic := k.TextMuted
		w := 400
		if on {
			ic, w = k.Accent, 600
			selectionBar(c, 6, "sidebar")
		}
		ui.Icon(c, icons.Get("server")).FontSize(14).TextColor(ic)
		ui.Text(c, in.Name).FontSize(sizeBody).FontWeight(w).TextColor(k.Text).SingleLine().Grow(1).MinWidth(0)
	})
	row.Tooltip(d.Name + " · " + in.Name)
	if row.Clicked() {
		a.mcsSelectInstance(d.ID, in.UUID)
	}
}

// mcsBrowser reports whether the main area and inspector show panel files.
func (a *app) mcsBrowser() bool {
	return a.mcsMode() && a.tool == ""
}

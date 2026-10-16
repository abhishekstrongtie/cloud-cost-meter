//go:build darwin

package ui

import (
	"os/exec"
	"sync"
	"time"

	"fyne.io/systray"

	"costmeter/internal/settings"
)

const costManagementURL = "https://portal.azure.com/#view/Microsoft_Azure_CostManagement/Menu/~/overview"

// Actions are what the menu can make the rest of the app do. Each must return quickly: the
// menu library drops a click that arrives while the previous one is still being handled.
type Actions struct {
	RefreshNow  func()
	Pick        func(Change)        // a choice in the Scope submenu
	ResetScopes func()              // "Track all subscriptions"
	SetRefresh  func(time.Duration) // a choice under Settings › Refresh every
	SetServices func(int)           // a choice under Settings › Services listed
	OnExit      func()
}

// slot is one pre-allocated menu item. It remembers what it last showed, so Show only talks to
// the menu library about what actually changed.
type slot struct {
	item     *systray.MenuItem
	title    string
	shown    bool
	disabled bool
	checked  bool
}

// newSlot wraps a freshly added item and hides it until the model needs it.
func newSlot(item *systray.MenuItem) *slot {
	item.Hide()
	return &slot{item: item}
}

func (s *slot) set(title string, shown, disabled bool) {
	if !shown {
		if s.shown {
			s.item.Hide()
			s.shown = false
		}
		return
	}
	if s.title != title {
		s.item.SetTitle(title)
		s.title = title
	}
	if s.disabled != disabled {
		if disabled {
			s.item.Disable()
		} else {
			s.item.Enable()
		}
		s.disabled = disabled
	}
	if !s.shown {
		s.item.Show()
		s.shown = true
	}
}

func (s *slot) setChecked(checked bool) {
	if s.checked == checked {
		return
	}
	if checked {
		s.item.Check()
	} else {
		s.item.Uncheck()
	}
	s.checked = checked
}

type scopeSlots struct {
	parent   *slot
	services [MaxServiceLines]*slot
	portal   *slot
	url      string // read by the click handler, written by Show
}

type pickerSlots struct {
	parent  *slot
	options [2 + MaxPickerGroups]*slot
	more    *slot
	changes [2 + MaxPickerGroups]Change
}

// Tray is the menu bar item and its menu.
type Tray struct {
	act   Actions
	title string

	mu        sync.Mutex // guards the click targets (url, changes), which Show writes and clicks read, and aboutOpen
	aboutOpen bool       // the About window is showing
	top       [MaxTop]*slot
	scopes    [MaxScopes]scopeSlots
	note      *slot
	bottom    [MaxBottom]*slot
	status    *slot
	picker    [MaxPickerSubscriptions]pickerSlots
	pickNone  *slot // "No subscriptions known yet"
	moreSubs  *slot
	reset     *slot

	refreshParent   *slot
	refreshOptions  []*slot
	servicesParent  *slot
	servicesOptions []*slot
}

// Run starts the menu bar app and blocks until Quit is chosen. It must be called from the
// main goroutine. ready is called once the tray exists.
func Run(act Actions, ready func(*Tray)) {
	systray.Run(func() { ready(newTray(act)) }, func() {
		if act.OnExit != nil {
			act.OnExit()
		}
	})
}

// onClick keeps one goroutine always waiting on an item's clicks.
func onClick(item *systray.MenuItem, f func()) {
	go func() {
		for range item.ClickedCh {
			f()
		}
	}()
}

func open(args ...string) {
	cmd := exec.Command("open", args...)
	if err := cmd.Start(); err == nil {
		go cmd.Wait() // reap it
	}
}

// showAbout opens the About window and returns when it is closed. A second click while it is
// open does nothing. It blocks, so callers run it in its own goroutine.
func (t *Tray) showAbout() {
	t.mu.Lock()
	if t.aboutOpen {
		t.mu.Unlock()
		return
	}
	t.aboutOpen = true
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		t.aboutOpen = false
		t.mu.Unlock()
	}()
	script := `display dialog "` + AboutText(Version) + `" with title "About CostMeter" ` +
		`buttons {"OK"} default button "OK" with icon note`
	_ = exec.Command("osascript", "-e", script).Run()
}

func newTray(act Actions) *Tray {
	t := &Tray{act: act}
	systray.SetTemplateIcon(cloudTemplate, cloudTemplate)
	systray.SetTitle("…")
	t.title = "…"
	systray.SetTooltip("CostMeter: Azure cost this month")

	for i := range t.top {
		t.top[i] = newSlot(systray.AddMenuItem("", ""))
	}
	systray.AddSeparator()

	for i := range t.scopes {
		row := &t.scopes[i]
		row.parent = newSlot(systray.AddMenuItem("", ""))
		for j := range row.services {
			row.services[j] = newSlot(row.parent.item.AddSubMenuItem("", ""))
		}
		row.portal = newSlot(row.parent.item.AddSubMenuItem("Open in Azure Portal", ""))
		onClick(row.portal.item, func() {
			t.mu.Lock()
			url := row.url
			t.mu.Unlock()
			if url != "" {
				open(url)
			}
		})
	}
	t.note = newSlot(systray.AddMenuItem("", ""))
	systray.AddSeparator()

	for i := range t.bottom {
		t.bottom[i] = newSlot(systray.AddMenuItem("", ""))
	}
	systray.AddSeparator()

	t.status = newSlot(systray.AddMenuItem("", ""))
	refresh := systray.AddMenuItem("Refresh Now", "Fetch the latest cost data")
	onClick(refresh, func() {
		if act.RefreshNow != nil {
			act.RefreshNow()
		}
	})
	t.buildPicker()
	t.buildSettings()

	costMgmt := systray.AddMenuItem("Open Azure Cost Management", "")
	onClick(costMgmt, func() { open(costManagementURL) })

	about := systray.AddMenuItem("About CostMeter", "About the app and its developer")
	onClick(about, func() { go t.showAbout() })

	systray.AddSeparator()
	quit := systray.AddMenuItem("Quit CostMeter", "")
	onClick(quit, systray.Quit)
	return t
}

// buildPicker creates the Scope submenu: for each subscription, a submenu of checkable choices.
func (t *Tray) buildPicker() {
	scope := systray.AddMenuItem("Scope", "Choose subscriptions and resource groups to track")

	none := scope.AddSubMenuItem("No subscriptions known yet", "")
	none.Disable()
	t.pickNone = &slot{item: none, title: "No subscriptions known yet", shown: true, disabled: true}

	for i := range t.picker {
		ps := &t.picker[i]
		ps.parent = newSlot(scope.AddSubMenuItem("", ""))
		for j := range ps.options {
			ps.options[j] = newSlot(ps.parent.item.AddSubMenuItemCheckbox("", "", false))
			onClick(ps.options[j].item, func() {
				t.mu.Lock()
				change := ps.changes[j]
				t.mu.Unlock()
				if t.act.Pick != nil {
					t.act.Pick(change)
				}
			})
		}
		ps.more = newSlot(ps.parent.item.AddSubMenuItem("", ""))
	}
	t.moreSubs = newSlot(scope.AddSubMenuItem("", ""))
	t.reset = newSlot(scope.AddSubMenuItem(resetTitle, "Forget the scopes chosen here"))
	onClick(t.reset.item, func() {
		if t.act.ResetScopes != nil {
			t.act.ResetScopes()
		}
	})
}

// Show puts the model on screen. It is safe to call from any goroutine.
func (t *Tray) Show(m Model) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if m.Title != t.title {
		systray.SetTitle(m.Title)
		t.title = m.Title
	}

	showLines(t.top[:], m.Top)
	for i := range t.scopes {
		row := &t.scopes[i]
		if i >= len(m.Scopes) {
			row.parent.set("", false, false)
			row.url = ""
			continue
		}
		r := m.Scopes[i]
		row.parent.set(r.Title, true, false)
		row.url = r.PortalURL
		showLines(row.services[:], r.Services)
		row.portal.set("Open in Azure Portal", true, false)
	}
	t.note.set(m.ScopesNote.Text, m.ScopesNote.Text != "", m.ScopesNote.Disabled)
	showLines(t.bottom[:], m.Bottom)
	t.status.set(m.Status.Text, m.Status.Text != "", m.Status.Disabled)

	for i := range t.picker {
		ps := &t.picker[i]
		if i >= len(m.Picker.Subscriptions) {
			ps.parent.set("", false, false)
			continue
		}
		sub := m.Picker.Subscriptions[i]
		ps.parent.set(sub.Title, true, false)
		for j, opt := range ps.options {
			if j >= len(sub.Options) {
				opt.set("", false, false)
				continue
			}
			o := sub.Options[j]
			opt.set(o.Title, true, o.Disabled)
			opt.setChecked(o.Checked)
			ps.changes[j] = o.Change
		}
		ps.more.set(sub.More, sub.More != "", true)
	}
	t.pickNone.set("No subscriptions known yet", len(m.Picker.Subscriptions) == 0, true)
	t.moreSubs.set(m.Picker.More, m.Picker.More != "", true)
	t.reset.set(resetTitle, m.Picker.CanReset, false)

	t.refreshParent.set(m.Settings.RefreshTitle, true, false)
	showOptions(t.refreshOptions, m.Settings.Refresh)
	t.servicesParent.set(m.Settings.ServicesTitle, true, false)
	showOptions(t.servicesOptions, m.Settings.Services)
}

// resetTitle is the Scope submenu's last item, offered once a scope has been chosen.
const resetTitle = "Track all subscriptions"

// buildSettings creates the Settings submenu: two pick-lists, each choice a checkable item.
func (t *Tray) buildSettings() {
	menu := systray.AddMenuItem("Settings", "How CostMeter behaves")

	t.refreshParent = &slot{item: menu.AddSubMenuItem("", ""), shown: true}
	for _, d := range settings.RefreshOptions {
		item := t.refreshParent.item.AddSubMenuItemCheckbox(RefreshTitle(d), "", false)
		t.refreshOptions = append(t.refreshOptions, &slot{item: item, title: RefreshTitle(d), shown: true})
		onClick(item, func() {
			if t.act.SetRefresh != nil {
				t.act.SetRefresh(d)
			}
		})
	}

	t.servicesParent = &slot{item: menu.AddSubMenuItem("", ""), shown: true}
	for _, n := range settings.ServicesOptions {
		item := t.servicesParent.item.AddSubMenuItemCheckbox(ServicesTitle(n), "", false)
		t.servicesOptions = append(t.servicesOptions, &slot{item: item, title: ServicesTitle(n), shown: true})
		onClick(item, func() {
			if t.act.SetServices != nil {
				t.act.SetServices(n)
			}
		})
	}
}

// showOptions puts a check mark on the chosen option of a pick-list.
func showOptions(slots []*slot, options []SettingOption) {
	for i, s := range slots {
		if i < len(options) {
			s.setChecked(options[i].Checked)
		}
	}
}

func showLines(slots []*slot, lines []Line) {
	for i, s := range slots {
		if i < len(lines) {
			s.set(lines[i].Text, true, lines[i].Disabled)
		} else {
			s.set("", false, false)
		}
	}
}

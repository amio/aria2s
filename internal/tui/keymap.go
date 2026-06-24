package tui

import (
	"strings"

	"charm.land/bubbles/v2/key"
)

// dashboardKeys is the authoritative shortcut map for all dashboard modes.
// Key matching and footer-help labels must both flow from these bindings.
var dashboardKeys = newDashboardKeyMaps()

type dashboardKeyMaps struct {
	List   listKeyMap
	Add    addKeyMap
	Detail detailKeyMap
}

type listKeyMap struct {
	Quit       key.Binding
	SelectDown key.Binding
	SelectUp   key.Binding
	Detail     key.Binding
	Add        key.Binding
	PasteURL   key.Binding
	Pause      key.Binding
	Resume     key.Binding
	Remove     key.Binding
	NextPage   key.Binding
	PrevPage   key.Binding
}

type addKeyMap struct {
	Quit       key.Binding
	Cancel     key.Binding
	Submit     key.Binding
	NextField  key.Binding
	PrevField  key.Binding
	Backspace  key.Binding
	RecentUp   key.Binding
	RecentDown key.Binding
}

type detailKeyMap struct {
	Quit       key.Binding
	Back       key.Binding
	Next       key.Binding
	Prev       key.Binding
	ScrollDown key.Binding
	ScrollUp   key.Binding
	NextPage   key.Binding
	PrevPage   key.Binding
	Open       key.Binding
}

func newDashboardKeyMaps() dashboardKeyMaps {
	return dashboardKeyMaps{
		List: listKeyMap{
			Quit:       newBinding("q", "Quit", "q", "ctrl+c"),
			SelectDown: newBinding("j", "Select", "j", "down"),
			SelectUp:   newBinding("k", "Select", "k", "up"),
			Detail:     newBinding("Enter/l", "Detail", "enter", "l"),
			Add:        newBinding("a", "Add", "a"),
			PasteURL:   newBinding("Ctrl+P", "Paste URL", "ctrl+p"),
			Pause:      newBinding("p", "Pause", "p"),
			Resume:     newBinding("r", "Resume", "r"),
			Remove:     newBinding("d", "Remove", "d"),
			NextPage:   newBinding("n", "Next/Prev Page", "n"),
			PrevPage:   newBinding("b", "Next/Prev Page", "b"),
		},
		Add: addKeyMap{
			Quit:       newBinding("Ctrl+C", "Quit", "ctrl+c"),
			Cancel:     newBinding("Esc", "Back", "esc"),
			Submit:     newBinding("Enter", "Submit", "enter"),
			NextField:  newBinding("Tab", "Next", "tab"),
			PrevField:  newHiddenBinding("shift+tab"),
			Backspace:  newHiddenBinding("backspace"),
			RecentUp:   newHiddenBinding("up"),
			RecentDown: newHiddenBinding("down"),
		},
		Detail: detailKeyMap{
			Quit:       newBinding("q", "Quit", "q", "ctrl+c"),
			Back:       newBinding("Esc/h", "Back", "esc", "h", "enter"),
			Next:       newBinding("j", "Next/Prev", "j"),
			Prev:       newBinding("k", "Next/Prev", "k"),
			ScrollDown: newHiddenBinding("down"),
			ScrollUp:   newHiddenBinding("up"),
			NextPage:   newBinding("n", "Page", "n"),
			PrevPage:   newBinding("b", "Page", "b"),
			Open:       newBinding("o", "Open in File Manager", "o"),
		},
	}
}

func (keys listKeyMap) HelpItems() []helpItem {
	return bindingHelpItems(
		helpGroup(keys.SelectDown, keys.SelectUp),
		helpGroup(keys.Detail),
		helpGroup(keys.Add),
		helpGroup(keys.PasteURL),
		helpGroup(keys.Pause),
		helpGroup(keys.Resume),
		helpGroup(keys.Remove),
		helpGroup(keys.NextPage, keys.PrevPage),
		helpGroup(keys.Quit),
	)
}

func (keys addKeyMap) HelpItems() []helpItem {
	return bindingHelpItems(
		helpGroup(keys.Submit),
		helpGroup(keys.NextField),
		helpGroup(keys.Cancel),
		helpGroup(keys.Quit),
	)
}

func (keys detailKeyMap) HelpItems() []helpItem {
	return bindingHelpItems(
		helpGroup(keys.Back),
		helpGroup(keys.Next, keys.Prev),
		helpGroup(keys.NextPage, keys.PrevPage),
		helpGroup(keys.Open),
		helpGroup(keys.Quit),
	)
}

type bindingHelpGroup struct {
	bindings []key.Binding
}

func helpGroup(bindings ...key.Binding) bindingHelpGroup {
	return bindingHelpGroup{bindings: bindings}
}

func bindingHelpItems(groups ...bindingHelpGroup) []helpItem {
	items := make([]helpItem, 0, len(groups))
	for _, group := range groups {
		item, ok := group.item()
		if ok {
			items = append(items, item)
		}
	}
	return items
}

func (group bindingHelpGroup) item() (helpItem, bool) {
	labels := make([]string, 0, len(group.bindings))
	desc := ""
	for _, binding := range group.bindings {
		if !binding.Enabled() {
			continue
		}
		help := binding.Help()
		if help.Key == "" || help.Desc == "" {
			continue
		}
		if desc == "" {
			desc = help.Desc
		}
		labels = append(labels, help.Key)
	}
	if len(labels) == 0 || desc == "" {
		return helpItem{}, false
	}
	return helpItem{
		key:  strings.Join(labels, "/"),
		desc: desc,
	}, true
}

func newBinding(label string, desc string, keys ...string) key.Binding {
	return key.NewBinding(
		key.WithKeys(keys...),
		key.WithHelp(label, desc),
	)
}

func newHiddenBinding(keys ...string) key.Binding {
	return key.NewBinding(key.WithKeys(keys...))
}

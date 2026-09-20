package tui

import (
	"slices"
	"sort"
	"strings"

	"github.com/amio/aria2s/internal/app"
)

// taskGroup is one Dashboard ordering bucket. A row joins the first group whose
// canonical status matches and whose optional `holds` refinement accepts it;
// `before` then orders the rows inside that group.
type taskGroup struct {
	status app.TaskStatus
	holds  func(row app.TaskRow) bool         // nil accepts every row of status
	before func(left, right app.TaskRow) bool // group-local order
}

// dashboardGroups is the single source of truth for Dashboard list order:
// slice order is group order, and every group carries the rule that sorts its
// own rows. One canonical status may claim several groups when its rows belong
// beside different neighbours — a paused partial download is unfinished work,
// while a paused payload at 100% is a finished one waiting to seed.
var dashboardGroups = []taskGroup{
	{status: app.StatusError, before: keepSnapshotOrder},
	{status: app.StatusWaiting, before: keepSnapshotOrder},
	{status: app.StatusMetadata, before: newerAddedTask},
	{status: app.StatusPaused, holds: partiallyDownloaded, before: moreCompleteTask},
	{status: app.StatusDownloading, before: lessCompleteTask},
	{status: app.StatusSeeding, before: taskNameLess},
	{status: app.StatusPaused, holds: fullyDownloaded, before: taskNameLess},
	{status: app.StatusComplete, before: taskNameLess},
}

// unexpectedTaskGroup receives rows whose canonical status no group claims.
// They trail the known groups in snapshot order instead of being folded into a
// group whose rule was not written for them.
var unexpectedTaskGroup = taskGroup{before: keepSnapshotOrder}

// dashboardSummaryStatuses lists each canonical status once, in the order its
// first group appears, so the list footer counts follow the visible grouping.
var dashboardSummaryStatuses = summaryStatuses()

// sortTaskRows orders rows by group, then by that group's own rule. The sort is
// stable, so rows a group rule considers equivalent keep snapshot order: the
// aria2 queue order and the app's newest-first stopped history.
func sortTaskRows(rows []app.TaskRow) {
	sort.SliceStable(rows, func(left, right int) bool {
		leftRank, group := taskGroupOf(rows[left])
		rightRank, _ := taskGroupOf(rows[right])
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		return group.before(rows[left], rows[right])
	})
}

func taskGroupOf(row app.TaskRow) (int, taskGroup) {
	status := app.TaskStatus(row.CanonicalStatus)
	for rank, group := range dashboardGroups {
		if group.status == status && (group.holds == nil || group.holds(row)) {
			return rank, group
		}
	}
	return len(dashboardGroups), unexpectedTaskGroup
}

func summaryStatuses() []app.TaskStatus {
	statuses := make([]app.TaskStatus, 0, len(dashboardGroups))
	for _, group := range dashboardGroups {
		if !slices.Contains(statuses, group.status) {
			statuses = append(statuses, group.status)
		}
	}
	return statuses
}

// taskProgress reports the row's completion ratio, and whether the row exposes
// enough length information for that ratio to mean anything.
func taskProgress(row app.TaskRow) (float64, bool) {
	if row.TotalLength <= 0 {
		return 0, false
	}
	return float64(row.CompletedLength) / float64(row.TotalLength), true
}

// fullyDownloaded reports a payload observed at 100%. An unmeasurable row stays
// incomplete: an absent total proves nothing about the payload.
func fullyDownloaded(row app.TaskRow) bool {
	progress, known := taskProgress(row)
	return known && progress >= 1
}

func partiallyDownloaded(row app.TaskRow) bool {
	return !fullyDownloaded(row)
}

func keepSnapshotOrder(app.TaskRow, app.TaskRow) bool {
	return false
}

func newerAddedTask(left, right app.TaskRow) bool {
	if left.AddedAt.IsZero() != right.AddedAt.IsZero() {
		return !left.AddedAt.IsZero()
	}
	return left.AddedAt.After(right.AddedAt)
}

func taskNameLess(left, right app.TaskRow) bool {
	return strings.ToLower(left.Name) < strings.ToLower(right.Name)
}

// moreCompleteTask and lessCompleteTask order measurable rows by progress and
// keep unmeasurable ones last, where neither direction is meaningful.
func moreCompleteTask(left, right app.TaskRow) bool {
	leftProgress, leftKnown := taskProgress(left)
	rightProgress, rightKnown := taskProgress(right)
	if leftKnown != rightKnown {
		return leftKnown
	}
	return leftKnown && leftProgress > rightProgress
}

func lessCompleteTask(left, right app.TaskRow) bool {
	leftProgress, leftKnown := taskProgress(left)
	rightProgress, rightKnown := taskProgress(right)
	if leftKnown != rightKnown {
		return leftKnown
	}
	return leftKnown && leftProgress < rightProgress
}

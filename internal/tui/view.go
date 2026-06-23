package tui

import (
	"fmt"
	"math"
	"path/filepath"
	"strings"

	"github.com/amio/aria2s/internal/aria2"
	"github.com/charmbracelet/x/ansi"
)

const (
	defaultViewportWidth  = 120
	defaultViewportHeight = 28
	minTableWidth         = 58 // below this even the 4-column minimal layout won't fit
	minBodyHeight         = 3
	minNameWidth          = 18
	columnGap             = "  "
	framePaddingX         = 2
	bodyTopPaddingLines   = 1
	bodyBottomPaddingLine = 1

	// column base widths
	statusBaseWidth     = 10
	sizeBaseWidth       = 12
	downloadedBaseWidth = 12
	progressBaseWidth   = 10
	downBaseWidth       = 14
	upBaseWidth         = 14
)

type rgb struct {
	r int
	g int
	b int
}

var (
	frameEdgeColor    = rgb{87, 110, 129}
	frameDividerColor = rgb{29, 42, 54}
	frameTextColor    = rgb{241, 244, 247}
	bodyColor         = rgb{9, 15, 22}
	bodyTextColor     = rgb{210, 217, 225}
	selectedColor     = rgb{28, 44, 58}
	errorTextColor    = rgb{255, 152, 152}
)

func (model Model) View() string {
	switch model.mode {
	case ModeAdd:
		return model.addView()
	case ModeDetail:
		return model.detailView()
	default:
		return model.listView()
	}
}

func (model Model) listView() string {
	width, height := model.viewport()
	header := model.tableFrame(model.tableHeader(frameContentWidth(width)), true)
	footer := model.tableFrame(joinSides(model.listStats(), model.listHelp(), frameContentWidth(width)), false)
	bodyHeight := max(height-len(header)-len(footer), minBodyHeight)
	body := model.listBody(width, bodyHeight)
	return strings.Join(append(append(header, body...), footer...), "\n")
}

func (model Model) addView() string {
	width, height := model.viewport()
	header := model.titleFrame("Add Download")
	footer := model.tableFrame(joinSides("Submit a new task to local aria2 JSON-RPC.", model.addHelp(), frameContentWidth(width)), false)
	bodyHeight := max(height-len(header)-len(footer), minBodyHeight)

	body := model.fillBody(width, bodyHeight, model.addForm.BodyLines())
	return strings.Join(append(append(header, body...), footer...), "\n")
}

func (model Model) detailView() string {
	width, height := model.viewport()
	detail := model.detail

	// Header: name on left, [status] + progress on right.
	rightParts := []string{
		fmt.Sprintf("[%s]", detailStatusLabel(detail)),
		fmt.Sprintf("%s of %s (%s)", formatBytes(detail.CompletedLength), formatBytes(detail.TotalLength), formatProgress(detail.CompletedLength, detail.TotalLength)),
	}
	fcw := frameContentWidth(width)
	const minGap = 5
	rightMin := 0
	for i, p := range rightParts {
		if i > 0 {
			rightMin++
		}
		rightMin += ansi.StringWidth(p)
	}
	maxNameWidth := fcw - rightMin - minGap
	if maxNameWidth < 10 {
		maxNameWidth = 10
	}
	name := detail.Name
	if ansi.StringWidth(name) > maxNameWidth {
		name = ansi.Truncate(name, maxNameWidth, "...")
	}
	headerContent := joinSides(name, rightParts, fcw)
	header := model.tableFrame(headerContent, true)

	footer := model.tableFrame(joinSides(model.detailStats(), model.detailHelp(), fcw), false)
	bodyHeight := max(height-len(header)-len(footer), minBodyHeight)

	lines := []string{
		formatDetailLabel("GID", detail.GID),
	}
	if detail.InfoHash != "" {
		lines = append(lines, formatDetailLabel("Info Hash", detail.InfoHash))
	}
	lines = append(lines,
		formatDetailLabel("Download Dir", detailDownloadDir(detail)),
		"",
		formatDetailLabel("Down", formatSpeed(detail.DownloadSpeed)),
		formatDetailLabel("Up", formatSpeed(detail.UploadSpeed)),
		formatDetailLabel("Uploaded", formatBytes(detail.UploadLength)),
		formatDetailLabel("Connections", fmt.Sprintf("%d", detail.Connections)),
	)
	if detail.NumSeeders > 0 {
		lines = append(lines, formatDetailLabel("Seeders", fmt.Sprintf("%d", detail.NumSeeders)))
	}
	if detail.Seeder {
		lines = append(lines, formatDetailLabel("Seeding", "yes"))
	}
	lines = append(lines, "")
	if detail.PieceLength > 0 {
		lines = append(lines, formatDetailLabel("Piece Length", formatBytes(detail.PieceLength)))
	}
	if detail.NumPieces > 0 {
		lines = append(lines, formatDetailLabel("Pieces", fmt.Sprintf("%d", detail.NumPieces)))
	}
	if detail.VerifiedLength > 0 {
		lines = append(lines, formatDetailLabel("Verified", formatBytes(detail.VerifiedLength)))
	}
	if detail.VerifyIntegrityPending {
		lines = append(lines, formatDetailLabel("Hash Check", "pending"))
	}
	if detail.ErrorMessage != "" {
		lines = append(lines, formatDetailLabel("Error "+detail.ErrorCode, detail.ErrorMessage))
	}
	if len(detail.Files) > 0 {
		lines = append(lines, "", "Files:")
		for _, file := range detail.Files {
			pct := float64(file.CompletedLength) / float64(file.Length)
			if file.Length <= 0 {
				pct = 0
			}
			bar := makeProgressBar(pct)
			label := fmt.Sprintf("%s %s %s", bar, file.Name,
				dimText(fmt.Sprintf("(%s of %s)", formatBytes(file.CompletedLength), formatBytes(file.Length))))
			if !file.Selected {
				label += dimText(" (unselected)")
			}
			lines = append(lines, label)
		}
	}

	// Apply scroll offset.
	visible := bodyHeight
	if visible < 1 {
		visible = 1
	}
	maxScroll := len(lines) - visible
	if model.detailScroll > maxScroll {
		model.detailScroll = maxScroll
	}
	if model.detailScroll < 0 {
		model.detailScroll = 0
	}
	if model.detailScroll > 0 && model.detailScroll <= len(lines) {
		lines = lines[model.detailScroll:]
	}

	body := model.fillDetailBody(width, bodyHeight, lines)
	return strings.Join(append(append(header, body...), footer...), "\n")
}

func (model Model) tableHeader(contentWidth int) string {
	if contentWidth < minTableWidth {
		return fitLeft("Status  Name  Progress  Down Speed", contentWidth)
	}
	l := computeLayout(contentWidth)
	parts := make([]string, 0, 7)
	add := func(text string, width int, right bool) {
		if width > 0 {
			if right {
				parts = append(parts, fitRight(text, width))
			} else {
				parts = append(parts, fitLeft(text, width))
			}
		}
	}
	add("Status", l.statusWidth, false)
	add("Name", l.nameWidth, false)
	add("Size", l.sizeWidth, true)
	add("Downloaded", l.downloadedWidth, true)
	add("Progress", l.progressWidth, true)
	add("Down Speed", l.downWidth, true)
	add("Up Speed", l.upWidth, true)
	return strings.Join(parts, columnGap)
}

func (model Model) listBody(width int, height int) []string {
	contentWidth := frameContentWidth(width)
	if contentWidth < minTableWidth {
		return model.fillBody(width, height, []string{"Terminal is too narrow for the full table view.", "Increase the terminal width and resize again."})
	}

	body := make([]string, 0, height)
	if model.err != nil {
		body = append(body, paddedStyledLine(fmt.Sprintf("Error: %v", model.err), width, framePaddingX, errorTextColor, bodyColor, true))
	}

	items := model.items()
	remaining := height - len(body)
	if remaining <= 0 {
		return body[:height]
	}
	if len(items) == 0 {
		body = append(body, model.blankBodyLine(width, "No downloads yet. Press a to add one."))
		return append(body, model.blankBodyLines(width, height-len(body))...)
	}

	start := tableStart(model.selected, len(items), remaining)
	end := min(start+remaining, len(items))
	for index := start; index < end; index++ {
		body = append(body, model.downloadRow(width, items[index], index == model.selected))
	}
	if len(body) < height {
		body = append(body, model.blankBodyLines(width, min(bodyBottomPaddingLine, height-len(body)))...)
	}
	if len(body) < height {
		body = append(body, model.blankBodyLines(width, height-len(body))...)
	}
	return body
}

func (model Model) fillBody(width int, height int, lines []string) []string {
	body := make([]string, 0, height)
	body = append(body, model.blankBodyLines(width, min(bodyTopPaddingLines, height))...)
	for _, line := range lines {
		if len(body) == height {
			return body
		}
		body = append(body, model.blankBodyLine(width, line))
	}
	if len(body) < height {
		body = append(body, model.blankBodyLines(width, min(bodyBottomPaddingLine, height-len(body)))...)
	}
	if len(body) < height {
		body = append(body, model.blankBodyLines(width, height-len(body))...)
	}
	return body
}

func (model Model) fillDetailBody(width int, height int, lines []string) []string {
	body := make([]string, 0, height)
	for _, line := range lines {
		if len(body) == height {
			return body
		}
		body = append(body, model.blankBodyLine(width, line))
	}
	if len(body) < height {
		body = append(body, model.blankBodyLines(width, height-len(body))...)
	}
	return body
}

func (model Model) blankBodyLines(width int, count int) []string {
	lines := make([]string, 0, count)
	for range count {
		lines = append(lines, model.blankBodyLine(width, ""))
	}
	return lines
}

func (model Model) blankBodyLine(width int, text string) string {
	return paddedStyledLine(text, width, framePaddingX, bodyTextColor, bodyColor, false)
}

func (model Model) downloadRow(width int, download aria2.Download, selected bool) string {
	contentWidth := frameContentWidth(width)
	l := computeLayout(contentWidth)
	parts := make([]string, 0, 7)
	add := func(text string, width int, right bool) {
		if width > 0 {
			if right {
				parts = append(parts, fitRight(text, width))
			} else {
				parts = append(parts, fitLeft(text, width))
			}
		}
	}
	add(downloadStatusLabel(download), l.statusWidth, false)
	add(download.Name, l.nameWidth, false)
	add(formatBytes(download.TotalLength), l.sizeWidth, true)
	add(formatBytes(download.CompletedLength), l.downloadedWidth, true)
	add(formatProgress(download.CompletedLength, download.TotalLength), l.progressWidth, true)
	add(formatSpeed(download.DownloadSpeed), l.downWidth, true)
	add(formatSpeed(download.UploadSpeed), l.upWidth, true)

	row := strings.Join(parts, columnGap)
		background := bodyColor
		if selected {
			background = selectedColor
		}
		return selectedLine(row, width, background, downloadStatusTone(download), selected)
}

func (model Model) titleFrame(title string) []string {
	width, _ := model.viewport()
	return []string{
		strings.Repeat(" ", width),
		paddedTransparentLine(title, width, framePaddingX, frameTextColor, true),
		transparentHalfBlockLine(width, bodyColor, '▄'),
	}
}

func (model Model) tableFrame(content string, top bool) []string {
	width, _ := model.viewport()
	if top {
		return []string{
			strings.Repeat(" ", width),
			paddedTransparentLine(content, width, framePaddingX, frameTextColor, true),
			transparentHalfBlockLine(width, bodyColor, '▄'),
		}
	}
	return []string{
		transparentHalfBlockLine(width, frameDividerColor, '▀'),
		paddedTransparentLine(content, width, framePaddingX, frameTextColor, false),
		strings.Repeat(" ", width),
	}
}

func (model Model) listStats() string {
	items := model.items()
	var downTotal int64
	var upTotal int64
	for _, item := range items {
		downTotal += item.DownloadSpeed
		upTotal += item.UploadSpeed
	}
	return fmt.Sprintf(
		"Tasks %d (A%d W%d S%d) ↓%s ↑%s",
		len(items),
		len(model.snapshot.Active),
		len(model.snapshot.Waiting),
		len(model.snapshot.Stopped),
		formatSpeed(downTotal),
		formatSpeed(upTotal),
	)
}

func (model Model) detailStats() string {
	return fmt.Sprintf("Detail view for %s", model.detail.GID)
}

func (model Model) listHelp() []string {
	return helpSegments(
		helpItem{key: "j/k", desc: "Select"},
		helpItem{key: "Enter/l", desc: "Detail"},
		helpItem{key: "a", desc: "Add"},
		helpItem{key: "p", desc: "Pause"},
		helpItem{key: "r", desc: "Resume"},
		helpItem{key: "d", desc: "Remove"},
		helpItem{key: "n/b", desc: "Next/Prev Page"},
		helpItem{key: "q", desc: "Quit"},
	)
}

func (model Model) addHelp() []string {
	return helpSegments(
		helpItem{key: "Enter", desc: "Submit"},
		helpItem{key: "Tab", desc: "Next"},
		helpItem{key: "Esc", desc: "Back"},
		helpItem{key: "Ctrl+C", desc: "Quit"},
	)
}

func (model Model) detailHelp() []string {
	return helpSegments(
		helpItem{key: "Esc/h", desc: "Back"},
		helpItem{key: "j/k", desc: "Next/Prev"},
		helpItem{key: "n/b", desc: "Page"},
		helpItem{key: "q", desc: "Quit"},
	)
}

func (model Model) viewport() (int, int) {
	width := model.width
	height := model.height
	if width <= 0 {
		width = defaultViewportWidth
	}
	if height <= 0 {
		height = defaultViewportHeight
	}
	return width, height
}

func tableColumnWidths(width int) (int, int, int, int, int, int, int) {
	l := computeLayout(width)
	return l.statusWidth, l.nameWidth, l.sizeWidth, l.downloadedWidth, l.progressWidth, l.downWidth, l.upWidth
}

// tableLayout holds the computed column widths for a given content width.
// A width of 0 means the column is hidden.
type tableLayout struct {
	statusWidth     int
	nameWidth       int
	sizeWidth       int
	downloadedWidth int
	progressWidth   int
	downWidth       int
	upWidth         int
}

// computeLayout determines which columns are visible and their widths.
// Columns are hidden in this order as width shrinks: Downloaded, Size, Up Speed.
func computeLayout(width int) tableLayout {
	l := tableLayout{
		statusWidth:     statusBaseWidth,
		sizeWidth:       sizeBaseWidth,
		downloadedWidth: downloadedBaseWidth,
		progressWidth:   progressBaseWidth,
		downWidth:       downBaseWidth,
		upWidth:         upBaseWidth,
	}
	for l.fixed()+minNameWidth > width && l.hideNext() {
	}
	l.nameWidth = max(width-l.fixed(), minNameWidth)
	return l
}

// fixed returns the total width of all non-name columns plus column gaps.
func (l tableLayout) fixed() int {
	w := l.statusWidth + l.sizeWidth + l.downloadedWidth + l.progressWidth + l.downWidth + l.upWidth
	n := l.visible()
	if n > 1 {
		w += (n - 1) * len(columnGap)
	}
	return w
}

// visible returns the number of visible columns (including name).
func (l tableLayout) visible() int {
	n := 2 // status and name are always visible
	if l.sizeWidth > 0 {
		n++
	}
	if l.downloadedWidth > 0 {
		n++
	}
	if l.progressWidth > 0 {
		n++
	}
	if l.downWidth > 0 {
		n++
	}
	if l.upWidth > 0 {
		n++
	}
	return n
}

// hideNext removes the next optional column; returns false when none remain.
func (l *tableLayout) hideNext() bool {
	if l.downloadedWidth > 0 {
		l.downloadedWidth = 0
		return true
	}
	if l.sizeWidth > 0 {
		l.sizeWidth = 0
		return true
	}
	if l.upWidth > 0 {
		l.upWidth = 0
		return true
	}
	return false
}

func tableStart(selected int, total int, visible int) int {
	if visible <= 0 || total <= visible || selected < visible {
		return 0
	}
	start := selected - visible + 1
	maxStart := max(total-visible, 0)
	if start > maxStart {
		return maxStart
	}
	return start
}

func statusLabel(status string) string {
	switch status {
	case "active":
		return "Active"
	case "waiting":
		return "Waiting"
	case "paused":
		return "Paused"
	case "complete":
		return "Done"
	case "error":
		return "Error"
	case "removed":
		return "Removed"
	default:
		if status == "" {
			return "Unknown"
		}
		return strings.ToUpper(status[:1]) + status[1:]
	}
}

func statusTone(status string) rgb {
	switch status {
	case "active":
		return rgb{191, 220, 201}
	case "waiting":
		return rgb{224, 215, 181}
	case "paused":
		return rgb{222, 202, 176}
	case "complete":
		return rgb{188, 211, 228}
	case "error", "removed":
		return rgb{236, 191, 191}
	default:
		return bodyTextColor
	}
}

func downloadStatusLabel(download aria2.Download) string {
	if download.IsMetadata {
		return "Metadata"
	}
	return statusLabel(download.Status)
}

func downloadStatusTone(download aria2.Download) rgb {
	if download.IsMetadata {
		return rgb{180, 190, 210}
	}
	return statusTone(download.Status)
}

func detailStatusLabel(detail aria2.DownloadDetail) string {
	if detail.IsMetadata {
		return "Metadata"
	}
	return statusLabel(detail.Status)
}

func formatBytes(value int64) string {
	if value <= 0 {
		return "0 B"
	}
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	size := float64(value)
	unit := 0
	for size >= 1024 && unit < len(units)-1 {
		size /= 1024
		unit++
	}
	if unit == 0 {
		return fmt.Sprintf("%d %s", value, units[unit])
	}
	return fmt.Sprintf("%.1f %s", size, units[unit])
}

func formatSpeed(value int64) string {
	return fmt.Sprintf("%s/s", formatBytes(value))
}

func formatProgress(completed int64, total int64) string {
	if total <= 0 {
		return "0.0%"
	}
	return fmt.Sprintf("%.1f%%", float64(completed)/float64(total)*100)
}

/** makeProgressBar returns a unicode progress bar string with an always-visible
    half-character slider (╸) separating filled from empty. Each cell counts
    as 2 segments; one segment is reserved for the slider so it never vanishes
    into a full cell. The thin-track portion (─) is rendered dim. */
func makeProgressBar(progress float64, charCount ...int) string {
	n := 5
	if len(charCount) > 0 && charCount[0] > 0 {
		n = charCount[0]
	}
	if progress <= 0 {
		return dimText(strings.Repeat("\u2500", n)) // ─
	}
	if progress >= 1 {
		return strings.Repeat("\u2501", n) // ━
	}

	sliderPos := int(math.Floor(progress * float64(n*2-1)))

	var b strings.Builder
	b.Grow(n * 3)
	for i := 0; i < n; i++ {
		cellStart := i * 2
		if cellStart+1 < sliderPos {
			b.WriteRune('\u2501') // ━
		} else if cellStart <= sliderPos {
			b.WriteRune('\u2578') // ╸
		} else {
			b.WriteRune('\u2500') // ─
		}
	}

	raw := b.String()
	if idx := strings.IndexRune(raw, '\u2500'); idx >= 0 {
		return raw[:idx] + dimText(raw[idx:])
	}
	return raw
}

func joinSides(left string, rightParts []string, width int) string {
	left = strings.TrimSpace(left)
	leftWidth := ansi.StringWidth(left)
	const minGap = 5
	room := max(width-leftWidth-minGap, 0)
	var included []string
	for _, part := range rightParts {
		part = strings.TrimSpace(part)
		needed := ansi.StringWidth(part)
		if len(included) > 0 {
			needed++
		}
		if needed > room {
			break
		}
		included = append(included, part)
		room -= needed
	}
	right := strings.Join(included, " ")
	rightWidth := ansi.StringWidth(right)
	if rightWidth > 0 && leftWidth+minGap+rightWidth <= width {
		return left + strings.Repeat(" ", width-leftWidth-rightWidth) + right
	}
	return fitLeft(left, width)
}

func frameContentWidth(width int) int {
	return max(width-framePaddingX*2, 1)
}

func fitLeft(text string, width int) string {
	return fit(text, width, false)
}

func fitRight(text string, width int) string {
	return fit(text, width, true)
}

func fit(text string, width int, right bool) string {
	if width <= 0 {
		return ""
	}
	cleaned := strings.ReplaceAll(text, "\n", " ")
	if ansi.StringWidth(cleaned) > width {
		if width <= 3 {
			cleaned = strings.Repeat(".", width)
		} else {
			cleaned = ansi.Truncate(cleaned, width, "...")
		}
	}
	padding := width - ansi.StringWidth(cleaned)
	if padding < 0 {
		padding = 0
	}
	if right {
		return strings.Repeat(" ", padding) + cleaned
	}
	return cleaned + strings.Repeat(" ", padding)
}

func styledLine(text string, foreground rgb, background rgb, bold bool) string {
	return colorize(text, foreground, background, bold)
}

func paddedStyledLine(text string, width int, padding int, foreground rgb, background rgb, bold bool) string {
	innerWidth := max(width-padding*2, 0)
	if innerWidth == 0 {
		return styledLine(strings.Repeat(" ", width), foreground, background, bold)
	}
	line := strings.Repeat(" ", padding) + fitLeft(text, innerWidth) + strings.Repeat(" ", padding)
	return styledLine(line, foreground, background, bold)
}

func paddedTransparentLine(text string, width int, padding int, foreground rgb, bold bool) string {
	innerWidth := max(width-padding*2, 0)
	if innerWidth == 0 {
		return colorizeForeground(strings.Repeat(" ", width), foreground, bold)
	}
	line := strings.Repeat(" ", padding) + fitLeft(text, innerWidth) + strings.Repeat(" ", padding)
	return colorizeForeground(line, foreground, bold)
}

func selectedLine(text string, width int, background rgb, status rgb, selected bool) string {
	if ansi.StringWidth(text) == 0 {
		return paddedStyledLine("", width, framePaddingX, bodyTextColor, background, false)
	}
	return paddedStyledLine(text, width, framePaddingX, status, background, false)
}

func halfBlockLine(width int, top rgb, bottom rgb, block rune) string {
	return colorize(strings.Repeat(string(block), width), foregroundForBlock(top, bottom, block), backgroundForBlock(top, bottom, block), false)
}

func transparentHalfBlockLine(width int, color rgb, block rune) string {
	return colorizeForeground(strings.Repeat(string(block), width), color, false)
}

func foregroundForBlock(top rgb, bottom rgb, block rune) rgb {
	if block == '▀' {
		return top
	}
	return bottom
}

func backgroundForBlock(top rgb, bottom rgb, block rune) rgb {
	if block == '▀' {
		return bottom
	}
	return top
}

func colorize(text string, foreground rgb, background rgb, bold bool) string {
	var builder strings.Builder
	if bold {
		builder.WriteString("\x1b[1m")
	}
	builder.WriteString("\x1b[38;2;")
	builder.WriteString(rgbCode(foreground))
	builder.WriteString("m")
	builder.WriteString("\x1b[48;2;")
	builder.WriteString(rgbCode(background))
	builder.WriteString("m")
	builder.WriteString(text)
	builder.WriteString("\x1b[0m")
	return builder.String()
}

func colorizeForeground(text string, foreground rgb, bold bool) string {
	var builder strings.Builder
	if bold {
		builder.WriteString("\x1b[1m")
	}
	builder.WriteString("\x1b[38;2;")
	builder.WriteString(rgbCode(foreground))
	builder.WriteString("m")
	builder.WriteString(text)
	builder.WriteString("\x1b[0m")
	return builder.String()
}

func rgbCode(color rgb) string {
	return fmt.Sprintf("%d;%d;%d", color.r, color.g, color.b)
}

type helpItem struct {
	key  string
	desc string
}

func helpSegments(items ...helpItem) []string {
	parts := make([]string, 0, len(items))
	for _, item := range items {
		parts = append(parts, item.key+" "+dimText(item.desc))
	}
	return parts
}

func dimText(text string) string {
	return "\x1b[2m" + text + "\x1b[22m"
}

func boldText(text string) string {
	return "\x1b[1m" + text + "\x1b[22m"
}

const detailLabelWidth = 16

func formatDetailLabel(label string, value string) string {
	return dimText(fmt.Sprintf("%-*s", detailLabelWidth, label+":")) + " " + value
}

func detailDownloadDir(detail aria2.DownloadDetail) string {
	if detail.DownloadDir != "" {
		return detail.DownloadDir
	}
	for _, file := range detail.Files {
		if file.Path != "" {
			return filepath.Dir(file.Path)
		}
	}
	return "-"
}

func min(a int, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a int, b int) int {
	if a > b {
		return a
	}
	return b
}

package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/amio/aria2s/internal/aria2"
	"github.com/charmbracelet/x/ansi"
)

const (
	defaultViewportWidth  = 120
	defaultViewportHeight = 28
	minTableWidth         = 88
	minBodyHeight         = 3
	columnGap             = "  "
	framePaddingX         = 2
	bodyTopPaddingLines   = 1
	bodyBottomPaddingLine = 1
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
	selectedColor     = rgb{21, 35, 48}
	selectedTextColor = rgb{244, 246, 248}
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

	lines := []string{
		"URL or magnet link, Tab to set dir, Enter to submit.",
		"",
		model.urlFieldLine(),
		model.dirFieldLine(),
	}
	if model.addField == fieldDir && len(model.recentDirs) > 0 {
		lines = append(lines, "", "Recent dirs (Tab to cycle):")
		for i, dir := range model.recentDirs {
			marker := "  "
			if i == model.dirPick {
				marker = "> "
			}
			lines = append(lines, fmt.Sprintf("%s%d %s", marker, i+1, dir))
		}
	}
	body := model.fillBody(width, bodyHeight, lines)
	return strings.Join(append(append(header, body...), footer...), "\n")
}

func (model Model) urlFieldLine() string {
	label := "URL:"
	if model.addField == fieldURL {
		label = boldText(label)
	}
	value := model.input
	if model.addField == fieldURL {
		value += "_"
	}
	return fmt.Sprintf("%s %s", label, value)
}

func (model Model) dirFieldLine() string {
	label := "Dir:"
	if model.addField == fieldDir {
		label = boldText(label)
	}
	if model.dirInput == "" {
		hint := model.defaultDir
		if hint == "" {
			hint = "aria2 default"
		}
		text := dimText(hint + " (default)")
		if model.addField == fieldDir {
			text = "_" + text
		}
		return fmt.Sprintf("%s %s", label, text)
	}
	value := model.dirInput
	if model.addField == fieldDir {
		value += "_"
	}
	return fmt.Sprintf("%s %s", label, value)
}

func (model Model) detailView() string {
	width, height := model.viewport()
	header := model.titleFrame("Download Details")
	footer := model.tableFrame(joinSides(model.detailStats(), model.detailHelp(), frameContentWidth(width)), false)
	bodyHeight := max(height-len(header)-len(footer), minBodyHeight)

	detail := model.detail
	lines := []string{
		fmt.Sprintf("Name: %s", detail.Name),
		fmt.Sprintf("Status: %s", detailStatusLabel(detail)),
		fmt.Sprintf("GID: %s", detail.GID),
		fmt.Sprintf("URI: %s", detail.PrimaryURI),
		formatDetailLabel("Download Dir", detailDownloadDir(detail)),
		fmt.Sprintf("Progress: %s of %s (%s)", formatBytes(detail.CompletedLength), formatBytes(detail.TotalLength), formatProgress(detail.CompletedLength, detail.TotalLength)),
		fmt.Sprintf("Down: %s", formatSpeed(detail.DownloadSpeed)),
		fmt.Sprintf("Up: %s", formatSpeed(detail.UploadSpeed)),
		fmt.Sprintf("Connections: %d", detail.Connections),
	}
	if detail.ErrorMessage != "" {
		lines = append(lines, fmt.Sprintf("Error %s: %s", detail.ErrorCode, detail.ErrorMessage))
	}
	if len(detail.Files) > 0 {
		lines = append(lines, "", "Files:")
		for _, file := range detail.Files {
			lines = append(lines, fmt.Sprintf("- %s (%s of %s)", file.Name, formatBytes(file.CompletedLength), formatBytes(file.Length)))
		}
	}

	body := model.fillBody(width, bodyHeight, lines)
	return strings.Join(append(append(header, body...), footer...), "\n")
}

func (model Model) tableHeader(contentWidth int) string {
	if contentWidth < minTableWidth {
		return fitLeft("Status  Name  Size  Downloaded  Progress  Down  Up", contentWidth)
	}
	statusWidth, nameWidth, sizeWidth, downloadedWidth, progressWidth, downWidth, upWidth := tableColumnWidths(contentWidth)
	columns := []string{
		fitLeft("Status", statusWidth),
		fitLeft("Name", nameWidth),
		fitLeft("Size", sizeWidth),
		fitLeft("Downloaded", downloadedWidth),
		fitLeft("Progress", progressWidth),
		fitLeft("Down", downWidth),
		fitLeft("Up", upWidth),
	}
	return strings.Join(columns, columnGap)
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
	statusWidth, nameWidth, sizeWidth, downloadedWidth, progressWidth, downWidth, upWidth := tableColumnWidths(contentWidth)
	status := fitLeft(downloadStatusLabel(download), statusWidth)
	name := fitLeft(download.Name, nameWidth)
	size := fitRight(formatBytes(download.TotalLength), sizeWidth)
	downloaded := fitRight(formatBytes(download.CompletedLength), downloadedWidth)
	progress := fitRight(formatProgress(download.CompletedLength, download.TotalLength), progressWidth)
	down := fitRight(formatSpeed(download.DownloadSpeed), downWidth)
	up := fitRight(formatSpeed(download.UploadSpeed), upWidth)

	row := strings.Join([]string{status, name, size, downloaded, progress, down, up}, columnGap)
	background := bodyColor
	foreground := bodyTextColor
	if selected {
		background = selectedColor
		foreground = selectedTextColor
	}
	return selectedLine(row, width, background, foreground, downloadStatusTone(download), selected)
}

func (model Model) titleFrame(title string) []string {
	width, _ := model.viewport()
	return []string{
		transparentHalfBlockLine(width, frameEdgeColor, '▀'),
		paddedTransparentLine(title, width, framePaddingX, frameTextColor, true),
		transparentHalfBlockLine(width, bodyColor, '▄'),
	}
}

func (model Model) tableFrame(content string, top bool) []string {
	width, _ := model.viewport()
	if top {
		return []string{
			transparentHalfBlockLine(width, frameEdgeColor, '▀'),
			paddedTransparentLine(content, width, framePaddingX, frameTextColor, true),
			transparentHalfBlockLine(width, bodyColor, '▄'),
		}
	}
	return []string{
		transparentHalfBlockLine(width, frameDividerColor, '▀'),
		paddedTransparentLine(content, width, framePaddingX, frameTextColor, false),
		transparentHalfBlockLine(width, frameEdgeColor, '▄'),
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
		"Items %d A%d W%d S%d P%d Down %s Up %s",
		len(items),
		len(model.snapshot.Active),
		len(model.snapshot.Waiting),
		len(model.snapshot.Stopped),
		model.stoppedPage+1,
		formatSpeed(downTotal),
		formatSpeed(upTotal),
	)
}

func (model Model) detailStats() string {
	return fmt.Sprintf("Detail view for %s", model.detail.GID)
}

func (model Model) listHelp() string {
	return helpText(
		helpItem{key: "↑/↓", desc: "Move"},
		helpItem{key: "Enter/l", desc: "Detail"},
		helpItem{key: "a", desc: "Add"},
		helpItem{key: "p", desc: "Pause"},
		helpItem{key: "r", desc: "Resume"},
		helpItem{key: "d", desc: "Remove"},
		helpItem{key: "n", desc: "Next Page"},
		helpItem{key: "b", desc: "Prev Page"},
		helpItem{key: "q", desc: "Quit"},
	)
}

func (model Model) addHelp() string {
	return helpText(
		helpItem{key: "Enter", desc: "Submit"},
		helpItem{key: "Tab", desc: "Next"},
		helpItem{key: "Esc", desc: "Back"},
		helpItem{key: "q", desc: "Quit"},
	)
}

func (model Model) detailHelp() string {
	return helpText(
		helpItem{key: "Esc/h", desc: "Back"},
		helpItem{key: "j/k", desc: "Next/Prev"},
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
	statusWidth := 10
	sizeWidth := 12
	downloadedWidth := 12
	progressWidth := 10
	downWidth := 11
	upWidth := 11
	fixed := statusWidth + sizeWidth + downloadedWidth + progressWidth + downWidth + upWidth + len(columnGap)*6
	nameWidth := max(width-fixed, 18)
	return statusWidth, nameWidth, sizeWidth, downloadedWidth, progressWidth, downWidth, upWidth
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

func joinSides(left string, right string, width int) string {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	leftWidth := ansi.StringWidth(left)
	rightWidth := ansi.StringWidth(right)
	if leftWidth+1+rightWidth <= width {
		return left + strings.Repeat(" ", width-leftWidth-rightWidth) + right
	}
	if rightWidth >= width {
		return fitRight(right, width)
	}

	leftRoom := max(width-rightWidth-1, 0)
	return fitLeft(left, leftRoom) + " " + right
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

func selectedLine(text string, width int, background rgb, foreground rgb, status rgb, selected bool) string {
	if ansi.StringWidth(text) == 0 {
		return paddedStyledLine("", width, framePaddingX, bodyTextColor, background, false)
	}
	if !selected {
		return paddedStyledLine(text, width, framePaddingX, status, background, false)
	}
	return paddedStyledLine(text, width, framePaddingX, foreground, background, false)
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

func helpText(items ...helpItem) string {
	parts := make([]string, 0, len(items))
	for _, item := range items {
		parts = append(parts, item.key+" "+dimText(item.desc))
	}
	return strings.Join(parts, " ")
}

func dimText(text string) string {
	return "\x1b[2m" + text + "\x1b[22m"
}

func boldText(text string) string {
	return "\x1b[1m" + text + "\x1b[22m"
}

func formatDetailLabel(label string, value string) string {
	return boldText(label+":") + " " + value
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

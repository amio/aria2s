package tui

import (
	"fmt"
	"strings"

	"github.com/amio/aria2s/internal/aria2"
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
	var builder strings.Builder
	builder.WriteString("aria2s console\n\n")
	model.writeSection(&builder, "Active", model.snapshot.Active)
	model.writeSection(&builder, "Waiting", model.snapshot.Waiting)
	model.writeSection(&builder, fmt.Sprintf("Stopped page %d", model.stoppedPage+1), model.snapshot.Stopped)
	if model.err != nil {
		fmt.Fprintf(&builder, "\nError: %v\n", model.err)
	}
	builder.WriteString("\nKeys: a add, p pause, r resume, d remove, enter detail, n next stopped page, b previous stopped page, q quit\n")
	return builder.String()
}

func (model Model) addView() string {
	return fmt.Sprintf("Add URL or magnet\n\n%s\n\nEnter submits, Esc cancels, q quits\n", model.input)
}

func (model Model) detailView() string {
	var builder strings.Builder
	detail := model.detail
	fmt.Fprintf(&builder, "%s\n\n", detail.Name)
	fmt.Fprintf(&builder, "GID: %s\n", detail.GID)
	fmt.Fprintf(&builder, "Status: %s\n", detail.Status)
	fmt.Fprintf(&builder, "URI: %s\n", detail.PrimaryURI)
	fmt.Fprintf(&builder, "Progress: %d/%d bytes\n", detail.CompletedLength, detail.TotalLength)
	fmt.Fprintf(&builder, "Speed: down %d B/s, up %d B/s\n", detail.DownloadSpeed, detail.UploadSpeed)
	fmt.Fprintf(&builder, "Connections: %d\n", detail.Connections)
	if detail.ErrorMessage != "" {
		fmt.Fprintf(&builder, "Error %s: %s\n", detail.ErrorCode, detail.ErrorMessage)
	}
	if len(detail.Files) > 0 {
		builder.WriteString("\nFiles:\n")
		for _, file := range detail.Files {
			fmt.Fprintf(&builder, "  %s %d/%d bytes\n", file.Name, file.CompletedLength, file.Length)
		}
	}
	builder.WriteString("\nEsc returns to list, q quits\n")
	return builder.String()
}

func (model Model) writeSection(builder *strings.Builder, title string, downloads []aria2.Download) {
	fmt.Fprintf(builder, "%s\n", title)
	if len(downloads) == 0 {
		builder.WriteString("  (none)\n")
		return
	}
	offset := model.sectionOffset(title)
	for index, download := range downloads {
		cursor := " "
		if model.selected == offset+index {
			cursor = ">"
		}
		fmt.Fprintf(builder, "%s %-8s %-10s %s %d/%d\n", cursor, download.GID, download.Status, download.Name, download.CompletedLength, download.TotalLength)
	}
}

func (model Model) sectionOffset(title string) int {
	switch title {
	case "Waiting":
		return len(model.snapshot.Active)
	default:
		if strings.HasPrefix(title, "Stopped") {
			return len(model.snapshot.Active) + len(model.snapshot.Waiting)
		}
		return 0
	}
}

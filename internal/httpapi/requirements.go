package httpapi

import (
	"errors"
	"io"
	"net/http"

	"github.com/nisha-ts-40599/blink-backend/internal/zipkit"
)

var (
	errUpload    = errors.New("Upload a document.")
	errEmptyFile = errors.New("The file is empty.")
)

func (s *Server) extractRequirement(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeErr(w, errUpload)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeErr(w, errUpload)
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 25<<20))
	if err != nil || len(data) == 0 {
		writeErr(w, errEmptyFile)
		return
	}
	text, err := zipkit.ExtractRequirement(header.Filename, data)
	if err != nil {
		writeErr(w, err)
		return
	}
	name := header.Filename
	if name == "" {
		name = "upload"
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"fileName": name,
		"text":     text,
	})
}

package zipkit

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"io"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/ledongthuc/pdf"
	"github.com/richardlehane/mscfb"
	"golang.org/x/text/encoding/charmap"
)

const maxRequirementChars = 60_000

var (
	errUnsupported = errors.New("Use a PDF, Word, TXT, or Markdown file.")
	errPDF         = errors.New("Could not read this PDF. Paste the requirements instead.")
	errPDFPassword = errors.New("This PDF is password-protected. Remove the password or paste the requirements.")
	errWord        = errors.New("Could not read this Word file. Save it as .docx or paste the requirements.")
	errNotPlain    = errors.New("This file is not plain text. Use PDF, Word, TXT, or Markdown.")
)

var (
	spaceRun  = regexp.MustCompile(`[ \t]{2,}`)
	lineTrail = regexp.MustCompile(` *\n`)
	blankRun  = regexp.MustCompile(`\n{3,}`)
)

// ExtractRequirement reads a PDF, Word, TXT, or Markdown upload into plain text.
func ExtractRequirement(fileName string, data []byte) (string, error) {
	if len(data) == 0 {
		return "", errors.New("The file is empty.")
	}
	ext := fileExtension(fileName)
	var raw string
	var err error
	switch ext {
	case "pdf":
		raw, err = readPDF(data)
	case "docx":
		raw, err = readDocx(data)
	case "doc":
		raw, err = readDoc(data)
	case "txt", "md", "markdown":
		raw, err = readPlain(data)
	default:
		return "", errUnsupported
	}
	if err != nil {
		return "", err
	}
	text := normalizeRequirement(raw)
	if text == "" {
		return "", emptyRequirementError(ext)
	}
	if len(text) > maxRequirementChars {
		text = strings.TrimRight(text[:maxRequirementChars], " \t\r\n") +
			"\n\n[Truncated. The rest of the document was not included. Shorten the file or paste the important part.]"
	}
	return text, nil
}

func emptyRequirementError(ext string) error {
	switch ext {
	case "pdf":
		return errors.New("This PDF has no selectable text. Paste the requirements instead.")
	case "doc", "docx":
		return errors.New("This Word file has no readable text. Paste the requirements instead.")
	default:
		return errors.New("The file has no readable text. Paste the requirements instead.")
	}
}

func readPDF(data []byte) (text string, err error) {
	defer func() {
		if recover() != nil {
			text = ""
			err = errPDF
		}
	}()
	reader, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		if encryptedPDF(err) {
			return "", errPDFPassword
		}
		return "", errPDF
	}
	var b strings.Builder
	fonts := map[string]*pdf.Font{}
	pages := reader.NumPage()
	if pages > 300 {
		pages = 300
	}
	for i := 1; i <= pages; i++ {
		page := reader.Page(i)
		if page.V.IsNull() {
			continue
		}
		plain, perr := page.GetPlainText(fonts)
		if perr != nil {
			if encryptedPDF(perr) {
				return "", errPDFPassword
			}
			continue
		}
		b.WriteString(plain)
		b.WriteByte('\n')
	}
	return b.String(), nil
}

func encryptedPDF(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "encrypt") || strings.Contains(msg, "password")
}

func readDocx(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", errWord
	}
	var parts []string
	for _, name := range []string{"word/document.xml"} {
		part, ok, err := docxPart(zr, name)
		if err != nil {
			return "", errWord
		}
		if ok {
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 {
		return "", errWord
	}
	for _, file := range zr.File {
		lower := strings.ToLower(file.Name)
		if strings.HasPrefix(lower, "word/header") || strings.HasPrefix(lower, "word/footer") {
			if !strings.HasSuffix(lower, ".xml") {
				continue
			}
			part, ok, err := docxPart(zr, file.Name)
			if err != nil {
				return "", errWord
			}
			if ok && strings.TrimSpace(part) != "" {
				parts = append(parts, part)
			}
		}
	}
	return strings.Join(parts, "\n"), nil
}

func docxPart(zr *zip.Reader, name string) (string, bool, error) {
	var file *zip.File
	for _, candidate := range zr.File {
		if candidate.Name == name {
			file = candidate
			break
		}
	}
	if file == nil {
		return "", false, nil
	}
	if file.UncompressedSize64 > 20<<20 {
		return "", false, errWord
	}
	rc, err := file.Open()
	if err != nil {
		return "", false, errWord
	}
	defer rc.Close()
	text, err := wordXMLText(io.LimitReader(rc, 20<<20))
	if err != nil {
		return "", false, errWord
	}
	return text, true, nil
}

func wordXMLText(r io.Reader) (string, error) {
	dec := xml.NewDecoder(r)
	var b strings.Builder
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		switch el := tok.(type) {
		case xml.StartElement:
			if !isWordML(el.Name) {
				continue
			}
			switch el.Name.Local {
			case "t":
				var text string
				if err := dec.DecodeElement(&text, &el); err != nil {
					return "", err
				}
				b.WriteString(text)
			case "tab":
				b.WriteByte(' ')
			case "br", "cr":
				b.WriteByte('\n')
			}
		case xml.EndElement:
			if isWordML(el.Name) && el.Name.Local == "p" {
				b.WriteByte('\n')
			}
		}
	}
	return b.String(), nil
}

func isWordML(name xml.Name) bool {
	return name.Space == "" || strings.Contains(name.Space, "wordprocessingml")
}

func readDoc(data []byte) (string, error) {
	reader, err := mscfb.New(bytes.NewReader(data))
	if err != nil {
		return "", errWord
	}
	streams := map[string][]byte{}
	for {
		file, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", errWord
		}
		payload, err := io.ReadAll(io.LimitReader(file, 20<<20))
		if err != nil {
			return "", errWord
		}
		streams[file.Name] = payload
	}
	word := streams["WordDocument"]
	if len(word) < 0x50 {
		return "", errWord
	}
	return textFromWord(word, streams["0Table"], streams["1Table"])
}

func textFromWord(word, table0, table1 []byte) (string, error) {
	if len(word) < 0x50 || binary.LittleEndian.Uint16(word[0:2]) != 0xA5EC {
		return "", errWord
	}
	flags := binary.LittleEndian.Uint16(word[0x0A : 0x0C])
	if flags&0x0100 != 0 || flags&0x8000 != 0 {
		return "", errWord
	}
	if flags&0x0004 == 0 {
		return simpleWordText(word, flags&0x1000 != 0), nil
	}
	primary, secondary := table0, table1
	if flags&0x0200 != 0 {
		primary, secondary = table1, table0
	}
	if text, ok := scanPieceTables(word, primary); ok {
		return text, nil
	}
	if text, ok := scanPieceTables(word, secondary); ok {
		return text, nil
	}
	return "", errWord
}

func simpleWordText(word []byte, extChar bool) string {
	fcMin := int(binary.LittleEndian.Uint32(word[0x18:0x1C]))
	ccp := int(binary.LittleEndian.Uint32(word[0x4C:0x50]))
	if ccp <= 0 || fcMin < 0 || fcMin >= len(word) {
		return ""
	}
	if extChar {
		end := fcMin + ccp*2
		if end > len(word) {
			end = len(word)
		}
		if (end-fcMin)%2 == 1 {
			end--
		}
		return utf16ToString(word[fcMin:end])
	}
	end := fcMin + ccp
	if end > len(word) {
		end = len(word)
	}
	return windows1252(word[fcMin:end])
}

func scanPieceTables(word, table []byte) (string, bool) {
	if len(table) < 16 {
		return "", false
	}
	best := ""
	bestScore := 0
	for i := 0; i+5 < len(table); i++ {
		if table[i] != 0x02 {
			continue
		}
		lcb := int(binary.LittleEndian.Uint32(table[i+1 : i+5]))
		if lcb < 16 || (lcb-4)%12 != 0 || i+5+lcb > len(table) {
			continue
		}
		n := (lcb - 4) / 12
		if n < 1 || n > 5000 {
			continue
		}
		text, ok := pieces(word, table[i+5:i+5+lcb], n)
		if !ok {
			continue
		}
		score := proseScore(text)
		if score > bestScore {
			best = text
			bestScore = score
		}
	}
	if bestScore < 1 {
		return "", false
	}
	return best, true
}

func pieces(word, plc []byte, n int) (string, bool) {
	cps := make([]int, n+1)
	for i := 0; i <= n; i++ {
		cps[i] = int(int32(binary.LittleEndian.Uint32(plc[i*4 : i*4+4])))
		if i > 0 && cps[i] <= cps[i-1] {
			return "", false
		}
	}
	pcdAt := (n + 1) * 4
	var b strings.Builder
	for i := 0; i < n; i++ {
		off := pcdAt + i*8
		if off+8 > len(plc) {
			return "", false
		}
		raw := binary.LittleEndian.Uint32(plc[off+2 : off+6])
		unicode := raw&0x40000000 == 0
		fc := int(raw & 0x3FFFFFFF)
		if !unicode {
			fc /= 2
		}
		chars := cps[i+1] - cps[i]
		if chars <= 0 || chars > 1_000_000 || fc < 0 {
			return "", false
		}
		if unicode {
			nbytes := chars * 2
			if fc+nbytes > len(word) {
				return "", false
			}
			b.WriteString(utf16ToString(word[fc : fc+nbytes]))
			continue
		}
		if fc+chars > len(word) {
			return "", false
		}
		b.WriteString(windows1252(word[fc : fc+chars]))
	}
	text := b.String()
	if strings.TrimSpace(text) == "" || proseScore(text) < 1 {
		return "", false
	}
	return text, true
}

func proseScore(text string) int {
	letters := 0
	bad := 0
	for _, r := range text {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			letters++
		case unicode.IsSpace(r) || strings.ContainsRune(".,;:'\"-()/&", r):
		case r < 32 && r != '\n' && r != '\t':
			bad++
		}
	}
	if bad > letters {
		return 0
	}
	return letters
}

func utf16ToString(b []byte) string {
	if len(b) < 2 {
		return ""
	}
	if len(b)%2 == 1 {
		b = b[:len(b)-1]
	}
	units := make([]uint16, len(b)/2)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(b[i*2:])
	}
	return string(utf16.Decode(units))
}

func windows1252(b []byte) string {
	decoded, err := charmap.Windows1252.NewDecoder().Bytes(b)
	if err != nil {
		return string(b)
	}
	return string(decoded)
}

func readPlain(data []byte) (string, error) {
	if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
		data = data[3:]
	}
	if looksBinary(data) {
		return "", errNotPlain
	}
	if utf8.Valid(data) {
		return string(data), nil
	}
	return windows1252(data), nil
}

func looksBinary(data []byte) bool {
	limit := len(data)
	if limit > 4096 {
		limit = 4096
	}
	suspicious := 0
	for i := 0; i < limit; i++ {
		value := data[i]
		if value == 0 {
			return true
		}
		if value < 0x09 {
			suspicious++
		}
	}
	return limit > 0 && suspicious > limit/10
}

func normalizeRequirement(raw string) string {
	text := strings.ReplaceAll(raw, "\u0000", "")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.ReplaceAll(text, "\f", "\n")
	text = spaceRun.ReplaceAllString(text, " ")
	text = lineTrail.ReplaceAllString(text, "\n")
	text = blankRun.ReplaceAllString(text, "\n\n")
	return strings.TrimSpace(text)
}

func fileExtension(fileName string) string {
	name := strings.TrimSpace(strings.ToLower(fileName))
	name = strings.ReplaceAll(name, "\\", "/")
	if slash := strings.LastIndex(name, "/"); slash >= 0 {
		name = name[slash+1:]
	}
	dot := strings.LastIndex(name, ".")
	if dot < 0 || dot == len(name)-1 {
		return ""
	}
	return name[dot+1:]
}

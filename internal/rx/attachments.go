package rx

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Вложение карточки это пара «название и адрес». Адрес бывает трёх видов:
// обычная ссылка, документ RX (гиперссылка Sungero?type=…&id=…) и файл в хранилище
// досок (storage://<Id>&<тип содержимого>), загруженный действием UploadPersistedBinaryData.

// docTypeGUID тип «электронный документ» платформы. Гиперссылка с типом-предком
// открывает документ любого вида, поэтому вид документа знать не нужно.
const docTypeGUID = "030d8d67-9b94-4f0d-bcc6-691016eb70f3"

// MaxAttachmentBytes предел размера загружаемого файла.
const MaxAttachmentBytes = 20 << 20

// AttachmentInput одно вложение: заполняется ровно одно из URL, File, DocumentID.
type AttachmentInput struct {
	URL        string
	File       string // путь к файлу на машине, где запущен сервер
	DocumentID int64
	Name       string // подпись; пусто = имя файла, название документа или сама ссылка
}

// Label короткое описание вложения для подтверждения.
func (a AttachmentInput) Label() string {
	switch {
	case a.File != "":
		return "файл " + a.File
	case a.DocumentID != 0:
		return fmt.Sprintf("документ #%d", a.DocumentID)
	default:
		return "ссылка " + a.URL
	}
}

type attRef struct {
	ID    int64  `json:"Id"`
	URL   string `json:"Url"`
	Name  string `json:"Name"`
	State string `json:"State"`
}

// resolveAttachments превращает вложения в записи для SaveTicket; файлы при этом
// загружаются в хранилище досок.
func (s *Service) resolveAttachments(ctx context.Context, in []AttachmentInput) ([]attRef, error) {
	out := make([]attRef, 0, len(in))
	for _, a := range in {
		n := 0
		for _, set := range []bool{a.URL != "", a.File != "", a.DocumentID != 0} {
			if set {
				n++
			}
		}
		if n != 1 {
			return nil, errors.New("у вложения должно быть задано ровно одно из: url, file, document_id")
		}
		ref := attRef{ID: newID, State: "Added", Name: strings.TrimSpace(a.Name)}
		switch {
		case a.URL != "":
			u, err := url.Parse(strings.TrimSpace(a.URL))
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return nil, fmt.Errorf("ссылка %q должна начинаться с http:// или https://", a.URL)
			}
			ref.URL = u.String()
			if ref.Name == "" {
				ref.Name = ref.URL
			}
		case a.DocumentID != 0:
			d, err := s.GetDocument(ctx, a.DocumentID)
			if err != nil {
				return nil, fmt.Errorf("документ #%d: %w", a.DocumentID, err)
			}
			base, err := url.Parse(s.c.Base())
			if err != nil {
				return nil, err
			}
			ref.URL = fmt.Sprintf("%s://%s/Sungero?type=%s&id=%d", base.Scheme, base.Host, docTypeGUID, a.DocumentID)
			if ref.Name == "" {
				ref.Name = d.Name
			}
		default:
			u, err := s.uploadFile(ctx, a.File)
			if err != nil {
				return nil, err
			}
			ref.URL = u
			if ref.Name == "" {
				ref.Name = filepath.Base(a.File)
			}
		}
		out = append(out, ref)
	}
	return out, nil
}

func (s *Service) uploadFile(ctx context.Context, path string) (string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("файл %s: %w", path, err)
	}
	if st.IsDir() {
		return "", fmt.Errorf("%s это каталог, нужен файл", path)
	}
	if st.Size() > MaxAttachmentBytes {
		return "", fmt.Errorf("файл %s больше %d МБ", path, MaxAttachmentBytes>>20)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	raw, err := s.c.Action(ctx, "AgileBoards", "UploadPersistedBinaryData", map[string]any{
		"file": base64.StdEncoding.EncodeToString(data),
	})
	if err != nil {
		return "", fmt.Errorf("загрузка %s: %w", filepath.Base(path), err)
	}
	var r struct {
		Value int64 `json:"value"`
	}
	if err := json.Unmarshal(raw, &r); err != nil || r.Value == 0 {
		// действие может вернуть и голое число
		if _, e := fmt.Sscanf(strings.TrimSpace(string(raw)), "%d", &r.Value); e != nil || r.Value == 0 {
			return "", fmt.Errorf("загрузка %s: хранилище не вернуло Id файла", filepath.Base(path))
		}
	}
	ct := mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
	if i := strings.Index(ct, ";"); i >= 0 {
		ct = ct[:i]
	}
	if ct == "" {
		ct = "application/octet-stream"
	}
	return fmt.Sprintf("storage://%d&%s", r.Value, ct), nil
}

// attachmentKind подпись вида вложения для карточки.
func attachmentKind(u string) string {
	switch {
	case strings.HasPrefix(u, "storage://"):
		return "файл"
	case idFromURL(u) != 0:
		return fmt.Sprintf("id %d", idFromURL(u))
	default:
		return u
	}
}

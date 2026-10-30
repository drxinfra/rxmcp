package server_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func pngBytes(t *testing.T) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 40, 20))
	for x := 0; x < 40; x++ {
		img.Set(x, 10, color.RGBA{255, 0, 0, 255})
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// Картинки карточки приходят изображениями в rx_ticket, вложения читаются по номеру.
func TestTicketImagesAndFiles(t *testing.T) {
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Write([]byte("ERROR blocks_in\n"))
	zw.Close()
	files := map[float64][]byte{
		101: pngBytes(t), // в описании
		102: pngBytes(t), // в комментарии
		103: []byte(`{"ключ": "значение из json"}`),
		104: gz.Bytes(),
	}
	var downloads []float64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/ITickets(5)"):
			json.NewEncoder(w).Encode(map[string]any{"Id": 5, "Name": "Падает импорт", "Uid": "AB-1", "BoardId": 9, "CommentsCount": 1,
				"Description": "Ошибка:\n\n ![](storage://101&image/png)\n\n",
				"Attachments": []any{
					map[string]any{"Name": "настройки.json", "Url": "storage://103&application/json"},
					map[string]any{"Name": "worker.log.gz", "Url": "storage://104&application/x-gzip"},
					map[string]any{"Name": "Требование", "Url": "http://127.0.0.1/Sungero?type=abc&id=300"},
					map[string]any{"Name": "Таблица", "Url": "https://docs.example/t"},
					map[string]any{"Name": "С другого стенда", "Url": "https://other.example/Sungero?type=abc&id=301"},
				}})
		case strings.HasSuffix(r.URL.Path, "/TeamsCommonAPI/LoadComments"):
			json.NewEncoder(w).Encode(map[string]any{"value": []any{map[string]any{"Id": 7, "Text": "и так ![](storage://102&image/png)", "Author": map[string]any{"Id": 1, "Name": "Петров"}}}})
		case strings.HasSuffix(r.URL.Path, "/AgileBoards/GetPersistedBinaryData"):
			b, _ := io.ReadAll(r.Body)
			var m map[string]float64
			json.Unmarshal(b, &m)
			downloads = append(downloads, m["binaryId"])
			json.NewEncoder(w).Encode(map[string]any{"value": files[m["binaryId"]]})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	cs := connect(t, false, srv.URL+"/odata")
	call := func(name string, args map[string]any) *mcp.CallToolResult {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	res := call("rx_ticket", map[string]any{"id": 5})
	head := res.Content[0].(*mcp.TextContent).Text
	for _, want := range []string{"1. настройки.json · файл json", "5. С другого стенда · ссылка · https://other.example/", "6. картинка 1.png · картинка png · описание", "7. картинка 1.png · картинка png · комментарий #7", "Ниже картинки карточки № 6, 7", "[картинка 6]", "и так [картинка 7]"} {
		if !strings.Contains(head, want) {
			t.Errorf("нет %q в\n%s", want, head)
		}
	}
	if len(res.Content) != 3 {
		t.Fatalf("ждали текст и 2 картинки, пришло %d частей", len(res.Content))
	}
	if im, ok := res.Content[1].(*mcp.ImageContent); !ok || im.MIMEType != "image/jpeg" || len(im.Data) == 0 {
		t.Errorf("картинка: %#v", res.Content[1])
	}

	res = call("rx_ticket", map[string]any{"id": 5, "text_only": true})
	if len(res.Content) != 1 || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, "Картинок у карточки: 2, не показаны") {
		t.Errorf("text_only: %d частей", len(res.Content))
	}

	text := func(r *mcp.CallToolResult) string { return r.Content[0].(*mcp.TextContent).Text }
	if out := text(call("rx_ticket_file", map[string]any{"id": 5, "item": 1})); !strings.Contains(out, "значение из json") || !strings.Contains(out, "файл 1 «настройки.json» (json") {
		t.Errorf("json: %s", out)
	}
	if out := text(call("rx_ticket_file", map[string]any{"id": 5, "item": 2})); !strings.Contains(out, "распакован") || !strings.Contains(out, "ERROR blocks_in") {
		t.Errorf("gz: %s", out)
	}
	if out := text(call("rx_ticket_file", map[string]any{"id": 5, "item": 2, "grep": "error"})); !strings.Contains(out, "строк с «error»: 1") || !strings.Contains(out, "1: ERROR blocks_in") {
		t.Errorf("grep: %s", out)
	}
	if r := call("rx_ticket_file", map[string]any{"id": 5, "item": 3}); !r.IsError || !strings.Contains(text(r), "объект RX id 300") {
		t.Errorf("ссылка на объект RX: %s", text(r))
	}
	if out := text(call("rx_ticket_file", map[string]any{"id": 5, "item": 4})); !strings.Contains(out, "внешняя ссылка") {
		t.Errorf("внешняя ссылка: %s", out)
	}
	if out := text(call("rx_ticket_file", map[string]any{"id": 5})); !strings.Contains(out, "Файлы карточки AB-1 (id 5): 7") {
		t.Errorf("список: %s", out)
	}
	if r := call("rx_ticket_file", map[string]any{"id": 5, "item": 6}); len(r.Content) != 2 {
		t.Errorf("картинка по номеру: %d частей", len(r.Content))
	}
	if out := text(call("rx_ticket_file", map[string]any{"id": 5, "item": 5})); !strings.Contains(out, "внешняя ссылка") {
		t.Errorf("ссылка на другой стенд читается как свой документ: %s", out)
	}
	if r := call("rx_ticket_file", map[string]any{"id": 5, "item": 9}); !r.IsError {
		t.Error("несуществующий номер")
	}
}

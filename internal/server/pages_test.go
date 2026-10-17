package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestDocumentTextScanAndImage(t *testing.T) {
	scan, err := os.ReadFile("../textx/testdata/scan-g4.pdf")
	if err != nil {
		t.Fatal(err)
	}
	var pic bytes.Buffer
	png.Encode(&pic, image.NewGray(image.Rect(0, 0, 400, 300)))
	docs := map[string]struct {
		ext  string
		body []byte
	}{"7": {"pdf", scan}, "8": {"png", pic.Bytes()}, "9": {"", scan}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/odata/")
		for id, d := range docs {
			switch p {
			case "IElectronicDocuments(" + id + ")":
				w.Header().Set("Content-Type", "application/json")
				n, _ := strconv.Atoi(id)
				json.NewEncoder(w).Encode(map[string]any{"Id": n, "Name": "Скан " + id, "HasVersions": true,
					"Versions": []any{map[string]any{"Id": 1, "Number": 1, "AssociatedApplication": map[string]any{"Extension": d.ext}}}})
				return
			case "IElectronicDocuments(" + id + ")/Versions(1)/Body/$value":
				w.Write(d.body)
				return
			}
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()
	cs := connect(t, false, srv.URL+"/odata")
	for _, c := range []struct {
		id   int
		want string
	}{{7, "скан, страниц-картинок 1, показаны 1–1"}, {8, "изображение"}, {9, "скан"}} {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "rx_get_document_text", Arguments: map[string]any{"id": c.id}})
		if err != nil || res.IsError {
			t.Fatalf("#%d: %v %s", c.id, err, res.Content[0].(*mcp.TextContent).Text)
		}
		if len(res.Content) != 2 {
			t.Fatalf("#%d: ждали текст и картинку, пришло %d частей", c.id, len(res.Content))
		}
		if txt := res.Content[0].(*mcp.TextContent).Text; !strings.Contains(txt, c.want) {
			t.Errorf("#%d: %s", c.id, txt)
		}
		im, ok := res.Content[1].(*mcp.ImageContent)
		if !ok || im.MIMEType != "image/jpeg" || len(im.Data) < 1000 {
			t.Errorf("#%d: картинка %T %+v", c.id, res.Content[1], ok)
		}
	}
}

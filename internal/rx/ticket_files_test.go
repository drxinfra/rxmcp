package rx

import "testing"

func TestTicketFiles(t *testing.T) {
	tk := &Ticket{Description: "Ошибка:\n\n ![](storage://12783&image/png)Ещё ![схема](storage://12784&image/jpeg) и повтор ![](storage://12783&image/png), [лог](storage://12790&text/plain)"}
	tk.Attachments = append(tk.Attachments,
		struct {
			Name string `json:"Name"`
			URL  string `json:"Url"`
		}{"отметки.docx", "storage://12660&application/vnd.openxmlformats-officedocument.wordprocessingml.document"},
		struct {
			Name string `json:"Name"`
			URL  string `json:"Url"`
		}{"Требование №292", "https://rx.example/Sungero?type=6b39&id=292"},
		struct {
			Name string `json:"Name"`
			URL  string `json:"Url"`
		}{"https://docs.example/x", "https://docs.example/x"},
		struct {
			Name string `json:"Name"`
			URL  string `json:"Url"`
		}{"План.mpp", "storage://12623&"},
	)
	cs := []Comment{{ID: 77, Text: "вот так ![](storage://12800&image/png)"}}
	fs := TicketFiles(tk, cs, "rx.example")
	want := []string{
		"1. отметки.docx · файл docx",
		"2. Требование №292 · объект RX id 292",
		"3. https://docs.example/x · ссылка",
		"4. План.mpp · файл mpp",
		"5. картинка 1.png · картинка png · описание",
		"6. схема · картинка jpg · описание",
		"7. лог · файл txt · описание",
		"8. картинка 1.png · картинка png · комментарий #77",
	}
	if len(fs) != len(want) {
		for _, f := range fs {
			t.Log(f.Line())
		}
		t.Fatalf("файлов %d, ждали %d", len(fs), len(want))
	}
	for i, w := range want {
		if got := fs[i].Line(); got != w {
			t.Errorf("%d: %q, ждали %q", i+1, got, w)
		}
	}
	if !fs[4].IsImage() || fs[0].IsImage() || fs[6].IsImage() || fs[4].StorageID != 12783 {
		t.Errorf("картинки определены не так: %+v", fs)
	}
	if got := ReplaceStorageLinks("Ошибка: ![](storage://12783&image/png) и [лог](storage://12790&text/plain)", fs); got != "Ошибка: [картинка 5] и [файл 7 «лог»]" {
		t.Errorf("замена ссылок: %q", got)
	}
	// Ссылка на объект другого стенда: её id здесь читать нельзя.
	other := &Ticket{}
	other.Attachments = tk.Attachments[1:2]
	if f := TicketFiles(other, nil, "rx.other.example"); f[0].DocID != 0 || f[0].Kind() != "ссылка" {
		t.Errorf("чужой стенд: %+v", f[0])
	}
	if _, _, ok := ParseStorage("storage://abc&image/png"); ok {
		t.Error("кривой адрес storage принят")
	}
}

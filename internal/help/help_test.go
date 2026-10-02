package help

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

const toc = `<html><head><title>Справка Directum RX 25.2</title></head><body><ul id="toc">
<li class="heading1" id="i1" onclick="x"><a class="heading1" id="a1" href="admin.htm" target="hmcontent"><span class="heading1" id="s1">Администрирование</span></a>
<ul id="ul1">
<li class="heading2" id="i1.1"><a class="heading2" href="approval_rules.htm#top"><span class="heading2">Правила согласования</span></a></li>
<li class="heading2" id="i1.2"><a class="heading2" href="backup.htm"><span class="heading2">Резервное копирование &amp; восстановление</span></a></li>
</ul></li>
<li class="heading1" id="i2"><a class="heading1" href="http://example.com/x.htm"><span>Внешняя</span></a></li>
</ul></body></html>`

func page(body string) []byte {
	return []byte(`<html><body><div id="printheader"><h1>t</h1></div><div id="idcontent"><!--ZOOMRESTART-->` + body + `<!--ZOOMSTOP--><script>var x = "мусор";</script></div></body></html>`)
}

var pages = map[string][]byte{
	"hmcontent.htm":      []byte(toc),
	"admin.htm":          page(`<p>Раздел для администраторов системы.</p>`),
	"approval_rules.htm": page(`<p>Правило согласования определяет <b>этапы</b> и исполнителей.</p><table><tr><td>Этап</td><td>Срок</td></tr><tr><td>Согласование с&nbsp;руководителем</td><td>2 дня</td></tr></table><p>См. также <a href="backup.htm#x">резервное копирование</a> и <a href="https://example.com/">внешний сайт</a>.</p><ul><li>первый</li><li>второй</li></ul>`),
	"backup.htm":         page(`<p>Резервные копии базы данных делаются по расписанию.</p>`),
}

func fetchFake(_ string) Fetch {
	return func(_ context.Context, u string) ([]byte, string, error) {
		name := u[strings.LastIndexByte(u, '/')+1:] // после перенаправления база меняется, имя файла остаётся
		if b, ok := pages[name]; ok {
			return b, "https://rx.example/Client/solution/WebHelp/ru-RU/" + name, nil
		}
		return nil, "", errors.New("404 " + u)
	}
}

func TestParseTOC(t *testing.T) {
	product, topics := ParseTOC([]byte(toc))
	if product != "Справка Directum RX 25.2" || len(topics) != 3 {
		t.Fatalf("product=%q topics=%d", product, len(topics))
	}
	if topics[1].File != "approval_rules.htm" || topics[1].Title != "Правила согласования" || strings.Join(topics[1].Crumbs, ">") != "Администрирование" {
		t.Errorf("topic 1: %+v", topics[1])
	}
	if topics[2].Title != "Резервное копирование & восстановление" {
		t.Errorf("entities: %q", topics[2].Title)
	}
}

func TestParseTopic(t *testing.T) {
	txt := ParseTopic(pages["approval_rules.htm"])
	for _, want := range []string{"Правило согласования определяет этапы и исполнителей.", "Этап | Срок", "Согласование с руководителем | 2 дня", "[резервное копирование](backup.htm)", "внешний сайт", "- первый"} {
		if !strings.Contains(txt, want) {
			t.Errorf("нет %q в:\n%s", want, txt)
		}
	}
	for _, bad := range []string{"мусор", "<", "example.com"} {
		if strings.Contains(txt, bad) {
			t.Errorf("лишнее %q в:\n%s", bad, txt)
		}
	}
}

func TestStem(t *testing.T) {
	for _, p := range [][2]string{{"согласования", "согласование"}, {"правила", "правило"}, {"копирование", "копирования"}, {"настроить", "настроил"}} {
		if stem(p[0]) != stem(p[1]) {
			t.Errorf("%s → %s, %s → %s", p[0], stem(p[0]), p[1], stem(p[1]))
		}
	}
	if stem("OData") != "OData" || stem("бд") != "бд" {
		t.Error("латиница и короткие слова не должны меняться")
	}
}

func TestCrawlSaveLoadSearch(t *testing.T) {
	const base = "https://rx.example/Client/WebHelp/ru-RU/"
	ix, err := Crawl(context.Background(), fetchFake(base), base, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ix.Base != "https://rx.example/Client/solution/WebHelp/ru-RU/" {
		t.Errorf("база после перенаправления: %s", ix.Base)
	}
	p := filepath.Join(t.TempDir(), "h.idx.gz")
	if err := ix.Save(p); err != nil {
		t.Fatal(err)
	}
	ix2, err := Load(p)
	if err != nil || len(ix2.Topics) != 3 {
		t.Fatalf("load: %v %d", err, len(ix2.Topics))
	}
	hits := ix2.Search("как настроить правила согласований", "", 5)
	if len(hits) == 0 || hits[0].Topic.File != "approval_rules.htm" {
		t.Fatalf("поиск: %+v", hits)
	}
	if !strings.Contains(hits[0].Snippet, "согласован") {
		t.Errorf("сниппет: %q", hits[0].Snippet)
	}
	if h := ix2.Search("резервная копия базы", "", 5); len(h) == 0 || h[0].Topic.File != "backup.htm" {
		t.Errorf("поиск бэкапа: %+v", h)
	}
	if h := ix2.Search("копирование", "нет такого раздела", 5); len(h) != 0 {
		t.Errorf("фильтр раздела не сработал: %d", len(h))
	}
	if ix2.Topic("backup") == nil || ix2.Topic("backup.htm") == nil || ix2.Topic("nope") != nil {
		t.Error("Topic по имени")
	}
	if _, err := Load(filepath.Join(t.TempDir(), "none")); !errors.Is(err, ErrNoIndex) {
		t.Errorf("нет файла: %v", err)
	}
	if got := Path("/d", "https://rx.company.ru:8443/Integration"); got != "/d/help-rx.company.ru_8443.idx.gz" {
		t.Errorf("Path: %s", got)
	}
}

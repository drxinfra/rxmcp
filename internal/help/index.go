package help

import (
	"compress/gzip"
	"context"
	"encoding/gob"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Index справка одного стенда.
type Index struct {
	Base    string    // адрес каталога справки после перенаправлений, со слэшем на конце
	Product string    // заголовок оглавления, например «Справка Directum RX 25.2»
	Built   time.Time // когда скачана
	Topics  []Topic

	once   sync.Once
	post   map[string][]posting // основа слова → статьи
	dlen   []float64            // длина статьи в словах
	avg    float64
	byFile map[string]int
	title  []map[string]bool // основы слов заголовка статьи
	crumb  []map[string]bool // основы слов пути по оглавлению
}

type posting struct {
	doc int32
	tf  float32 // взвешенная частота: заголовок весит больше текста
}

// Fetch читает абсолютный адрес и возвращает тело и итоговый адрес после перенаправлений.
type Fetch func(ctx context.Context, url string) (body []byte, final string, err error)

// Crawl скачивает оглавление и все статьи. progress вызывается после каждой статьи.
func Crawl(ctx context.Context, fetch Fetch, base string, workers int, progress func(done, total int)) (*Index, error) {
	base = strings.TrimRight(base, "/") + "/"
	page, final, err := fetch(ctx, base+"hmcontent.htm")
	if err != nil {
		return nil, fmt.Errorf("оглавление справки %shmcontent.htm: %w", base, err)
	}
	product, topics := ParseTOC(page)
	if len(topics) == 0 {
		return nil, fmt.Errorf("по адресу %shmcontent.htm нет оглавления справки: задайте адрес каталога справки в RXMCP_HELP_URL", base)
	}
	if i := strings.LastIndexByte(final, '/'); i >= 0 {
		base = final[:i+1]
	}
	if workers < 1 {
		workers = 6
	}
	ix := &Index{Base: base, Product: product, Built: time.Now(), Topics: topics}
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		done   int
		failed int
		jobs   = make(chan int)
	)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				var body []byte
				var err error
				for attempt := 0; attempt < 3; attempt++ {
					if body, _, err = fetch(ctx, base+ix.Topics[i].File); err == nil || ctx.Err() != nil {
						break
					}
					time.Sleep(time.Duration(attempt+1) * 700 * time.Millisecond)
				}
				mu.Lock()
				if err != nil {
					failed++
				} else {
					ix.Topics[i].Text = ParseTopic(body)
				}
				done++
				if progress != nil {
					progress(done, len(ix.Topics))
				}
				mu.Unlock()
			}
		}()
	}
	for i := range ix.Topics {
		if ctx.Err() != nil {
			break
		}
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if failed > len(ix.Topics)/5 {
		return nil, fmt.Errorf("не скачалось %d статей из %d: стенд недоступен или режет запросы", failed, len(ix.Topics))
	}
	return ix, nil
}

// Save пишет индекс на диск: сначала во временный файл, затем переименование.
func (ix *Index) Save(path string) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	zw := gzip.NewWriter(f)
	err = gob.NewEncoder(zw).Encode(struct {
		Base, Product string
		Built         time.Time
		Topics        []Topic
	}{ix.Base, ix.Product, ix.Built, ix.Topics})
	if e := zw.Close(); err == nil {
		err = e
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// ErrNoIndex индекс ещё не построен.
var ErrNoIndex = errors.New("справка не проиндексирована")

// Load читает индекс с диска.
func Load(path string) (*Index, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNoIndex
		}
		return nil, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	ix := &Index{}
	var v struct {
		Base, Product string
		Built         time.Time
		Topics        []Topic
	}
	if err := gob.NewDecoder(zr).Decode(&v); err != nil {
		return nil, fmt.Errorf("%s повреждён, перестройте: rxmcp docs index: %w", filepath.Base(path), err)
	}
	ix.Base, ix.Product, ix.Built, ix.Topics = v.Base, v.Product, v.Built, v.Topics
	return ix, nil
}

// Path имя файла индекса для адреса RX: у каждого стенда своя справка.
func Path(dir, rxURL string) string {
	host := rxURL
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	if i := strings.IndexAny(host, "/?"); i >= 0 {
		host = host[:i]
	}
	host = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '-' {
			return r
		}
		return '_'
	}, host)
	return filepath.Join(dir, "help-"+host+".idx.gz")
}

// --- поиск ---

var stop = map[string]bool{"и": true, "в": true, "на": true, "с": true, "по": true, "для": true, "как": true, "что": true, "это": true,
	"не": true, "к": true, "из": true, "о": true, "от": true, "до": true, "за": true, "при": true, "или": true, "а": true, "у": true, "же": true, "ли": true, "то": true}

// Окончания проверяются от длинных к коротким: порядок в списке важен.
var suffixes = []string{"иями", "ями", "ами", "иях", "ией", "ого", "его", "ому", "ему", "ыми", "ими", "ешь", "ишь", "ете", "ите", "ует", "уют",
	"ить", "ать", "ять", "еть", "ила", "ала", "яла", "или", "али", "яли", "ило", "ало",
	"ая", "яя", "ое", "ее", "ые", "ие", "ый", "ий", "ой", "ей", "ом", "ем", "ам", "ям", "ах", "ях", "ов", "ев", "ть", "ти", "ет", "ют", "ут", "ит", "ат", "ят",
	"ил", "ал", "ял", "ел", "ла", "ло", "ли", "ия", "ию", "ии",
	"а", "я", "о", "е", "ы", "и", "у", "ю", "ь", "й"}

// stem грубо отрезает русское окончание: «согласования» и «согласование» дают одну основу.
// Латиница и короткие слова остаются как есть.
func stem(w string) string {
	r := []rune(w)
	if len(r) < 5 || r[len(r)-1] < 0x430 {
		return w
	}
	if n := len(r); n > 6 && (string(r[n-2:]) == "ся" || string(r[n-2:]) == "сь") {
		r = r[:n-2]
	}
	for _, s := range suffixes {
		sr := []rune(s)
		if len(r)-len(sr) >= 3 && string(r[len(r)-len(sr):]) == s {
			return string(r[:len(r)-len(sr)])
		}
	}
	return string(r)
}

// Tokens разбивает текст на основы слов без служебных.
func Tokens(s string) []string {
	s = strings.ReplaceAll(strings.ToLower(s), "ё", "е")
	var out []string
	for _, w := range strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' }) {
		if len(w) < 2 || stop[w] {
			continue
		}
		out = append(out, stem(w))
	}
	return out
}

func (ix *Index) build() {
	ix.post = map[string][]posting{}
	ix.dlen = make([]float64, len(ix.Topics))
	ix.byFile = make(map[string]int, len(ix.Topics))
	ix.title = make([]map[string]bool, len(ix.Topics))
	ix.crumb = make([]map[string]bool, len(ix.Topics))
	var total float64
	for i, t := range ix.Topics {
		ix.byFile[t.File] = i
		tf := map[string]float32{}
		body := Tokens(t.Text)
		for _, w := range body {
			tf[w]++
		}
		ix.title[i], ix.crumb[i] = map[string]bool{}, map[string]bool{}
		for _, w := range Tokens(t.Title) {
			tf[w] += 5
			ix.title[i][w] = true
		}
		for _, w := range Tokens(strings.Join(t.Crumbs, " ")) {
			tf[w] += 1.5
			ix.crumb[i][w] = true
		}
		ix.dlen[i] = float64(len(body)) + 20
		total += ix.dlen[i]
		for w, f := range tf {
			ix.post[w] = append(ix.post[w], posting{int32(i), f})
		}
	}
	if len(ix.Topics) > 0 {
		ix.avg = total / float64(len(ix.Topics))
	}
}

// Hit результат поиска.
type Hit struct {
	Topic   *Topic
	Score   float64
	Snippet string
}

// Search ищет статьи по запросу (BM25 с весом заголовка). section — подстрока пути по оглавлению.
func (ix *Index) Search(query, section string, limit int) []Hit {
	ix.once.Do(ix.build)
	q := uniq(Tokens(query))
	if len(q) == 0 {
		return nil
	}
	if limit <= 0 {
		limit = 8
	}
	const k1, bb = 1.4, 0.6
	n := float64(len(ix.Topics))
	score := map[int32]float64{}
	matched := map[int32]int{}
	for _, w := range q {
		ps := ix.post[w]
		if len(ps) == 0 {
			continue
		}
		idf := math.Log(1 + (n-float64(len(ps))+0.5)/(float64(len(ps))+0.5))
		for _, p := range ps {
			tf := float64(p.tf)
			score[p.doc] += idf * tf * (k1 + 1) / (tf + k1*(1-bb+bb*ix.dlen[p.doc]/ix.avg))
			matched[p.doc]++
		}
	}
	section = strings.ToLower(section)
	hits := make([]Hit, 0, len(score))
	for d, sc := range score {
		t := &ix.Topics[d]
		if section != "" && !strings.Contains(strings.ToLower(strings.Join(t.Crumbs, " > ")+" > "+t.Title), section) {
			continue
		}
		// статья, где нашлись все слова запроса, важнее статьи с одним частым словом
		cover := float64(matched[d]) / float64(len(q))
		// статья, чей заголовок сам отвечает на запрос, важнее статьи, где слова рассыпаны по тексту
		var inTitle, inCrumb float64
		for _, w := range q {
			if ix.title[d][w] {
				inTitle++
			} else if ix.crumb[d][w] {
				inCrumb++
			}
		}
		boost := 1 + 2*inTitle/float64(len(q)) + 0.6*inCrumb/float64(len(q))
		// короткий заголовок, целиком покрытый запросом («Замещения»), получает ещё немного
		if n := len(ix.title[d]); n > 0 && inTitle == float64(n) {
			boost += 0.5
		}
		hits = append(hits, Hit{Topic: t, Score: sc * cover * cover * boost})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].Topic.File < hits[j].Topic.File
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	qs := map[string]bool{}
	for _, w := range q {
		qs[w] = true
	}
	for i := range hits {
		hits[i].Snippet = snippet(hits[i].Topic.Text, qs, 320)
	}
	return hits
}

// Topic статья по имени файла.
func (ix *Index) Topic(file string) *Topic {
	ix.once.Do(ix.build)
	file = strings.TrimSpace(file)
	if i, ok := ix.byFile[file]; ok {
		return &ix.Topics[i]
	}
	if i, ok := ix.byFile[file+".htm"]; ok {
		return &ix.Topics[i]
	}
	return nil
}

// URL адрес статьи на стенде.
func (ix *Index) URL(t *Topic) string { return ix.Base + t.File }

func uniq(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, w := range in {
		if !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	return out
}

// snippet выбирает абзац, где встречается больше всего разных слов запроса.
func snippet(text string, q map[string]bool, max int) string {
	best, bestN := "", -1
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if len(line) < 20 {
			continue
		}
		seen := map[string]bool{}
		for _, w := range Tokens(line) {
			if q[w] {
				seen[w] = true
			}
		}
		if len(seen) > bestN {
			best, bestN = line, len(seen)
		}
	}
	r := []rune(best)
	if len(r) > max {
		return string(r[:max]) + "…"
	}
	return best
}

package textx

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"strings"
	"testing"
)

func TestUnpack(t *testing.T) {
	var zbuf bytes.Buffer
	zw := zip.NewWriter(&zbuf)
	w, _ := zw.Create("logs/app.log")
	w.Write([]byte(strings.Repeat("x", 2048)))
	zw.Create("logs/")
	zw.Close()
	l, _, _, ok, err := Unpack(zbuf.Bytes(), "zip", "logs.zip")
	if err != nil || !ok || !strings.Contains(l, "Архив zip, файлов 1") || !strings.Contains(l, "logs/app.log · 2 КБ") {
		t.Errorf("zip: %q %v %v", l, ok, err)
	}

	var gbuf bytes.Buffer
	gw := gzip.NewWriter(&gbuf)
	gw.Write([]byte("2026-10-29 ERROR очередь blocks_in\n"))
	gw.Close()
	l, inner, ext, ok, err := Unpack(gbuf.Bytes(), "gz", "worker.log.gz")
	if err != nil || !ok || l != "" || ext != "log" || !strings.Contains(string(inner), "blocks_in") {
		t.Errorf("gz: %q %q %v %v", ext, inner, ok, err)
	}

	var tbuf bytes.Buffer
	gw = gzip.NewWriter(&tbuf)
	tw := tar.NewWriter(gw)
	tw.WriteHeader(&tar.Header{Name: "snap/pods.yaml", Mode: 0o644, Size: 3, Typeflag: tar.TypeReg})
	tw.Write([]byte("a: 1"[:3]))
	tw.Close()
	gw.Close()
	l, _, _, ok, err = Unpack(tbuf.Bytes(), "gz", "snap.tar.gz")
	if err != nil || !ok || !strings.Contains(l, "Архив tar, файлов 1") || !strings.Contains(l, "snap/pods.yaml") {
		t.Errorf("tar.gz: %q %v %v", l, ok, err)
	}

	if b, e, err := ArchiveFile(tbuf.Bytes(), "gz", "snap/pods.yaml"); err != nil || e != "yaml" || string(b) != "a: " {
		t.Errorf("файл из tar.gz: %q %q %v", b, e, err)
	}
	if b, e, err := ArchiveFile(zbuf.Bytes(), "zip", "logs/app.log"); err != nil || e != "log" || len(b) != 2048 {
		t.Errorf("файл из zip: %d %q %v", len(b), e, err)
	}
	if _, _, err := ArchiveFile(zbuf.Bytes(), "zip", "нет.txt"); err == nil {
		t.Error("несуществующий файл в архиве")
	}
	if _, _, _, ok, _ := Unpack([]byte("просто текст"), "txt", "a.txt"); ok {
		t.Error("текст не архив")
	}
}

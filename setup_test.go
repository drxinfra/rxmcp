package main

import "testing"

func TestNormalizeRX(t *testing.T) {
	cases := map[string]string{
		"https://rx.example":                             "https://rx.example/integration",
		"rx.example":                                     "https://rx.example/integration",
		"https://rx.example/":                            "https://rx.example/integration",
		"https://rx.example/Integration":                 "https://rx.example/Integration",
		"https://rx.example/Integration/odata":           "https://rx.example/Integration",
		"https://rx.example/integration/odata/$metadata": "https://rx.example/integration",
		"http://rx.example:8080/Integration":             "http://rx.example:8080/Integration",
	}
	for in, want := range cases {
		got, err := normalizeRX(in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("%q -> %q, ожидалось %q", in, got, want)
		}
	}
	if _, err := normalizeRX("   "); err == nil {
		t.Error("пустой адрес должен быть ошибкой")
	}
}

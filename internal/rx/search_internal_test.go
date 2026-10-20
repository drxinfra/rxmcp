package rx

import "testing"

func TestStem(t *testing.T) {
	for in, want := range map[string]string{
		"согласование": "согласован", "согласования": "согласован", "договоров": "договор",
		"Договор": "Договор", "удалённой": "удалённ", "паролей": "парол", "Петров": "Петров",
		"Иванова": "Иванов", "бухгалтерии": "бухгалтер", "plan": "plan", "№17": "№17", "План": "План",
	} {
		if got := stem(in); got != want {
			t.Errorf("stem(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWordsFilter(t *testing.T) {
	got := wordsFilter("Регламент согласования договоров", "Name")
	want := "contains(tolower(Name),'регламент') and contains(tolower(Name),'согласован') and contains(tolower(Name),'договор')"
	if got != want {
		t.Errorf("one field:\n%s\n%s", got, want)
	}
	got = wordsFilter("начальник по закупкам", "Name", "JobTitle/Name")
	want = "(contains(tolower(Name),'начальник') or contains(tolower(JobTitle/Name),'начальник')) and (contains(tolower(Name),'закупк') or contains(tolower(JobTitle/Name),'закупк'))"
	if got != want {
		t.Errorf("two fields:\n%s\n%s", got, want)
	}
	if got := wordsFilter("ИТ", "Name"); got != "contains(tolower(Name),'ит')" {
		t.Errorf("short: %s", got)
	}
}

func TestNormStatus(t *testing.T) {
	a := []string{"in_process", "overdue", "unread", "completed", "all"}
	for in, want := range map[string]string{
		"": "", "overdue": "overdue", "in_process, overdue": "in_process", "Overdue, Unread": "in_process",
		"In Progress": "in_process", "done": "completed", "in_process,completed": "all", "all, overdue": "all",
		"срочное": "срочное",
	} {
		if got := normStatus(in, a...); got != want {
			t.Errorf("normStatus(%q) = %q, want %q", in, got, want)
		}
	}
}

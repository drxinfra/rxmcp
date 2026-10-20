package config

import "testing"

func TestTimeZone(t *testing.T) {
	t.Setenv("RXMCP_HOME", t.TempDir())
	t.Setenv("RXMCP_URL", "https://rx.example.test/Integration")
	t.Setenv("RXMCP_LOGIN", "u")
	t.Setenv("RXMCP_PASSWORD", "p")
	t.Setenv("RXMCP_TZ", "Europe/Moscow")
	c, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if loc := c.Location(); loc.String() != "Europe/Moscow" {
		t.Errorf("пояс %s", loc)
	}
	c.TimeZone = "Марс/Олимп"
	if err := c.Validate(); err == nil {
		t.Error("неизвестный пояс принят")
	}
}
